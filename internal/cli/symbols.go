package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/symbols"
	"go.lyrelog.com/cli/internal/ui"
)

// symbolFile is one map as the server lists it.
type symbolFile struct {
	UUID       string     `json:"uuid"`
	Arch       string     `json:"arch"`
	Binary     string     `json:"binary"`
	Bytes      int64      `json:"bytes"`
	UploadedAt time.Time  `json:"uploaded_at"`
	UploadedBy string     `json:"uploaded_by"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (a *App) symbolsCommand() *cobra.Command {
	list := func(cmd *cobra.Command, args []string) error {
		sp := a.spin("Loading symbols")
		p, err := a.project(cmd.Context(), a.projectRef(nil))
		var raw map[string]json.RawMessage
		if err == nil {
			raw, err = a.getMap(cmd.Context(), projectPath(p, "symbols"), nil)
		}
		sp.Stop()
		if err != nil {
			return err
		}
		if a.mode.JSON {
			return a.printRaw(rawList(raw, "symbols"))
		}
		files := decodeInto[symbolFile](raw["symbols"])
		if len(files) == 0 {
			a.println("No symbols or source maps uploaded to " + p.Name + " yet: its release crashes show as sent.")
			a.tip("Upload a build's:", "lyrelog symbols upload path/to/App.xcarchive")
			return nil
		}
		var used, quota int64
		_ = json.Unmarshal(raw["used_bytes"], &used)
		_ = json.Unmarshal(raw["quota_bytes"], &quota)
		th := a.theme
		t := ui.Table{Columns: []ui.Column{{Header: "UUID", Keep: true}, {Header: "Binary", Keep: true}, {Header: "Arch"}, {Header: "Size"}, {Header: "Uploaded", Drop: 1}, {Header: "By", Drop: 2}}}
		for _, f := range files {
			t.Add(th.Dim(f.UUID), th.Bold(f.Binary), f.Arch, mib(f.Bytes), ui.Ago(f.UploadedAt, a.Now()), f.UploadedBy)
		}
		t.Render(a.out, th, a.Width)
		a.println(th.Dim(fmt.Sprintf("%s of %s used by the organization", mib(used), mib(quota))))
		return nil
	}
	cmd := &cobra.Command{
		Use:     "symbols",
		Aliases: []string{"dsym", "dsyms"},
		Short:   "Debug symbols that make crashes readable (dSYMs, R8 mappings, native libraries, Flutter's Dart symbols): upload, list, delete",
		Long: `Debug symbols turn a crash into the functions, files and lines it happened in:
- iOS and Mac: a dSYM turns offsets (Tilefall +0x1a2b3c) into functions, inlined ones included.
  The CLI reads it here and uploads a compact map (function and file names without their
  folders, lines), with Swift names demangled by Xcode's tools.
- Android: a release's R8 mapping (mapping.txt) turns renamed classes and methods back. Each
  frame names its mapping (r8-map-id-...), so uploading it once per build is all.
- Android native code (C, C++: the NDK, a game engine): each library's debug information turns
  its crashes' addresses into functions, files and lines, by the library's build id. Gradle
  keeps the unstripped libraries under build/; point at app/build and both are found.
- Flutter releases built with --split-debug-info (and --obfuscate): the folder it wrote
  (app.android-arm64.symbols, app.ios-arm64.symbols) turns Dart frames printed by address into
  functions, files and lines. Give the --save-obfuscation-map file too, and an error's
  obfuscated type reads as its real name.
Upload each build's once, from Xcode, Gradle or CI; crashes of builds without them stay as sent.`,
		Example: `  lyrelog symbols upload ~/Library/Developer/Xcode/Archives/2026-11-25/Tilefall.xcarchive
  lyrelog symbols upload app/build       # an Android app: its R8 mapping and native libraries
  lyrelog symbols upload build/debug-info build/obfuscation.map.json   # a Flutter release
  lyrelog symbols upload "$DWARF_DSYM_FOLDER_PATH"
  lyrelog symbols
  lyrelog symbols delete A3F11B8C-0F44-3ABF-A3AA-25AC3AF39B8F`,
		Args: cobra.NoArgs,
		RunE: list,
	}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List the project's uploaded symbols", Args: cobra.NoArgs, RunE: list})

	upload := &cobra.Command{
		Use:   "upload <dSYM, .xcarchive, mapping.txt, .so, Flutter .symbols or folder>...",
		Short: "Upload the dSYMs, R8 mappings and native libraries' symbols found at each path (only builds the project doesn't have yet)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var files, mappings, obfuscations, dartApple []string
			var natives []nativeLib
			for _, path := range args {
				obfuscations = append(obfuscations, dartObfuscationMaps(path)...)
				dartApple = append(dartApple, dartMachOFiles(path)...)
				found, maps, libs, err := symbolFiles(path)
				if err != nil {
					return err
				}
				files = append(files, found...)
				mappings = append(mappings, maps...)
				natives = append(natives, libs...)
			}
			if len(files) == 0 && len(mappings) == 0 && len(natives) == 0 && len(dartApple) == 0 {
				return usagef("No dSYM, R8 mapping or native library found at %s", strings.Join(args, ", ")).hint("Point at a .dSYM, an .xcarchive, the folder Xcode writes them to ($DWARF_DSYM_FOLDER_PATH), an Android app's build folder (app/build: its R8 mapping and native libraries), or the folder a Flutter build's --split-debug-info wrote.")
			}
			// What to upload: each binary's symbol map, each mapping as it is.
			type upload struct {
				id, binary, arch string
				data             func() ([]byte, error)
			}
			var uploads []upload
			if len(files) > 0 || len(natives) > 0 || len(dartApple) > 0 {
				sp := a.spin("Reading the debug symbols")
				var maps []*symbols.Map
				// A dSYM that can't be read (an empty framework's, a stray file) is passed over and
				// named: one shouldn't keep the rest of an archive from uploading.
				var unread []string
				for _, f := range files {
					m, err := symbols.Extract(f)
					if err != nil {
						unread = append(unread, err.Error())
						continue
					}
					maps = append(maps, m...)
				}
				native, stripped, err := richestNative(natives)
				if err != nil {
					sp.Stop()
					return err
				}
				maps = append(maps, native...)
				for _, path := range dartApple {
					m, err := extractDartMachO(path)
					if err != nil {
						sp.Stop()
						return err
					}
					maps = append(maps, m)
				}
				// A Flutter build's obfuscation map goes with its Dart symbols: the server renames
				// an error's type back with it. With more than one, which goes with which can't be told.
				if names, err := dartNames(obfuscations); err != nil {
					sp.Stop()
					return err
				} else if names != nil {
					for _, m := range maps {
						if m.Dart {
							m.DartObfuscation = names
						}
					}
				}
				demangled := symbols.Demangle(ctx, maps)
				sp.Stop()
				if demangled != nil && !a.mode.JSON {
					a.println(a.theme.Dim("Names stay mangled (" + demangled.Error() + "): run this where Xcode's tools or c++filt are installed for readable ones."))
				}
				if len(unread) > 0 && len(maps) == 0 && len(mappings) == 0 {
					return fmt.Errorf("%s", strings.Join(unread, "; "))
				}
				if len(unread) > 0 && !a.mode.JSON {
					a.println(a.theme.Dim("Not read (nothing uploaded for them): " + strings.Join(unread, "; ")))
				}
				if len(stripped) > 0 && !a.mode.JSON {
					a.println(a.theme.Dim("Only stripped copies of " + strings.Join(stripped, ", ") + " (nothing to upload): point at the build folder, where Gradle keeps them unstripped."))
				}
				for _, m := range maps {
					uploads = append(uploads, upload{id: m.UUID, binary: m.Binary, arch: m.Arch, data: func() ([]byte, error) { return gzipJSON(m) }})
				}
			}
			for _, path := range mappings {
				text, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				id := r8MapID(text)
				if id == "" {
					return usagef("%s has no pg_map_id line", path).hint("R8 writes it from Android Gradle Plugin 8: build the release again with a newer AGP.")
				}
				uploads = append(uploads, upload{id: id, binary: "mapping.txt", arch: "jvm", data: func() ([]byte, error) { return gzipBytes(text) }})
			}
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			ids := make([]string, len(uploads))
			for i, u := range uploads {
				ids[i] = u.id
			}
			// The server answers about 500 builds at a time.
			need := map[string]bool{}
			for start := 0; start < len(ids); start += 500 {
				var missing struct {
					Missing []string `json:"missing"`
				}
				if err := a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: projectPath(p, "symbols/missing"), Body: map[string]any{"uuids": ids[start:min(start+500, len(ids))]}}, &missing); err != nil {
					return err
				}
				for _, u := range missing.Missing {
					need[u] = true
				}
			}
			type result struct {
				UUID   string `json:"uuid"`
				Binary string `json:"binary"`
				Arch   string `json:"arch"`
				Status string `json:"status"` // uploaded, already_there
				Bytes  int    `json:"bytes,omitempty"`
			}
			var results []result
			for _, u := range uploads {
				r := result{UUID: u.id, Binary: u.binary, Arch: u.arch, Status: "already_there"}
				if need[u.id] {
					data, err := u.data()
					if err != nil {
						return err
					}
					sp := a.spin("Uploading " + u.binary + " (" + u.arch + ", " + mib(int64(len(data))) + ")")
					err = a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: projectPath(p, "symbols"), Body: data, ContentType: "application/gzip"}, nil)
					sp.Stop()
					if err != nil {
						return err
					}
					r.Status, r.Bytes = "uploaded", len(data)
					need[u.id] = false // a fat dSYM lists one UUID once
				}
				results = append(results, r)
			}
			if a.mode.JSON {
				return a.printJSON(results)
			}
			for _, r := range results {
				what := a.theme.Bold(r.Binary) + " " + r.Arch + " " + a.theme.Dim(r.UUID)
				if r.Status == "uploaded" {
					a.done("Uploaded " + what)
				} else {
					a.println(a.theme.Dim("Already there: ") + what)
				}
			}
			return nil
		},
	}

	var yes bool
	del := &cobra.Command{
		Use:   "delete <uuid>",
		Short: "Delete a build's symbols (owners); its crashes show as offsets again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// As given: the server finds an id in either case (Apple UUIDs are kept upper-case,
			// Android build ids and web debug ids lower-case).
			uuid := strings.TrimSpace(args[0])
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			if err := a.confirm(ctx, "Delete the symbols of build "+uuid+" from "+p.Name+"? Its crashes show as offsets again.", yes); err != nil {
				return err
			}
			if err := a.client.JSON(ctx, api.Request{Method: http.MethodDelete, Path: projectPath(p, "symbols/"+api.PathEscape(uuid))}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"deleted": true, "uuid": uuid})
			}
			a.done("Deleted the symbols of " + uuid)
			return nil
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask to confirm")
	cmd.AddCommand(upload, del)
	return cmd
}

