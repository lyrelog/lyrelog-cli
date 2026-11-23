// Package skills reads Lyrelog's agent skills (github.com/lyrelog/skills): the latest, from the
// repository's archive on GitHub, or the copy inside the binary (files/, a copy of the
// repository's skills/ folder that `make skills` refreshes) when GitHub can't be reached. So the
// skills improve without a new CLI, and installing still works offline.
package skills

import (
	"archive/tar"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing/fstest"
)

// ArchiveURL is the repository's main branch as a .tar.gz.
const ArchiveURL = "https://codeload.github.com/lyrelog/skills/tar.gz/refs/heads/main"

// maxArchive caps what's read from the network: the skills are a few tens of KB.
const maxArchive = 4 << 20

//go:embed files
var bundled embed.FS

// Set is a set of skills read from one place.
type Set struct {
	fsys fs.FS // skill folders at its root
	// From says where they came from: "github" or "bundled".
	From string
}

// Bundled is the copy inside the binary.
func Bundled() Set {
	sub, _ := fs.Sub(bundled, "files")
	return Set{fsys: sub, From: "bundled"}
}

// FromArchive reads the skills/ folder of the repository's .tar.gz (GitHub names its top folder
// <repo>-<ref>, so the first path segment is skipped).
func FromArchive(r io.Reader) (Set, error) {
	gz, err := gzip.NewReader(io.LimitReader(r, maxArchive))
	if err != nil {
		return Set{}, err
	}
	tr := tar.NewReader(gz)
	m := fstest.MapFS{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Set{}, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		_, rest, ok := strings.Cut(h.Name, "/")
		rel, inSkills := strings.CutPrefix(rest, "skills/")
		if !ok || !inSkills || rel == "" || !fs.ValidPath(rel) {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxArchive))
		if err != nil {
			return Set{}, err
		}
		m[rel] = &fstest.MapFile{Data: data, Mode: 0o644}
	}
	s := Set{fsys: m, From: "github"}
	if len(s.All()) == 0 {
		return Set{}, fmt.Errorf("the archive has no skills")
	}
	return s, nil
}

// Skill is one skill: its folder name, its description and version (from SKILL.md's
// frontmatter), and its files relative to the folder.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Files       []string `json:"-"`
}

// All lists the skills, by name: every folder with a SKILL.md.
func (s Set) All() []Skill {
	entries, _ := fs.ReadDir(s.fsys, ".")
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		text, err := fs.ReadFile(s.fsys, path.Join(e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		sk := Skill{Name: e.Name()}
		sk.Description, sk.Version = Frontmatter(string(text))
		_ = fs.WalkDir(s.fsys, e.Name(), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				sk.Files = append(sk.Files, strings.TrimPrefix(p, e.Name()+"/"))
			}
			return nil
		})
		sort.Strings(sk.Files)
		out = append(out, sk)
	}
	return out
}

// Read returns one file of a skill.
func (s Set) Read(skill, file string) ([]byte, error) {
	return fs.ReadFile(s.fsys, path.Join(skill, file))
}

// Frontmatter reads the description and metadata.version from a SKILL.md ("" when missing).
func Frontmatter(text string) (description, version string) {
	if !strings.HasPrefix(text, "---\n") {
		return "", ""
	}
	head, _, _ := strings.Cut(text[4:], "\n---\n")
	for _, line := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			description = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "version:"); ok && strings.HasPrefix(line, " ") {
			version = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return description, version
}
