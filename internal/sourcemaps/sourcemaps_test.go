package sourcemaps

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type segment struct{ line, col int64 }

// segments are every mapping's generated position, with what follows it as text, to compare two
// maps' mappings.
func segments(t *testing.T, mappings string) map[segment][4]int64 {
	t.Helper()
	out := map[segment][4]int64{}
	var src, oline, ocol, name int64
	for line, text := range strings.Split(mappings, ";") {
		var col int64
		for _, seg := range strings.Split(text, ",") {
			if seg == "" {
				continue
			}
			var f [5]int64
			n := 0
			for rest := seg; rest != ""; n++ {
				v, r, ok := readVLQ(rest)
				if !ok {
					t.Fatalf("bad segment %q", seg)
				}
				f[n], rest = v, r
			}
			col += f[0]
			if n >= 4 {
				src, oline, ocol = src+f[1], oline+f[2], ocol+f[3]
			}
			if n == 5 {
				name += f[4]
			}
			out[segment{int64(line), col}] = [4]int64{src, oline, ocol, name}
		}
	}
	return out
}

func copyBuild(t *testing.T, dir string) Bundle {
	t.Helper()
	out := t.TempDir()
	for _, name := range []string{"main.js", "main.js.map"} {
		data, err := os.ReadFile(filepath.Join("testdata/tilefall", dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bundles, inline, err := Find(out)
	if err != nil || len(bundles) != 1 || len(inline) != 0 {
		t.Fatalf("%v %v %v", bundles, inline, err)
	}
	return bundles[0]
}

func mappingsOf(t *testing.T, path string) (map[string]json.RawMessage, string) {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(text, &raw); err != nil {
		t.Fatal(err)
	}
	var m string
	_ = json.Unmarshal(raw["mappings"], &m)
	return raw, m
}

// Inject puts the snippet first and the debug id last, and moves the map with it: every segment
// at the snippet's line is where it was plus the snippet's length, the rest untouched. A second
// run changes nothing, and the id is the same for the same build.
func TestInjectGivesABundleItsDebugIDAndKeepsItsMapTrue(t *testing.T) {
	for _, dir := range []string{"esbuild", "rolldown"} {
		t.Run(dir, func(t *testing.T) {
			b := copyBuild(t, dir)
			before, _ := os.ReadFile(b.Path)
			_, oldMappings := mappingsOf(t, b.MapPath)
			res, err := Inject(b)
			if err != nil || !res.Changed || !uuidRule.MatchString(res.ID) {
				t.Fatalf("%+v %v", res, err)
			}
			after, _ := os.ReadFile(b.Path)
			code := snippet(res.ID)
			if !bytes.HasPrefix(after, []byte(code)) || !bytes.HasSuffix(after, []byte("//# debugId="+res.ID+"\n")) {
				t.Fatalf("bundle: %.200s ... %s", after, after[len(after)-80:])
			}
			if !bytes.Contains(after, before) {
				t.Fatal("the bundle's own code changed")
			}
			raw, newMappings := mappingsOf(t, b.MapPath)
			if DebugIDOf(nil, raw) != res.ID {
				t.Fatal("map has no debugId")
			}
			old, moved := segments(t, oldMappings), segments(t, newMappings)
			if len(old) != len(moved) {
				t.Fatalf("%d segments, %d after", len(old), len(moved))
			}
			for at, v := range old {
				want := at
				if at.line == 0 {
					want.col += int64(len(code))
				}
				if moved[want] != v {
					t.Fatalf("segment %v: %v moved to %v holds %v", at, v, want, moved[want])
				}
			}
			again, err := Inject(b)
			if err != nil || again.Changed || again.ID != res.ID {
				t.Fatalf("again: %+v %v", again, err)
			}
			if fresh := copyBuild(t, dir); func() string { r, _ := Inject(fresh); return r.ID }() != res.ID {
				t.Fatal("the same build got another id")
			}
		})
	}
}

// The snippet goes after a hashbang and after the directives that open the bundle, so "use
// strict" and "use client" still apply.
func TestTheSnippetGoesAfterHashbangAndDirectives(t *testing.T) {
	for src, want := range map[string]string{
		"var a=1;":                      "",
		"#!/usr/bin/env node\nvar a=1;": "#!/usr/bin/env node\n",
		`"use strict";var a=1;`:         `"use strict";`,
		"/*! licence */\n\"use client\";\n'use strict';a()": "/*! licence */\n\"use client\";\n'use strict';",
		`"use strict".length;`:                              "",
	} {
		off, at := insertionPoint([]byte(src))
		if src[:off] != want {
			t.Errorf("%q: before the snippet %q, want %q", src, src[:off], want)
		}
		if at != advance(pos{}, []byte(want)) {
			t.Errorf("%q: %+v", src, at)
		}
	}
}

// What's uploaded: the map with its id, the bundle's functions and its file name, without the
// sources' text, and without the build machine's folders.
func TestPrepareUploadsTheMapWithoutTheSources(t *testing.T) {
	b := copyBuild(t, "esbuild")
	if _, err := Prepare(b, ""); err == nil || !strings.Contains(err.Error(), "inject") {
		t.Fatalf("before inject: %v", err)
	}
	res, err := Inject(b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(b, "/srv/tilefall")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		DebugID        string     `json:"debugId"`
		File           string     `json:"file"`
		Sources        []string   `json:"sources"`
		SourcesContent []string   `json:"sourcesContent"`
		Functions      *Functions `json:"x_lyrelog_functions"`
	}
	if err := json.Unmarshal(p.Map, &m); err != nil {
		t.Fatal(err)
	}
	if m.DebugID != res.ID || p.ID != res.ID || m.SourcesContent != nil || p.Shrunk == 0 || m.Functions == nil || len(m.Functions.Ranges) < 10 || p.Warn != "" {
		t.Fatalf("%+v %+v", p, m)
	}
	for in, want := range map[string]string{
		"/srv/tilefall/src/board.ts":        "src/board.ts",
		"file:///srv/tilefall/src/board.ts": "src/board.ts",
		"/home/someone/elsewhere/board.ts":  "board.ts",
		"/home/someone/repo/node_modules/react-native/Libraries/Pressability/Pressability.js": "node_modules/react-native/Libraries/Pressability/Pressability.js",
		"../src/board.ts":               "../src/board.ts",
		"webpack://tilefall/./src/a.ts": "webpack://tilefall/./src/a.ts",
	} {
		if got := relativeSource(in, "/srv/tilefall"); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