// symbolFiles finds what to upload at path: DWARF files (a .dSYM's Contents/Resources/DWARF/*,
// every .dSYM in a folder or an .xcarchive, or a Mach-O file given directly), R8 mappings (a
// mapping.txt, or each build/outputs/mapping/*/mapping.txt in a folder) and Android native
// libraries (.so files, given or in a folder, and native-debug-symbols.zip).
func symbolFiles(path string) (dwarf, mappings []string, natives []nativeLib, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, nil, err
	}
	if !info.IsDir() {
		switch {
		case isR8Mapping(path):
			return nil, []string{path}, nil, nil
		case strings.HasSuffix(path, ".zip"):
			libs, err := nativeInZip(path)
			return nil, nil, libs, err
		case symbols.IsELF(path) && isDartSymbolsName(path):
			return nil, nil, []nativeLib{dartOnDisk(path)}, nil
		case isDartSymbolsName(path):
			return nil, nil, nil, nil // iOS's, a Mach-O: dartMachOFiles
		case len(dartObfuscationMaps(path)) > 0:
			return nil, nil, nil, nil // a Flutter obfuscation map: dartObfuscationMaps
		case symbols.IsELF(path):
			return nil, nil, []nativeLib{nativeOnDisk(path)}, nil
		}
		return []string{path}, nil, nil, nil
	}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".gradle" || d.Name() == ".cxx") {
			return filepath.SkipDir
		}
		if d.IsDir() && strings.HasSuffix(d.Name(), ".dSYM") {
			entries, err := os.ReadDir(filepath.Join(p, "Contents", "Resources", "DWARF"))
			if err == nil {
				for _, e := range entries {
					if !e.IsDir() {
						dwarf = append(dwarf, filepath.Join(p, "Contents", "Resources", "DWARF", e.Name()))
					}
				}
			}
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case d.Name() == "mapping.txt" && strings.Contains(filepath.ToSlash(p), "/outputs/mapping/") && isR8Mapping(p):
			mappings = append(mappings, p)
		case d.Name() == "native-debug-symbols.zip":
			libs, err := nativeInZip(p)
			if err != nil {
				return err
			}
			natives = append(natives, libs...)
		case isNativeLibName(d.Name()) && d.Type().IsRegular() && symbols.IsELF(p):
			natives = append(natives, nativeOnDisk(p))
		case isDartSymbolsName(d.Name()) && d.Type().IsRegular() && symbols.IsELF(p):
			natives = append(natives, dartOnDisk(p))
		}
		return nil
	})
	return dwarf, mappings, natives, err
}

// isR8Mapping: a text file that starts as R8 writes its mappings.
func isR8Mapping(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := f.Read(head)
	return strings.HasPrefix(string(head[:n]), "# compiler: R8") || strings.Contains(string(head[:n]), "com.android.tools.r8.mapping")
}

// r8MapID is a mapping's `# pg_map_id:`, the id every frame of its build names.
func r8MapID(text []byte) string {
	for _, line := range strings.SplitN(string(text[:min(len(text), 4096)]), "\n", 32) {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "# pg_map_id: "); ok {
			return id
		}
	}
	return ""
}

func gzipBytes(data []byte) ([]byte, error) {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func gzipJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func mib(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
