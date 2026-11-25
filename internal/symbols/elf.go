package symbols

import (
	"bytes"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Android's native libraries (docs: phase 3b, 2026-11-25): an ELF shared library built by the
// NDK, read into the same map as a dSYM. A native crash's tombstone names each frame's library
// by its build id (the GNU build-id note, as lower-case hex) and its address in the library
// (`rel_pc`, already the call's own instruction for frames below the top), so the map is keyed
// by that build id and its offsets are the library's own addresses (TextAddr 0).

// IsELF says whether the file at path starts as an ELF file does.
func IsELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	_, err = io.ReadFull(f, magic)
	return err == nil && bytes.Equal(magic, []byte(elf.ELFMAG))
}

// ExtractELF reads a native library with its debug information: an unstripped .so (what Gradle
// keeps under build/intermediates) or the debug file of one (native-debug-symbols.zip's .so.dbg).
// name is the library's name as a crash names it ("libgame.so").
func ExtractELF(r io.ReaderAt, name string) (*Map, error) {
	return extractELF(r, name, nil)
}

// ExtractDart reads a Flutter build's Dart debug information (--split-debug-info's
// app.<platform>-<arch>.symbols, an ELF file whose build id the build's stacks print): the same
// map as a native library's, with the Dart SDK's and packages' files named as Dart names them
// (dart:async/future.dart, package:flutter/src/widgets/framework.dart), so they're told apart
// from the app's own (main.dart).
func ExtractDart(r io.ReaderAt, name string) (*Map, error) {
	m, err := extractELF(r, name, DartFile)
	if m != nil {
		m.Dart = true
	}
	return m, err
}

func extractELF(r io.ReaderAt, name string, fileName func(string) string) (*Map, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("%s: not an ELF file: %w", name, err)
	}
	defer f.Close()
	id := buildID(f)
	if id == "" {
		return nil, fmt.Errorf("%s: no build id (the NDK writes one; a library linked without --build-id can't be matched to its crashes)", name)
	}
	m := &Map{Version: Version, UUID: id, Arch: elfArch(f.Machine), Binary: name, Strings: []string{""}}
	// The code is the executable segments, at their own addresses.
	var code [][2]uint64
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 {
			code = append(code, [2]uint64{p.Vaddr, p.Vaddr + p.Memsz})
			m.TextSize = max(m.TextSize, p.Vaddr+p.Memsz)
		}
	}
	if len(code) == 0 {
		if text := f.Section(".text"); text != nil {
			code = append(code, [2]uint64{text.Addr, text.Addr + text.Size})
			m.TextSize = text.Addr + text.Size
		}
	}
	in := func(pc uint64) (uint64, bool) {
		for _, c := range code {
			if pc >= c[0] && pc < c[1] {
				return pc, true
			}
		}
		return 0, false
	}
	b := &builder{m: m, strings: map[string]uint64{"": 0}, fileName: fileName}
	if d, err := f.DWARF(); err == nil {
		if err := b.walk(d, in, name); err != nil {
			return nil, err
		}
	}
	sort.Slice(m.Functions, func(i, j int) bool { return m.Functions[i][0] < m.Functions[j][0] })
	b.elfSymbols(f, in)
	return b.finish(name)
}

// buildID is the library's GNU build-id note, as lower-case hex (as a tombstone prints it).
func buildID(f *elf.File) string {
	for _, s := range f.Sections {
		if s.Type != elf.SHT_NOTE {
			continue
		}
		data, err := s.Data()
		if err != nil {
			continue
		}
		for len(data) >= 12 {
			namesz := f.ByteOrder.Uint32(data[0:])
			descsz := f.ByteOrder.Uint32(data[4:])
			typ := f.ByteOrder.Uint32(data[8:])
			nameEnd := 12 + align4(namesz)
			descEnd := nameEnd + align4(descsz)
			if uint64(descEnd) > uint64(len(data)) {
				break
			}
			if typ == 3 /* NT_GNU_BUILD_ID */ && string(bytes.TrimRight(data[12:12+namesz], "\x00")) == "GNU" {
				return hex.EncodeToString(data[nameEnd : nameEnd+descsz])
			}
			data = data[descEnd:]
		}
	}
	return ""
}

func align4(n uint32) uint32 { return (n + 3) &^ 3 }

func elfArch(m elf.Machine) string {
	switch m {
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_ARM:
		return "armv7"
	case elf.EM_X86_64:
		return "x86_64"
	case elf.EM_386:
		return "x86"
	case elf.EM_RISCV:
		return "riscv64"
	}
	return strings.ToLower(strings.TrimPrefix(m.String(), "EM_"))
}

