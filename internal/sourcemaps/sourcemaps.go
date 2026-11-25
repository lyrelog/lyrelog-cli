// Package sourcemaps prepares a web build's source maps for Lyrelog (docs: phase 4,
// 2026-11-25): each bundle gets a debug id, written into the bundle and its map, with a snippet
// that tells the JavaScript SDK which id a bundle has (so an exception's stack names the maps it
// needs); then each map is uploaded with that id and the bundle's functions.
package sourcemaps

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Bundle is a JavaScript file of the build and its source map.
type Bundle struct {
	Path    string
	MapPath string
}

// The snippet registers the bundle's id under the stack of an Error made in the bundle itself:
// the SDK reads the bundle's URL from that stack. It goes at the very start of the bundle (after
// a hashbang and directives), so it has run before anything in the bundle can throw.
const snippetTemplate = `;!function(){try{var e="undefined"!=typeof globalThis?globalThis:"undefined"!=typeof window?window:"undefined"!=typeof self?self:"undefined"!=typeof global?global:{},n=(new e.Error).stack;n&&((e._lyrelogDebugIds=e._lyrelogDebugIds||{})[n]="%s")}catch(e){}}();`

func snippet(id string) string { return fmt.Sprintf(snippetTemplate, id) }

var (
	debugIDComment = regexp.MustCompile(`(?m)^//# debugId=([0-9a-fA-F-]{36})\s*$`)
	mapURLComment  = regexp.MustCompile(`(?m)^//[#@] sourceMappingURL=(\S+)\s*$`)
	uuidRule       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ErrInlineMap: the bundle carries its map inline (a data: URL), which would ship the sources to
// every visitor; Lyrelog reads external maps.
var ErrInlineMap = errors.New("an inline source map (data: URL)")

// Find lists the bundles under root that have a source map: the one their sourceMappingURL
// comment names, else the file next to them with .map added (a "hidden" map, Vite's
// build.sourcemap: 'hidden'). inline are the bundles whose map is inline.
func Find(root string) (bundles []Bundle, inline []string, err error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, err
	}
	visit := func(path string) error {
		if !isBundleName(path) {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mapPath := ""
		if all := mapURLComment.FindAllSubmatch(text, -1); len(all) > 0 {
			url := string(all[len(all)-1][1])
			if strings.HasPrefix(url, "data:") {
				inline = append(inline, path)
				return nil
			}
			if !strings.Contains(url, "://") {
				if i := strings.IndexAny(url, "?#"); i >= 0 {
					url = url[:i]
				}
				mapPath = filepath.Join(filepath.Dir(path), filepath.FromSlash(url))
			}
		}
		if mapPath == "" || !exists(mapPath) {
			mapPath = path + ".map"
		}
		if exists(mapPath) {
			bundles = append(bundles, Bundle{Path: path, MapPath: mapPath})
		}
		return nil
	}
	if !info.IsDir() {
		return bundles, inline, visit(root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			return visit(path)
		}
		return nil
	})
	return bundles, inline, err
}

