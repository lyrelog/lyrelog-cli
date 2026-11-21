package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveIsPrivateAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	f := &File{URL: "http://127.0.0.1:8090"}
	f.Host("http://127.0.0.1:8090").Token = "lyr_cli_secret"
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", info.Mode().Perm())
		}
		dir, _ := os.Stat(filepath.Dir(path))
		if dir.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v, want 0700", dir.Mode().Perm())
		}
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Hosts["http://127.0.0.1:8090"].Token != "lyr_cli_secret" || back.URL != f.URL {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || f.Hosts == nil {
		t.Fatalf("got %v, %v", f, err)
	}
}

func TestNormalizeURL(t *testing.T) {
	good := map[string]string{
		"":                                  DefaultURL,
		"app.lyrelog.com":                   "https://app.lyrelog.com",
		"https://App.Lyrelog.com/p/abc?x=1": "https://app.lyrelog.com",
		"http://127.0.0.1:8090/":            "http://127.0.0.1:8090",
		"http://localhost:8090":             "http://localhost:8090",
	}
	for in, want := range good {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://app.lyrelog.com", "ftp://x.com", "https://u:p@x.com", "https://"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) should fail", bad)
		}
	}
}

func TestDir(t *testing.T) {
	env := map[string]string{"LYRELOG_CONFIG_DIR": "/tmp/x"}
	if d, _ := Dir(func(k string) string { return env[k] }); d != "/tmp/x" {
		t.Fatal(d)
	}
	if runtime.GOOS != "windows" {
		env = map[string]string{"XDG_CONFIG_HOME": "/xdg"}
		if d, _ := Dir(func(k string) string { return env[k] }); d != "/xdg/lyrelog" {
			t.Fatal(d)
		}
	}
}
