// Package config keeps the CLI's settings in one JSON file, mode 0600: the dashboard address,
// and for each address the sign-in token, who it belongs to and the default project.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultURL is the hosted dashboard; local development is http://127.0.0.1:8090.
const DefaultURL = "https://app.lyrelog.com"

// Host is what the CLI knows about one Lyrelog server.
type Host struct {
	Token string `json:"token,omitempty"`
	// ExpiresAt: when the session ends at the latest (Unix seconds); 0 when unknown.
	ExpiresAt int64  `json:"expires_at,omitempty"`
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	// Project is the default project's public id; ProjectName is for messages only.
	Project     string `json:"project,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
}

// File is the config file's content.
type File struct {
	// URL is the server used when neither --url nor LYRELOG_URL says otherwise.
	URL   string           `json:"url,omitempty"`
	Hosts map[string]*Host `json:"hosts,omitempty"`
}

// Dir is where the file lives: LYRELOG_CONFIG_DIR, else $XDG_CONFIG_HOME/lyrelog, else
// ~/.config/lyrelog (macOS and Linux alike, like gh), %AppData%\lyrelog on Windows.
func Dir(getenv func(string) string) (string, error) {
	if dir := getenv("LYRELOG_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "lyrelog"), nil
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "lyrelog"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "lyrelog"), nil
}

// Load reads the file at path; a missing file is an empty config.
func Load(path string) (*File, error) {
	f := &File{Hosts: map[string]*Host{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v): fix it or delete it", path, err)
	}
	if f.Hosts == nil {
		f.Hosts = map[string]*Host{}
	}
	return f, nil
}

// Save writes the file atomically with mode 0600, in a 0700 directory.
func (f *File) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Host returns the entry for a server, made if missing.
func (f *File) Host(base string) *Host {
	if f.Hosts == nil {
		f.Hosts = map[string]*Host{}
	}
	h := f.Hosts[base]
	if h == nil {
		h = &Host{}
		f.Hosts[base] = h
	}
	return h
}

// NormalizeURL checks a dashboard address and returns it without a trailing slash or path:
// https, or http for loopback only (local development). A pasted dashboard page is cut to its origin.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultURL, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a web address", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return "", fmt.Errorf("only https addresses work (http only on 127.0.0.1, for local development), not %s", raw)
		}
	default:
		return "", fmt.Errorf("the address starts with https://, not %s", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("leave the user and password out of the address %s", raw)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}
