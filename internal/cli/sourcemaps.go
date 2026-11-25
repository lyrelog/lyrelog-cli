package cli

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/sourcemaps"
)

func (a *App) sourcemapsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sourcemaps",
		Aliases: []string{"sourcemap"},
		Short:   "Source maps that make a website's errors readable: give a build's bundles their debug ids, upload their maps",
		Long: `A minified bundle's errors read as one line of letters (n.weight at main.js:1:139). Its
source map turns them back into the functions, files and lines of the source.

1. Build with source maps (Vite: build.sourcemap, webpack: devtool "source-map", esbuild:
   --sourcemap; "hidden" maps are found too).
2. lyrelog sourcemaps inject dist: gives each bundle a debug id, written into the bundle and its
   map, and a snippet with which the JavaScript SDK (1.1.0 or later) sends each bundle's id with
   the stack. A bundle that has an id already keeps it.
3. Deploy the bundles as inject left them.
4. lyrelog sourcemaps upload dist: uploads each map the project doesn't have yet, with the
   bundle's functions, so frames read by their names in the source. The sources' text stays
   here: Lyrelog keeps no source code. You don't need to deploy the .map files.

Run inject and upload in the same build, after the bundler and before deploying.

React Native: withLyrelog (@lyrelog/react-native/metro, in metro.config.js) gives release
bundles their ids instead of inject; after the release build, upload android/app/build (Android)
or the map at $SOURCEMAP_FILE (iOS). Hermes's maps are found and read as they are.`,
		Example: `  lyrelog sourcemaps inject dist
  lyrelog sourcemaps upload dist
  lyrelog sourcemaps upload .next/static --project shop
  lyrelog sourcemaps upload android/app/build          # React Native, with withLyrelog`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	find := func(args []string) ([]sourcemaps.Bundle, error) {
		var all []sourcemaps.Bundle
		var inline []string
		for _, path := range args {
			found, in, err := sourcemaps.Find(path)
			if err != nil {
				return nil, err
			}
			all = append(all, found...)
			inline = append(inline, in...)
		}
		if len(inline) > 0 && !a.mode.JSON {
			a.println(a.theme.Dim("Inline source maps (left as they are, they'd send your sources to every visitor): " + strings.Join(inline, ", ")))
		}
		if len(all) == 0 {
			return nil, usagef("No bundle with a source map found at %s", strings.Join(args, ", ")).hint("Point at the build's output folder (dist, build, .next/static) after building with source maps on.")
		}
		return all, nil
	}

	inject := &cobra.Command{
		Use:   "inject <folder or bundle>...",
		Short: "Give each bundle with a source map its debug id (in the bundle and its map), before deploying",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bundles, err := find(args)
			if err != nil {
				return err
			}
			type result struct {
				Bundle  string `json:"bundle"`
				Map     string `json:"map"`
				DebugID string `json:"debug_id"`
				Changed bool   `json:"changed"`
			}
			var results []result
			for _, b := range bundles {
				r, err := sourcemaps.Inject(b)
				if err != nil {
					return err
				}
				results = append(results, result{Bundle: b.Path, Map: b.MapPath, DebugID: r.ID, Changed: r.Changed})
			}
			if a.mode.JSON {
				return a.printJSON(results)
			}
			changed := 0
			for _, r := range results {
				if r.Changed {
					changed++
					a.done("Injected " + a.theme.Bold(r.Bundle) + " " + a.theme.Dim(r.DebugID))
				} else {
					what := r.Bundle
					if what == "" {
						what = r.Map + " (React Native: withLyrelog gave it its id)"
					}
					a.println(a.theme.Dim("Already has its id: ") + what)
				}
			}
			if changed > 0 {
				a.tip("Deploy these bundles, and upload their maps:", "lyrelog sourcemaps upload "+strings.Join(args, " "))
			}
			return nil
		},
	}

	upload := &cobra.Command{
		Use:   "upload <folder or bundle>...",
		Short: "Upload the source maps of the bundles found (only those the project doesn't have yet)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			bundles, err := find(args)
			if err != nil {
				return err
			}
			root, _ := os.Getwd()
			var prepared []sourcemaps.Prepared
			var notInjected []string
			for _, b := range bundles {
				p, err := sourcemaps.Prepare(b, root)
				if errors.Is(err, sourcemaps.ErrNoDebugID) {
					notInjected = append(notInjected, b.Path)
					continue
				}
				if err != nil {
					return err
				}
				if p.Warn != "" && !a.mode.JSON {
					a.println(a.theme.Dim("Functions of " + filepath.Base(b.Path) + " not read (" + p.Warn + "): its frames show the names the stack gives."))
				}
				prepared = append(prepared, p)
			}
			if len(notInjected) > 0 {
				return usagef("%d bundles have no debug id: %s", len(notInjected), strings.Join(notInjected, ", ")).hint("Run lyrelog sourcemaps inject on the build before deploying it, then upload.")
			}
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			ids := make([]string, len(prepared))
			for i, m := range prepared {
				ids[i] = m.ID
			}
			need := map[string]bool{}
			for start := 0; start < len(ids); start += 500 {
				batch := ids[start:min(start+500, len(ids))]
				var missing struct {
					Missing []string `json:"missing"`
				}
				if err := a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: projectPath(p, "symbols/missing"), Body: map[string]any{"uuids": batch}}, &missing); err != nil {
					return err
				}
				for _, id := range missing.Missing {
					need[id] = true
				}
			}
			type result struct {
				DebugID string `json:"debug_id"`
				Bundle  string `json:"bundle"`
				Status  string `json:"status"` // uploaded, already_there
				Bytes   int    `json:"bytes,omitempty"`
			}
			var results []result
			for _, m := range prepared {
				r := result{DebugID: m.ID, Bundle: m.Name, Status: "already_there"}
				if need[m.ID] {
					data, err := gzipBytes(m.Map)
					if err != nil {
						return err
					}
					sp := a.spin("Uploading the map of " + m.Name + " (" + mib(int64(len(data))) + ")")
					err = a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: projectPath(p, "symbols"), Body: data, ContentType: "application/gzip"}, nil)
					sp.Stop()
					if err != nil {
						return err
					}
					r.Status, r.Bytes = "uploaded", len(data)
					need[m.ID] = false
				}
				results = append(results, r)
			}
			if a.mode.JSON {
				return a.printJSON(results)
			}
			for _, r := range results {
				what := a.theme.Bold(r.Bundle) + " " + a.theme.Dim(r.DebugID)
				if r.Status == "uploaded" {
					a.done("Uploaded the map of " + what)
				} else {
					a.println(a.theme.Dim("Already there: ") + what)
				}
			}
			return nil
		},
	}
	cmd.AddCommand(inject, upload)
	return cmd
}
