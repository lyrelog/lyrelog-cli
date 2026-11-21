package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPKCE(t *testing.T) {
	allowed := regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	seen := map[string]bool{}
	for range 50 {
		p, err := NewPKCE()
		if err != nil {
			t.Fatal(err)
		}
		if !allowed.MatchString(p.Verifier) {
			t.Fatalf("verifier %q breaks RFC 7636", p.Verifier)
		}
		if seen[p.Verifier] {
			t.Fatal("verifier repeated")
		}
		seen[p.Verifier] = true
		sum := sha256.Sum256([]byte(p.Verifier))
		if p.Challenge != base64.RawURLEncoding.EncodeToString(sum[:]) || strings.Contains(p.Challenge, "=") {
			t.Fatalf("challenge %q", p.Challenge)
		}
		if len(p.State) < 32 {
			t.Fatalf("state too short: %q", p.State)
		}
	}
}

func TestChallengeKnownVector(t *testing.T) {
	// RFC 7636, appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal(got)
	}
}

func TestAuthorizeURL(t *testing.T) {
	got := AuthorizeURL("https://app.lyrelog.com", "abc", "http://127.0.0.1:5000/callback?state=s", "my mac", "")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/cli/authorize" || u.Query().Get("challenge") != "abc" || u.Query().Get("redirect") != "http://127.0.0.1:5000/callback?state=s" || u.Query().Get("name") != "my mac" {
		t.Fatal(got)
	}
	manual := AuthorizeURL("https://app.lyrelog.com", "abc", "", "my mac", "")
	if strings.Contains(manual, "redirect") {
		t.Fatal("manual URL carries a redirect:", manual)
	}
}

func get(t *testing.T, target string) (int, string) {
	t.Helper()
	res, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestLoopbackTakesTheCodeWithTheRightState(t *testing.T) {
	l, err := Listen("the-state")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !strings.HasPrefix(l.Redirect, "http://127.0.0.1:") {
		t.Fatal(l.Redirect)
	}
	// A wrong state is refused and doesn't end the wait.
	wrong := strings.Replace(l.Redirect, "the-state", "other", 1) + "&code=evil"
	if status, _ := get(t, wrong); status != 400 {
		t.Fatalf("wrong state answered %d", status)
	}
	status, body := get(t, l.Redirect+"&code=good-code")
	if status != 200 || !strings.Contains(body, "signed in") {
		t.Fatalf("%d %s", status, body)
	}
	code, err := l.Wait(context.Background(), time.Second)
	if err != nil || code != "good-code" {
		t.Fatalf("got %q, %v", code, err)
	}
}

func TestLoopbackWithoutCodeKeepsWaiting(t *testing.T) {
	l, err := Listen("s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if status, _ := get(t, l.Redirect); status != 400 {
		t.Fatalf("no code answered %d", status)
	}
	if _, err := l.Wait(context.Background(), 50*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestLoopbackDenied(t *testing.T) {
	l, err := Listen("s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	get(t, l.Redirect+"&error=access_denied")
	if _, err := l.Wait(context.Background(), time.Second); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("got %v", err)
	}
}

func TestLoopbackTimeoutAndCancel(t *testing.T) {
	l, err := Listen("s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	start := time.Now()
	if _, err := l.Wait(context.Background(), 30*time.Millisecond); !errors.Is(err, ErrTimeout) || time.Since(start) > time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Wait(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestCanOpenBrowser(t *testing.T) {
	env := map[string]string{"SSH_CONNECTION": "1.2.3.4 22 5.6.7.8 22", "DISPLAY": ":0"}
	if CanOpenBrowser(func(k string) string { return env[k] }) {
		t.Fatal("SSH sessions should get the code to paste")
	}
}

func TestAuthorizeURLAsksToKeep(t *testing.T) {
	u, _ := url.Parse(AuthorizeURL("https://app.lyrelog.com", "abc", "", "my mac", "never"))
	if u.Query().Get("keep") != "never" {
		t.Fatal(u)
	}
	if u, _ := url.Parse(AuthorizeURL("https://app.lyrelog.com", "abc", "", "my mac", "")); u.Query().Has("keep") {
		t.Fatal("keep without --keep:", u)
	}
}