// elfSymbols fills the code DWARF doesn't cover with the library's symbol tables (.symtab, else
// the exported .dynsym): code linked in without debug information, or a library built without
// -g. ELF symbols carry their size, so each covers exactly its function.
func (b *builder) elfSymbols(f *elf.File, in func(uint64) (uint64, bool)) {
	syms, err := f.Symbols()
	if err != nil || len(syms) == 0 {
		syms, _ = f.DynamicSymbols()
	}
	dwarfFns := b.m.Functions
	var added [][3]uint64
	seen := map[uint64]bool{}
	for _, s := range syms {
		if elf.ST_TYPE(s.Info) != elf.STT_FUNC || s.Value == 0 || s.Size == 0 || s.Name == "" {
			continue
		}
		lo := s.Value
		if f.Machine == elf.EM_ARM {
			lo &^= 1 // Thumb code: the address's low bit is the mode
		}
		if _, ok := in(lo); !ok || seen[lo] {
			continue
		}
		seen[lo] = true
		hi := lo + s.Size
		k := sort.Search(len(dwarfFns), func(k int) bool { return dwarfFns[k][0] > lo })
		if k > 0 && lo < dwarfFns[k-1][1] {
			continue // DWARF knows it
		}
		if k < len(dwarfFns) && dwarfFns[k][0] < hi {
			hi = dwarfFns[k][0]
		}
		added = append(added, [3]uint64{lo, hi, b.str(s.Name)})
	}
	if len(added) > 0 {
		b.m.Functions = append(b.m.Functions, added...)
		sort.Slice(b.m.Functions, func(i, j int) bool { return b.m.Functions[i][0] < b.m.Functions[j][0] })
	}
}

// ErrStripped: a library without debug information or symbols (the copy Gradle packs into the
// app), which says nothing a crash doesn't.
var ErrStripped = errors.New("stripped: no debug information or symbols")

// Richness is how much a copy of a library knows: 2 with DWARF, 1 with a symbol table only, 0
// stripped. Gradle leaves several copies of each library in build/ (unstripped, stripped): the
// richest is uploaded.
func Richness(r io.ReaderAt) (id string, richness int) {
	f, err := elf.NewFile(r)
	if err != nil {
		return "", -1
	}
	defer f.Close()
	id = buildID(f)
	switch {
	case f.Section(".debug_info") != nil || f.Section(".zdebug_info") != nil:
		return id, 2
	case f.Section(".symtab") != nil:
		return id, 1
	}
	return id, 0
}

// DartFile names a Dart source as Dart does when it's the SDK's or a package's, and by its base
// name otherwise (the app's own, without the build machine's folders).
func DartFile(path string) string {
	p := filepath.ToSlash(path)
	cut := func(marker string) (string, bool) {
		if i := strings.Index(p, marker); i >= 0 {
			return p[i+len(marker):], true
		}
		if rest, ok := strings.CutPrefix(p, strings.TrimPrefix(marker, "/")); ok {
			return rest, true
		}
		return "", false
	}
	if rest, ok := cut("/third_party/dart/sdk/lib/"); ok {
		if internal, ok := strings.CutPrefix(rest, "_internal/vm/lib/"); ok {
			return "dart:_internal/" + internal
		}
		return "dart:" + rest
	}
	if rest, ok := cut("/sky_engine/lib/"); ok {
		return "dart:" + rest // dart:ui/painting.dart
	}
	if rest, ok := strings.CutPrefix(p, "lib/ui/"); ok {
		return "dart:ui/" + rest // the engine's, relative to its source tree
	}
	if rest, ok := cut("/.pub-cache/hosted/"); ok {
		// <host>/<package>-<version>/lib/<file>
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) == 4 && parts[2] == "lib" {
			pkg := parts[1]
			if i := strings.LastIndex(pkg, "-"); i > 0 {
				pkg = pkg[:i]
			}
			return "package:" + pkg + "/" + parts[3]
		}
	}
	if rest, ok := cut("/packages/flutter/lib/"); ok {
		return "package:flutter/" + rest
	}
	for _, pkg := range []string{"flutter_test", "flutter_localizations", "flutter_web_plugins"} {
		if rest, ok := cut("/packages/" + pkg + "/lib/"); ok {
			return "package:" + pkg + "/" + rest
		}
	}
	return filepath.Base(path)
}
