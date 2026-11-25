// Package symbols turns an app's debug symbols (an Apple dSYM) into the symbol map Lyrelog's
// server reads to make crashes readable: which function, file and line an offset into the app's
// code is, inlined functions included. The map is written here, on the developer's Mac or CI,
// because only there can Swift names be demangled (xcrun swift-demangle); the server only looks
// offsets up. The format is specified in the server's docs/symbolication.md ("The symbol map"),
// and both sides test against the same golden file.
package symbols

import (
	"debug/dwarf"
	"debug/macho"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// Version is the map's format version; the server refuses one it doesn't know.
const Version = 1

// Map is one binary's symbols, for one architecture. Offsets are from the start of the
// binary's __TEXT segment, as MetricKit reports a frame (offsetIntoBinaryTextSegment).
type Map struct {
	Version  int    `json:"lyrelog_symbols"`
	UUID     string `json:"uuid"` // as MetricKit writes it: upper-case, with dashes
	Arch     string `json:"arch"`
	Binary   string `json:"binary"`
	TextAddr uint64 `json:"text_vmaddr"`
	TextSize uint64 `json:"text_vmsize"`
	// Strings holds every name and file once; the tables below point into it. Strings[0] is "".
	Strings []string `json:"strings"`
	// Functions: [lo, hi, name], sorted by lo; hi is past the end.
	Functions [][3]uint64 `json:"functions"`
	// Inlines: [lo, hi, depth, name, call_file, call_line], sorted by lo then depth. Depth 1 is
	// inlined straight into its function; the call is where it was inlined, in its caller.
	Inlines [][6]uint64 `json:"inlines"`
	// Lines: [lo, file, line], sorted by lo; a row holds until the next one. Line 0: unknown.
	Lines [][3]uint64 `json:"lines"`
	// DartObfuscation: a Flutter build's obfuscation map (--save-obfuscation-map), as Dart
	// writes it: [original, obfuscated, ...]. The server renames an error's type back with it.
	DartObfuscation []string `json:"dart_obfuscation,omitempty"`
	// Dart: read from a Flutter build's Dart debug information (not uploaded).
	Dart bool `json:"-"`
}

// Extract reads every architecture of a Mach-O file with DWARF (a dSYM's
// Contents/Resources/DWARF/<binary>, or a binary built with its debug info).
func Extract(path string) ([]*Map, error) {
	if fat, err := macho.OpenFat(path); err == nil {
		defer fat.Close()
		var out []*Map
		for _, a := range fat.Arches {
			m, err := extract(a.File, filepath.Base(path))
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	f, err := macho.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: not a Mach-O file: %w", path, err)
	}
	defer f.Close()
	m, err := extract(f, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	return []*Map{m}, nil
}

const lcUUID = 0x1b

func uuidOf(f *macho.File) string {
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) >= 24 && f.ByteOrder.Uint32(raw) == lcUUID {
			u := raw[8:24]
			return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]))
		}
	}
	return ""
}

func archOf(f *macho.File) string {
	switch {
	case f.Cpu == macho.CpuArm64 && f.SubCpu&0xff == 2:
		return "arm64e"
	case f.Cpu == macho.CpuArm64:
		return "arm64"
	case f.Cpu == macho.CpuAmd64:
		return "x86_64"
	case f.Cpu == macho.CpuArm:
		return "arm"
	}
	return strings.ToLower(f.Cpu.String())
}

// ErrNoDebugInfo: the file has no DWARF (a binary stripped, or a dSYM of nothing).
var ErrNoDebugInfo = errors.New("no debug information")

type builder struct {
	m       *Map
	strings map[string]uint64
	// fileName is how a source file is named in the map: its base name (no build machine's
	// folders), or Dart's own names for the Dart SDK's and packages' files (DartFile).
	fileName func(string) string
}

func (b *builder) file(path string) string {
	if b.fileName != nil {
		return b.fileName(path)
	}
	return BaseName(path)
}

func (b *builder) str(s string) uint64 {
	if i, ok := b.strings[s]; ok {
		return i
	}
	i := uint64(len(b.m.Strings))
	b.m.Strings = append(b.m.Strings, s)
	b.strings[s] = i
	return i
}

// ExtractDartMachO reads an iOS or macOS Flutter build's Dart debug information
// (app.ios-arm64.symbols, a Mach-O dSYM companion file with App.framework's UUID), with Dart's
// names for the SDK's and packages' files (DartFile).
func ExtractDartMachO(r io.ReaderAt, binaryName string) (*Map, error) {
	f, err := macho.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("%s: not a Mach-O file: %w", binaryName, err)
	}
	defer f.Close()
	m, err := extractWith(f, binaryName, DartFile)
	if m != nil {
		m.Dart = true
	}
	return m, err
}

func extract(f *macho.File, binaryName string) (*Map, error) {
	return extractWith(f, binaryName, nil)
}