func isBundleName(path string) bool {
	for _, ext := range []string{".js", ".mjs", ".cjs"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// DebugIDOf is the bundle's debug id: its last `//# debugId=` comment, else its map's.
func DebugIDOf(bundle []byte, rawMap map[string]json.RawMessage) string {
	if all := debugIDComment.FindAllSubmatch(bundle, -1); len(all) > 0 {
		if id := strings.ToLower(string(all[len(all)-1][1])); uuidRule.MatchString(id) {
			return id
		}
	}
	for _, key := range []string{"debugId", "debug_id"} {
		var id string
		if json.Unmarshal(rawMap[key], &id) == nil {
			if id = strings.ToLower(id); uuidRule.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

// newID is a debug id from the bundle's bytes: the same build gets the same id, so running inject
// twice, or on two machines, agrees.
func newID(bundle []byte) string {
	h := sha256.Sum256(bundle)
	h[6] = h[6]&0x0f | 0x40 // version 4
	h[8] = h[8]&0x3f | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// InjectResult says what Inject did to a bundle.
type InjectResult struct {
	ID      string
	Changed bool // the bundle or its map was written
}

// Inject gives a bundle its debug id: the snippet at its start, `//# debugId=` at its end, and
// `debugId` in its map, whose mappings move with the snippet. A bundle that has an id already
// keeps it (Rollup's and Rolldown's sourcemapDebugIds, Turbopack's, an earlier inject), and one
// that registers it already (an earlier inject, Sentry's bundler plugins) is left as it is.
func Inject(b Bundle) (InjectResult, error) {
	bundle, err := os.ReadFile(b.Path)
	if err != nil {
		return InjectResult{}, err
	}
	mapText, err := os.ReadFile(b.MapPath)
	if err != nil {
		return InjectResult{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mapText, &raw); err != nil {
		return InjectResult{}, fmt.Errorf("%s: not a source map: %w", b.MapPath, err)
	}
	id := DebugIDOf(bundle, raw)
	registered := bytes.Contains(bundle, []byte("_lyrelogDebugIds")) || bytes.Contains(bundle, []byte("_sentryDebugIds"))
	if registered && id != "" {
		if DebugIDOf(nil, raw) == id {
			return InjectResult{ID: id}, nil
		}
		raw["debugId"], _ = json.Marshal(id)
		return InjectResult{ID: id, Changed: true}, writeJSON(b.MapPath, raw)
	}
	if id == "" {
		id = newID(bundle)
	}
	code := snippet(id)
	off, at := insertionPoint(bundle)
	if err := shiftMap(raw, at, int64(len(code))); err != nil {
		return InjectResult{}, fmt.Errorf("%s: %w", b.MapPath, err)
	}
	raw["debugId"], _ = json.Marshal(id)
	var out bytes.Buffer
	out.Write(bundle[:off])
	out.WriteString(code)
	out.Write(bundle[off:])
	if !debugIDComment.Match(bundle) {
		if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
			out.WriteByte('\n')
		}
		out.WriteString("//# debugId=" + id + "\n")
	}
	if err := writeKeepingMode(b.Path, out.Bytes()); err != nil {
		return InjectResult{}, err
	}
	return InjectResult{ID: id, Changed: true}, writeJSON(b.MapPath, raw)
}

// directive is a directive at the start of code ("use strict";, "use client";): a statement
// before it would turn it into a plain string.
var directive = regexp.MustCompile(`^(?:\s|/\*[^*]*\*+(?:[^/*][^*]*\*+)*/)*("[^"\\\n]*"|'[^'\\\n]*')\s*;`)

// insertionPoint is where the snippet goes: after a hashbang line and the directives that open
// the bundle, as a byte offset and a generated position.
func insertionPoint(bundle []byte) (int, pos) {
	off := 0
	if bytes.HasPrefix(bundle, []byte("#!")) {
		if nl := bytes.IndexByte(bundle, '\n'); nl >= 0 {
			off = nl + 1
		} else {
			off = len(bundle)
		}
	}
	for {
		m := directive.FindIndex(bundle[off:])
		if m == nil {
			break
		}
		off += m[1]
	}
	return off, advance(pos{}, bundle[:off])
}

// shiftMap moves the map's mappings after at by n columns of the same line; an index map's
// sections move with them.
func shiftMap(raw map[string]json.RawMessage, at pos, n int64) error {
	if sections, ok := raw["sections"]; ok {
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(sections, &list); err != nil {
			return err
		}
		inside := -1 // the section the point is in: the last one starting before it
		for i, s := range list {
			var off struct{ Line, Column uint32 }
			if err := json.Unmarshal(s["offset"], &off); err != nil {
				return err
			}
			start := pos{off.Line, off.Column}
			switch {
			case start.line == at.line && !before(start, at):
				// Starts at the point or after it on its line: the whole section moves.
				s["offset"], _ = json.Marshal(map[string]uint32{"line": off.Line, "column": off.Column + uint32(n)})
			case before(start, at):
				inside = i
			}
		}
		if inside >= 0 {
			var off struct{ Line, Column uint32 }
			_ = json.Unmarshal(list[inside]["offset"], &off)
			var sub map[string]json.RawMessage
			if err := json.Unmarshal(list[inside]["map"], &sub); err != nil {
				return err
			}
			local := pos{at.line - off.Line, at.col}
			if at.line == off.Line {
				local.col = at.col - off.Column
			}
			if err := shiftMap(sub, local, n); err != nil {
				return err
			}
			list[inside]["map"], _ = json.Marshal(sub)
		}
		raw["sections"], _ = json.Marshal(list)
		return nil
	}
	var mappings string
	if err := json.Unmarshal(raw["mappings"], &mappings); err != nil {
		return errors.New("no mappings")
	}
	shifted, err := shiftLine(mappings, at.line, at.col, n)
	if err != nil {
		return err
	}
	raw["mappings"], _ = json.Marshal(shifted)
	return nil
}

func writeKeepingMode(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}

func writeJSON(path string, raw map[string]json.RawMessage) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return writeKeepingMode(path, data)
}

// ErrNoDebugID: the bundle has no debug id yet.
var ErrNoDebugID = errors.New("no debug id: run lyrelog sourcemaps inject on the build first")

// Prepared is a map ready to upload.
type Prepared struct {
	ID     string
	Name   string // the bundle's file name
	Map    []byte // the map's JSON, as uploaded (before gzip)
	Warn   string // why the bundle's functions couldn't be read, if they couldn't
	Shrunk int    // bytes of the sources' text left out
}

// Prepare reads a bundle's map for upload: with its debug id and the bundle's functions, without
// the sources' text (Lyrelog keeps no source code, and refuses a map with it), and with sources
// given as absolute paths made relative to root (or their file names), so no build machine's
// folders leave it.
func Prepare(b Bundle, root string) (Prepared, error) {
	bundle, err := os.ReadFile(b.Path)
	if err != nil {
		return Prepared{}, err
	}
	mapText, err := os.ReadFile(b.MapPath)
	if err != nil {
		return Prepared{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mapText, &raw); err != nil {
		return Prepared{}, fmt.Errorf("%s: not a source map: %w", b.MapPath, err)
	}
	p := Prepared{ID: DebugIDOf(bundle, raw), Name: filepath.Base(b.Path)}
	if p.ID == "" {
		return p, fmt.Errorf("%s: %w", b.Path, ErrNoDebugID)
	}
	named := map[pos]string{}
	if err := cleanMap(raw, root, named, 0, 0, &p.Shrunk); err != nil {
		return p, fmt.Errorf("%s: %w", b.MapPath, err)
	}
	if fns, err := ReadFunctions(bundle, named); err != nil {
		p.Warn = err.Error()
	} else {
		raw["x_lyrelog_functions"], _ = json.Marshal(fns)
	}
	raw["debugId"], _ = json.Marshal(p.ID)
	delete(raw, "debug_id")
	var file string
	if json.Unmarshal(raw["file"], &file) != nil || file == "" {
		raw["file"], _ = json.Marshal(p.Name)
	}
	p.Map, err = json.Marshal(raw)
	return p, err
}

// cleanMap takes the sources' text out, makes sources relative, and collects the map's names by
// position (sections included, at their offsets).
func cleanMap(raw map[string]json.RawMessage, root string, named map[pos]string, line, col uint32, shrunk *int) error {
	if sections, ok := raw["sections"]; ok {
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(sections, &list); err != nil {
			return err
		}
		for _, s := range list {
			var off struct{ Line, Column uint32 }
			if err := json.Unmarshal(s["offset"], &off); err != nil {
				return err
			}
			var sub map[string]json.RawMessage
			if err := json.Unmarshal(s["map"], &sub); err != nil {
				return err
			}
			if err := cleanMap(sub, root, named, line+off.Line, col+off.Column, shrunk); err != nil {
				return err
			}
			s["map"], _ = json.Marshal(sub)
		}
		raw["sections"], _ = json.Marshal(list)
		return nil
	}
	*shrunk += len(raw["sourcesContent"])
	delete(raw, "sourcesContent")
	var sources []*string
	if err := json.Unmarshal(raw["sources"], &sources); err != nil {
		return errors.New("no sources")
	}
	for _, s := range sources {
		if s != nil {
			*s = relativeSource(*s, root)
		}
	}
	raw["sources"], _ = json.Marshal(sources)
	var mappings string
	var names []string
	if err := json.Unmarshal(raw["mappings"], &mappings); err != nil {
		return errors.New("no mappings")
	}
	_ = json.Unmarshal(raw["names"], &names)
	return namedAt(mappings, names, line, col, named)
}

// relativeSource: an absolute path (or file:// URL) relative to root when it's under it, else
// its file name. Relative sources and other URLs stay.
func relativeSource(s, root string) string {
	path, isFile := strings.CutPrefix(s, "file://")
	if !isFile && !filepath.IsAbs(path) && !(len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')) {
		return s
	}
	if root != "" {
		if rel, err := filepath.Rel(root, filepath.FromSlash(path)); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(filepath.FromSlash(path))
}
