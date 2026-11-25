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
			a.println("No symbols uploaded to " + p.Name + " yet: its iOS and Mac crashes show as offsets.")
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
		Short:   "Debug symbols that make iOS and Mac crashes readable: upload, list, delete",
		Long: `Debug symbols turn a crash's offsets (Tilefall +0x1a2b3c) into the function, file and line
it happened in, inlined functions included. Upload each build's dSYMs once, from Xcode or CI;
crashes of builds without them keep their offsets.

The CLI reads the dSYM here and uploads a compact map of it (functions, file names without their
folders, lines), with Swift names demangled by Xcode's tools.`,
		Example: `  lyrelog symbols upload ~/Library/Developer/Xcode/Archives/2026-11-25/Tilefall.xcarchive
  lyrelog symbols upload "$DWARF_DSYM_FOLDER_PATH"
  lyrelog symbols
  lyrelog symbols delete A3F11B8C-0F44-3ABF-A3AA-25AC3AF39B8F`,
		Args: cobra.NoArgs,
		RunE: list,
	}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List the project's uploaded symbols", Args: cobra.NoArgs, RunE: list})

	upload := &cobra.Command{
		Use:   "upload <dSYM, .xcarchive or folder>...",
		Short: "Upload the dSYMs found at each path (only builds the project doesn't have yet)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var files []string
			for _, path := range args {
				found, err := dwarfFiles(path)
				if err != nil {
					return err
				}
				files = append(files, found...)
			}
			if len(files) == 0 {
				return usagef("No dSYM found at %s", strings.Join(args, ", ")).hint("Point at a .dSYM, an .xcarchive or the folder Xcode writes them to ($DWARF_DSYM_FOLDER_PATH).")
			}
			sp := a.spin("Reading the dSYMs")
			var maps []*symbols.Map
			for _, f := range files {
				m, err := symbols.Extract(f)
				if err != nil {
					sp.Stop()
					return err
				}
				maps = append(maps, m...)
			}
			demangled := symbols.Demangle(ctx, maps)
			sp.Stop()
			if demangled != nil && !a.mode.JSON {
				a.println(a.theme.Dim("Swift names stay mangled (" + demangled.Error() + "): run this on a Mac with Xcode for readable ones."))
			}
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			uuids := make([]string, len(maps))
			for i, m := range maps {
				uuids[i] = m.UUID
			}
			var missing struct {
				Missing []string `json:"missing"`
			}
			if err := a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: projectPath(p, "symbols/missing"), Body: map[string]any{"uuids": uuids}}, &missing); err != nil {
				return err
			}
			need := map[string]bool{}
			for _, u := range missing.Missing {
				need[u] = true
			}
			type result struct {
				UUID   string `json:"uuid"`
				Binary string `json:"binary"`
				Arch   string `json:"arch"`
				Status string `json:"status"` // uploaded, already_there
				Bytes  int    `json:"bytes,omitempty"`
			}
			var results []result
			for _, m := range maps {
				r := result{UUID: m.UUID, Binary: m.Binary, Arch: m.Arch, Status: "already_there"}
				if need[m.UUID] {
					data, err := gzipJSON(m)
					if err != nil {
						return err
					}
					sp := a.spin("Uploading " + m.Binary + " (" + m.Arch + ", " + mib(int64(len(data))) + ")")
					err = a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: projectPath(p, "symbols"), Body: data, ContentType: "application/gzip"}, nil)
					sp.Stop()
					if err != nil {
						return err
					}
					r.Status, r.Bytes = "uploaded", len(data)
					need[m.UUID] = false // a fat dSYM lists one UUID once
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
			uuid := strings.ToUpper(args[0])
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

// dwarfFiles finds the DWARF files at path: a .dSYM's Contents/Resources/DWARF/*, every .dSYM in
// a folder or an .xcarchive (dSYMs/), or a Mach-O file given directly.
func dwarfFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var out []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasSuffix(d.Name(), ".dSYM") {
			entries, err := os.ReadDir(filepath.Join(p, "Contents", "Resources", "DWARF"))
			if err == nil {
				for _, e := range entries {
					if !e.IsDir() {
						out = append(out, filepath.Join(p, "Contents", "Resources", "DWARF", e.Name()))
					}
				}
			}
			return filepath.SkipDir
		}
		return nil
	})
	return out, err
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