func extractWith(f *macho.File, binaryName string, fileName func(string) string) (*Map, error) {
	text := f.Segment("__TEXT")
	if text == nil {
		return nil, fmt.Errorf("%s: no __TEXT segment", binaryName)
	}
	m := &Map{Version: Version, UUID: uuidOf(f), Arch: archOf(f), Binary: binaryName, TextAddr: text.Addr, TextSize: text.Memsz, Strings: []string{""}}
	if m.UUID == "" {
		return nil, fmt.Errorf("%s: no UUID", binaryName)
	}
	d, err := f.DWARF()
	if err != nil {
		return nil, fmt.Errorf("%s (%s): %w", binaryName, m.Arch, ErrNoDebugInfo)
	}
	b := &builder{m: m, strings: map[string]uint64{"": 0}, fileName: fileName}
	in := func(pc uint64) (uint64, bool) {
		if pc < text.Addr || pc >= text.Addr+text.Memsz {
			return 0, false
		}
		return pc - text.Addr, true
	}

	if err := b.walk(d, in, binaryName); err != nil {
		return nil, err
	}
	sort.Slice(m.Functions, func(i, j int) bool { return m.Functions[i][0] < m.Functions[j][0] })
	b.symbolTable(f, text.Addr)
	return b.finish(binaryName)
}

