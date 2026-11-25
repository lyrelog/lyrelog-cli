package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
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
	if code == 0 || !strings.Contains(errOut, "No dSYM found") {
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
