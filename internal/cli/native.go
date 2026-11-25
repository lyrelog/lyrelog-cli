package cli

import (
	"archive/zip"
	"bytes"
	"debug/macho"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.lyrelog.com/cli/internal/symbols"
)

// An Android app's native libraries (.so), found in a folder (Gradle leaves several copies of
// each under build/: unstripped ones under intermediates, the stripped ones it packs) or in the
// native-debug-symbols.zip Gradle writes for Play. Each library is uploaded once, from its
// richest copy.

// nativeLib is one copy of a library: on disk, or inside a zip.
type nativeLib struct {
	label string // where it is, for messages
	name  string // as a crash names it: libgame.so
	open  func() (io.ReaderAt, func(), error)
	// dart: a Flutter build's Dart debug information (--split-debug-info), read with Dart's
	// names for the SDK's and packages' files.
	dart bool
}

// isDartSymbolsName: what flutter build --split-debug-info writes, app.android-arm64.symbols.
func isDartSymbolsName(name string) bool {
	return strings.HasSuffix(name, ".symbols")
}

// dartBinary is the binary a Flutter build's symbols are for, from their file name
// (app.android-arm64.symbols: libapp.so; app.ios-arm64.symbols: App).
func dartBinary(name string) string {
	switch {
	case strings.Contains(name, ".android-"):
		return "libapp.so"
	case strings.Contains(name, ".ios-"), strings.Contains(name, ".darwin-"), strings.Contains(name, ".macos-"):
		return "App"
	}
	return "app"
}

func isNativeLibName(name string) bool {
	return strings.HasSuffix(name, ".so") || strings.HasSuffix(name, ".so.dbg") || strings.HasSuffix(name, ".so.sym")
}

func libName(name string) string {
	name = filepath.Base(name)
	for _, suffix := range []string{".dbg", ".sym"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

func nativeOnDisk(path string) nativeLib {
	return nativeLib{label: path, name: libName(path), open: func() (io.ReaderAt, func(), error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return f, func() { f.Close() }, nil
	}}
}

// nativeInZip lists the libraries in a zip (native-debug-symbols.zip: <abi>/libgame.so.dbg).
func nativeInZip(path string) ([]nativeLib, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	var libs []nativeLib
	for _, e := range z.File {
		if e.FileInfo().IsDir() || !isNativeLibName(e.Name) {
			continue
		}
		entry := e.Name
		libs = append(libs, nativeLib{label: path + "!" + entry, name: libName(entry), open: func() (io.ReaderAt, func(), error) {
			z, err := zip.OpenReader(path)
			if err != nil {
				return nil, nil, err
			}
			defer z.Close()
			for _, e := range z.File {
				if e.Name == entry {
					r, err := e.Open()
					if err != nil {
						return nil, nil, err
					}
					defer r.Close()
					data, err := io.ReadAll(r)
					if err != nil {
						return nil, nil, err
					}
					return bytes.NewReader(data), func() {}, nil
				}
			}
			return nil, nil, fmt.Errorf("%s: gone from the zip", entry)
		}})
	}
	return libs, nil
}

// richestNative reads every copy and keeps, for each build id, the one that knows most. stripped
// are the libraries found only stripped (nothing to upload for them).
func richestNative(libs []nativeLib) (maps []*symbols.Map, stripped []string, err error) {
	type best struct {
		lib      nativeLib
		richness int
	}
	byID := map[string]best{}
	var order []string
	for _, lib := range libs {
		r, done, err := lib.open()
		if err != nil {
			return nil, nil, err
		}
		id, rich := symbols.Richness(r)
		done()
		if id == "" || rich < 0 {
			continue // not ELF, or no build id: no crash can name it
		}
		b, seen := byID[id]
		if !seen {
			order = append(order, id)
		}
		if !seen || rich > b.richness {
			byID[id] = best{lib, rich}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return byID[order[i]].lib.name < byID[order[j]].lib.name })
	for _, id := range order {
		b := byID[id]
		if b.richness == 0 {
			stripped = append(stripped, b.lib.name)
			continue
		}
		r, done, err := b.lib.open()
		if err != nil {
			return nil, nil, err
		}
		extract := symbols.ExtractELF
		if b.lib.dart {
			extract = symbols.ExtractDart
		}
		m, err := extract(r, b.lib.name)
		done()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", b.lib.label, err)
		}
		maps = append(maps, m)
	}
	return maps, stripped, nil
}

func dartOnDisk(path string) nativeLib {
	lib := nativeOnDisk(path)
	lib.name, lib.dart = dartBinary(filepath.Base(path)), true
	return lib
}

// dartObfuscationMaps are the Flutter obfuscation maps (--save-obfuscation-map: a JSON list of
// original and obfuscated names) at path: the file itself, or the .json files in the folder.
func dartObfuscationMaps(path string) []string {
	var found []string
	check := func(p string) {
		if !strings.HasSuffix(p, ".json") {
			return
		}
		f, err := os.Open(p)
		if err != nil {
			return
		}
		defer f.Close()
		head := make([]byte, 2)
		if n, _ := io.ReadFull(f, head); n == 2 && string(head) == `["` {
			found = append(found, p)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		check(path)
		return found
	}
	entries, _ := os.ReadDir(path)
	for _, e := range entries {
		if !e.IsDir() {
			check(filepath.Join(path, e.Name()))
		}
	}
	return found
}

// dartNames reads the one obfuscation map given (nil without one).
func dartNames(paths []string) ([]string, error) {
	switch len(paths) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, usagef("%d obfuscation maps given (%s): upload each build's with its symbols, one build at a time", len(paths), strings.Join(paths, ", "))
	}
	text, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, err
	}
	var names []string
	if json.Unmarshal(text, &names) != nil || len(names)%2 != 0 {
		return nil, usagef("%s isn't a Flutter obfuscation map (a JSON list of original and obfuscated names)", paths[0])
	}
	return names, nil
}

// dartMachOFiles are an iOS or macOS Flutter build's Dart symbols at path (app.ios-arm64.symbols:
// Mach-O, App.framework's UUID).
func dartMachOFiles(path string) []string {
	var found []string
	check := func(p string) {
		if isDartSymbolsName(p) && !symbols.IsELF(p) {
			if f, err := macho.Open(p); err == nil {
				f.Close()
				found = append(found, p)
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		check(path)
		return found
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			check(p)
		}
		return nil
	})
	return found
}

func extractDartMachO(path string) (*symbols.Map, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, err := symbols.ExtractDartMachO(f, dartBinary(filepath.Base(path)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}