// walk reads a binary's DWARF into the map: functions with their ranges, inlined calls with
// their call sites, and the line table, every address passed through in (an offset into the
// binary's code, or false when it isn't code of this binary).
func (b *builder) walk(d *dwarf.Data, in func(uint64) (uint64, bool), binaryName string) error {
	m := b.m
	names := &nameCache{d: d, byOffset: map[dwarf.Offset]string{}}
	r := d.Reader()
	var files []*dwarf.LineFile
	// The open entries above the current one: whether each is a function with code, and how deep
	// inside its function an inlined call sits.
	type open struct {
		inFunction bool
		inline     int
	}
	var stack []open
	top := func() open {
		if len(stack) == 0 {
			return open{}
		}
		return stack[len(stack)-1]
	}
	for {
		e, err := r.Next()
		if err != nil {
			return fmt.Errorf("%s: reading DWARF: %w", binaryName, err)
		}
		if e == nil {
			break
		}
		if e.Tag == 0 {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		state := top()
		switch e.Tag {
		case dwarf.TagCompileUnit:
			files = nil
			if lr, err := d.LineReader(e); err == nil && lr != nil {
				files = lr.Files()
				b.lines(lr, in)
			}
			state = open{}
		case dwarf.TagSubprogram:
			ranges, _ := d.Ranges(e)
			had := false
			for _, rg := range ranges {
				lo, ok1 := in(rg[0])
				if !ok1 || rg[1] <= rg[0] {
					continue
				}
				m.Functions = append(m.Functions, [3]uint64{lo, lo + (rg[1] - rg[0]), b.str(names.of(e))})
				had = true
			}
			state = open{inFunction: had}
		case dwarf.TagInlinedSubroutine:
			if state.inFunction {
				depth := state.inline + 1
				callFile := ""
				if i, ok := e.Val(dwarf.AttrCallFile).(int64); ok && i >= 0 && int(i) < len(files) && files[i] != nil {
					callFile = b.file(files[i].Name)
				}
				callLine, _ := e.Val(dwarf.AttrCallLine).(int64)
				ranges, _ := d.Ranges(e)
				for _, rg := range ranges {
					lo, ok := in(rg[0])
					if !ok || rg[1] <= rg[0] {
						continue
					}
					m.Inlines = append(m.Inlines, [6]uint64{lo, lo + (rg[1] - rg[0]), uint64(depth), b.str(names.of(e)), b.str(callFile), uint64(max(callLine, 0))})
				}
				state.inline = depth
			}
		}
		if e.Children {
			stack = append(stack, state)
		}
	}
	return nil
}

// finish sorts the tables and drops repeated line rows; an empty map is no debug information.
func (b *builder) finish(binaryName string) (*Map, error) {
	m := b.m
	sort.Slice(m.Functions, func(i, j int) bool { return m.Functions[i][0] < m.Functions[j][0] })
	if len(m.Functions) == 0 {
		return nil, fmt.Errorf("%s (%s): %w", binaryName, m.Arch, ErrNoDebugInfo)
	}
	sort.SliceStable(m.Inlines, func(i, j int) bool {
		if m.Inlines[i][0] != m.Inlines[j][0] {
			return m.Inlines[i][0] < m.Inlines[j][0]
		}
		return m.Inlines[i][2] < m.Inlines[j][2]
	})
	sort.SliceStable(m.Lines, func(i, j int) bool { return m.Lines[i][0] < m.Lines[j][0] })
	m.Lines = squash(m.Lines)
	return m, nil
}

// symbolTable fills the code DWARF doesn't cover with the binary's symbol table, as atos does:
// libraries linked in already compiled (gRPC, abseil, an SDK shipped as a binary) have names
// there but no debug information, so their frames get a function and no file or line. A symbol
// runs to the next one, or to the end of __text, and never into a function DWARF knows.
func (b *builder) symbolTable(f *macho.File, textAddr uint64) {
	if f.Symtab == nil {
		return
	}
	sect := f.Section("__text")
	if sect == nil {
		return
	}
	index := uint8(0)
	for i, s := range f.Sections {
		if s == sect {
			index = uint8(i + 1)
		}
	}
	const stab = 0xe0
	type sym struct {
		addr uint64
		name string
	}
	var syms []sym
	for _, s := range f.Symtab.Syms {
		if s.Type&stab != 0 || s.Sect != index || s.Name == "" || s.Value < sect.Addr || s.Value >= sect.Addr+sect.Size {
			continue
		}
		name := s.Name
		if strings.HasPrefix(name, "_") {
			name = name[1:] // Mach-O's leading underscore
		}
		if strings.HasPrefix(name, "ltmp") || strings.HasPrefix(name, "l_") {
			continue // assembler-local labels
		}
		syms = append(syms, sym{s.Value - textAddr, name})
	}
	sort.SliceStable(syms, func(i, j int) bool { return syms[i].addr < syms[j].addr })
	dwarfFns := b.m.Functions
	end := sect.Addr + sect.Size - textAddr
	var added [][3]uint64
	for i, s := range syms {
		if i > 0 && syms[i-1].addr == s.addr {
			continue // two names at one address: the first
		}
		hi := end
		for j := i + 1; j < len(syms); j++ {
			if syms[j].addr > s.addr {
				hi = syms[j].addr
				break
			}
		}
		// Not where DWARF already says, and not into the next function it knows.
		k := sort.Search(len(dwarfFns), func(k int) bool { return dwarfFns[k][0] > s.addr })
		if k > 0 && s.addr < dwarfFns[k-1][1] {
			continue
		}
		if k < len(dwarfFns) && dwarfFns[k][0] < hi {
			hi = dwarfFns[k][0]
		}
		if hi > s.addr {
			added = append(added, [3]uint64{s.addr, hi, b.str(s.name)})
		}
	}
	if len(added) > 0 {
		b.m.Functions = append(b.m.Functions, added...)
		sort.Slice(b.m.Functions, func(i, j int) bool { return b.m.Functions[i][0] < b.m.Functions[j][0] })
	}
}

// lines adds a unit's line table: a row where the file or line changes, and a row of line 0
// where a sequence ends (the gap up to the next one belongs to no line).
func (b *builder) lines(lr *dwarf.LineReader, in func(uint64) (uint64, bool)) {
	var le dwarf.LineEntry
	for lr.Next(&le) == nil {
		lo, ok := in(le.Address)
		if !ok {
			continue
		}
		if le.EndSequence {
			b.m.Lines = append(b.m.Lines, [3]uint64{lo, 0, 0})
			continue
		}
		file := ""
		if le.File != nil {
			file = b.file(le.File.Name)
		}
		b.m.Lines = append(b.m.Lines, [3]uint64{lo, b.str(file), uint64(max(le.Line, 0))})
	}
}

// squash keeps one row per address (the last, as the line program ends there) and drops a row
// that says what the one before it said.
func squash(rows [][3]uint64) [][3]uint64 {
	out := rows[:0]
	for _, r := range rows {
		if n := len(out); n > 0 && out[n-1][0] == r[0] {
			out[n-1] = r
			continue
		}
		if n := len(out); n > 0 && out[n-1][1] == r[1] && out[n-1][2] == r[2] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// nameCache finds the name a function is known by: its linkage (mangled) name, else its name,
// following an inlined call's or a definition's origin.
type nameCache struct {
	d        *dwarf.Data
	byOffset map[dwarf.Offset]string
}

func (n *nameCache) of(e *dwarf.Entry) string {
	if s, ok := e.Val(dwarf.AttrLinkageName).(string); ok && s != "" {
		return s
	}
	if s, ok := e.Val(dwarf.AttrName).(string); ok && s != "" {
		return s
	}
	for _, attr := range []dwarf.Attr{dwarf.AttrAbstractOrigin, dwarf.AttrSpecification} {
		if off, ok := e.Val(attr).(dwarf.Offset); ok {
			return n.at(off)
		}
	}
	return ""
}

func (n *nameCache) at(off dwarf.Offset) string {
	if s, ok := n.byOffset[off]; ok {
		return s
	}
	n.byOffset[off] = "" // a cycle answers nothing
	r := n.d.Reader()
	r.Seek(off)
	e, err := r.Next()
	s := ""
	if err == nil && e != nil {
		s = n.of(e)
	}
	n.byOffset[off] = s
	return s
}
