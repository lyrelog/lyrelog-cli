package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archive builds a .tar.gz shaped like GitHub's: everything under skills-main/.
func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "skills-main/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

const newerSkill = "---\nname: lyrelog-investigate\ndescription: Investigates. Use when asked.\nmetadata:\n  version: \"9.9.9\"\n---\n\n# Newer\n"

func skillsServer(t *testing.T, status int) string {
	body := archive(t, map[string]string{
		"README.md":                           "not a skill",
		"skills/lyrelog-investigate/SKILL.md": newerSkill,
		"skills/lyrelog-investigate/ref/x.md": "a reference",
		"../escape/SKILL.md":                  "never written",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == 200 {
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSkillsInstallTheNewestFromGitHub(t *testing.T) {
	f := newFake(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	out, errOut, code := f.run(t, runOpts{env: map[string]string{"HOME": home, "LYRELOG_SKILLS_URL": skillsServer(t, 200)}}, "skills", "install")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var got []skillResult
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 {
		t.Fatalf("want one skill installed, got %s (%v)", out, err)
	}
	dir := filepath.Join(home, ".claude", "skills", "lyrelog-investigate")
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != newerSkill {
		t.Fatalf("SKILL.md is %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "ref", "x.md")); err != nil {
		t.Fatal("the skill's other files weren't written")
	}
	if got[0].Version != "9.9.9" || errOut != "" {
		t.Fatalf("version %q, stderr %q", got[0].Version, errOut)
	}
}

func TestSkillsFallBackToTheBundledCopy(t *testing.T) {
	f := newFake(t)
	home := t.TempDir()
	_, errOut, code := f.run(t, runOpts{env: map[string]string{"HOME": home, "LYRELOG_SKILLS_URL": skillsServer(t, 503)}}, "skills", "install", "--agent", "codex")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "using the ones that came with the CLI") {
		t.Fatalf("no notice of the fallback: %q", errOut)
	}
	for _, name := range []string{"lyrelog-integrate", "lyrelog-investigate", "lyrelog-keys", "lyrelog-alerts"} {
		if _, err := os.Stat(filepath.Join(home, ".agents", "skills", name, "SKILL.md")); err != nil {
			t.Errorf("%s not installed for Codex", name)
		}
	}
}

func TestSkillsInAProjectAndRemoveOnlyOurs(t *testing.T) {
	f := newFake(t)
	project := t.TempDir()
	t.Chdir(project)
	theirs := filepath.Join(project, ".agents", "skills", "someone-elses")
	_ = os.MkdirAll(theirs, 0o755)
	_ = os.WriteFile(filepath.Join(theirs, "SKILL.md"), []byte("keep me"), 0o644)
	env := map[string]string{"HOME": t.TempDir()}
	if _, errOut, code := f.run(t, runOpts{env: env}, "skills", "install", "--project", "--offline", "--skill", "lyrelog-keys"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, d := range []string{".claude/skills", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(project, d, "lyrelog-keys", "SKILL.md")); err != nil {
			t.Errorf("not installed in %s", d)
		}
		if _, err := os.Stat(filepath.Join(project, d, "lyrelog-alerts")); err == nil {
			t.Errorf("--skill installed more than asked in %s", d)
		}
	}
	if _, errOut, code := f.run(t, runOpts{env: env}, "skills", "remove", "--project"); code != 0 {
		t.Fatalf("remove: exit %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude/skills/lyrelog-keys")); err == nil {
		t.Error("ours is still there")
	}
	if b, _ := os.ReadFile(filepath.Join(theirs, "SKILL.md")); string(b) != "keep me" {
		t.Error("remove touched another skill")
	}
}

func TestSkillsWithoutAnAgentSaysHow(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{env: map[string]string{"HOME": t.TempDir()}}, "skills", "install", "--offline")
	if code != ExitUsage || !strings.Contains(errOut, "--agent") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestSkillsAskWhereAndWhichThenRemember(t *testing.T) {
	f := newFake(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".cursor"), 0o755)
	cfgDir := t.TempDir()
	env := map[string]string{"HOME": home, "NO_COLOR": "1", "LYRELOG_CONFIG_DIR": cfgDir}
	// Where: 1 (for you). Which: 1 and 3 (Claude Code, Cursor).
	_, errOut, code := f.run(t, runOpts{tty: true, env: env, stdin: "1\n1,3\n", noConfig: true}, "skills", "install", "--offline")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "[x] Cursor") || !strings.Contains(errOut, "[ ] Claude Code") {
		t.Errorf("the agents found should start ticked:\n%s", errOut)
	}
	for _, d := range []string{".claude/skills", ".cursor/skills"} {
		if _, err := os.Stat(filepath.Join(home, d, "lyrelog-keys", "SKILL.md")); err != nil {
			t.Errorf("not installed in ~/%s", d)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	var saved struct {
		Skills struct {
			Scope  string   `json:"scope"`
			Agents []string `json:"agents"`
		} `json:"skills"`
	}
	_ = json.Unmarshal(raw, &saved)
	if saved.Skills.Scope != "user" || strings.Join(saved.Skills.Agents, ",") != "claude-code,cursor" {
		t.Fatalf("the choice wasn't saved: %s", raw)
	}
	// Next time: no question, the same folders, even with nothing to read on stdin.
	_ = os.RemoveAll(filepath.Join(home, ".claude", "skills"))
	_, errOut, code = f.run(t, runOpts{tty: true, env: env, noConfig: true}, "skills", "install", "--offline")
	if code != 0 || strings.Contains(errOut, "Where should") {
		t.Fatalf("asked again, or failed (exit %d): %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/lyrelog-keys/SKILL.md")); err != nil {
		t.Error("the saved choice wasn't used")
	}
	// --agent wins over the saved choice.
	_, errOut, code = f.run(t, runOpts{tty: true, env: env, noConfig: true}, "skills", "install", "--offline", "--agent", "gemini-cli")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini/skills/lyrelog-keys/SKILL.md")); err != nil {
		t.Error("--agent was ignored")
	}
}

func TestSkillsChooseNeedsAPerson(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{env: map[string]string{"HOME": t.TempDir()}}, "skills", "install", "--offline", "--choose")
	if code != ExitUsage || !strings.Contains(errOut, "--choose") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestKeysTypedFastAreSplit(t *testing.T) {
	got := strings.Join(splitKeys([]byte("\x1b[B\x1b[B a\r")), "|")
	if got != "\x1b[B|\x1b[B| |a|\r" {
		t.Fatalf("got %q", got)
	}
}
