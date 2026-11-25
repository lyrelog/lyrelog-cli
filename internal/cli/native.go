package cli

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
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
		m, err := symbols.ExtractELF(r, b.lib.name)
		done()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", b.lib.label, err)
		}
		maps = append(maps, m)
	}
	return maps, stripped, nil
}
