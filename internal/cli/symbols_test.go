package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Uploading a dSYM sends its map, gzip, for the builds the project doesn't have; JSON says what
// happened to each.
func TestSymbolsUploadSendsTheMapOfEachMissingBuild(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{}, "symbols", "upload", "../symbols/testdata/app.dwarf", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var results []struct{ UUID, Binary, Arch, Status string }
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 {
		t.Fatalf("output %q: %v", out, err)
	}
	if r := results[0]; r.UUID != "A3F11B8C-0F44-3ABF-A3AA-25AC3AF39B8F" || r.Arch != "arm64" || r.Status != "uploaded" {
		t.Fatalf("result: %+v", r)
	}
	if len(f.uploads) != 1 || f.uploads[0].contentType != "application/gzip" {
		t.Fatalf("uploads: %d", len(f.uploads))
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.uploads[0].body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	var m struct {
		Version int    `json:"lyrelog_symbols"`
		UUID    string `json:"uuid"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Version != 1 || m.UUID != "A3F11B8C-0F44-3ABF-A3AA-25AC3AF39B8F" {
		t.Fatalf("the uploaded map: %+v %v", m, err)
	}
}

func TestSymbolsUploadWithoutADSYMSaysWhereToPoint(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "symbols", "upload", t.TempDir(), "--json")
	if code == 0 || !strings.Contains(errOut, "No dSYM, R8 mapping or native library found") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestSymbolsListsTheProjectsBuilds(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{tty: true}, "symbols")
	if code != 0 || !strings.Contains(out, "Tilefall") || !strings.Contains(out, "2.3 MB") || !strings.Contains(out, "of 1.0 GB used") {
		t.Fatalf("exit %d: %s\n%s", code, out, errOut)
	}
}

// An Android release's mapping.txt is uploaded as it is (gzip), under its map id: the id every
// frame of that build names. Found inside a project folder too.
func TestSymbolsUploadSendsAnR8Mapping(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app", "build", "outputs", "mapping", "release", "mapping.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "0bf7286072d03642f0a5dd40075fcb11200b239911b907179c70080d08263a1d"
	mapping := "# compiler: R8\n# compiler_version: 9.5.29\n# pg_map_id: " + id + "\n" +
		"com.example.tilefall.BoardActivity -> com.example.tilefall.BoardActivity:\n    1:3:void <init>():15:15 -> <init>\n"
	if err := os.WriteFile(path, []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := f.run(t, runOpts{}, "symbols", "upload", dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, `"uuid":"`+id+`"`) || !strings.Contains(out, `"status":"uploaded"`) {
		t.Fatalf("output: %s", out)
	}
	if len(f.uploads) != 1 {
		t.Fatalf("uploads: %d", len(f.uploads))
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.uploads[0].body))
	if err != nil {
		t.Fatal(err)
	}
	sent, _ := io.ReadAll(zr)
	if string(sent) != mapping {
		t.Fatalf("the mapping sent isn't the file: %q", sent)
	}
}

// An Android build folder holds each native library more than once (Gradle's unstripped copy
// under intermediates, the stripped one it packs): the library is uploaded once, from the copy
// with its debug information, under its build id.
func TestSymbolsUploadSendsANativeLibraryFromItsRichestCopy(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	copyTo := func(from, to string) {
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		must(t, os.MkdirAll(filepath.Dir(to), 0o755))
		must(t, os.WriteFile(to, data, 0o644))
	}
	copyTo("../symbols/testdata/native.stripped.so", filepath.Join(dir, "intermediates", "stripped_native_libs", "release", "out", "lib", "arm64-v8a", "libtilefall.so"))
	copyTo("../symbols/testdata/native.so", filepath.Join(dir, "intermediates", "merged_native_libs", "release", "out", "lib", "arm64-v8a", "libtilefall.so"))
	out, errOut, code := f.run(t, runOpts{}, "symbols", "upload", dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, `"uuid":"0ebb2808997eeafc84f0fcfb81b4cee0c2b53f68"`) || !strings.Contains(out, `"binary":"libtilefall.so"`) || len(f.uploads) != 1 {
		t.Fatalf("uploads %d, output: %s", len(f.uploads), out)
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.uploads[0].body))
	must(t, err)
	var m struct {
		Functions [][3]uint64 `json:"functions"`
		Lines     [][3]uint64 `json:"lines"`
	}
	must(t, json.NewDecoder(zr).Decode(&m))
	if len(m.Lines) == 0 {
		t.Fatal("uploaded from the stripped copy: no lines")
	}
}


func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
