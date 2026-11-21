// Package login is the CLI's sign-in: PKCE, a loopback server for the browser's redirect, and
// the code a person can paste instead (the same flow Claude Code and gh use).
package login

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
)

// PKCE is one sign-in attempt's proof: the verifier stays here, the challenge goes to the browser.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// NewPKCE makes a verifier of 86 characters (64 random bytes, base64url without padding: only
// [A-Za-z0-9-_], within RFC 7636's 43 to 128 of [A-Za-z0-9-._~]), its S256 challenge and a
// random state of 43 characters.
func NewPKCE() (PKCE, error) {
	verifier, err := random(64)
	if err != nil {
		return PKCE{}, err
	}
	state, err := random(32)
	if err != nil {
		return PKCE{}, err
	}
	return PKCE{Verifier: verifier, Challenge: Challenge(verifier), State: state}, nil
}

// Challenge is base64url-no-padding(SHA-256(verifier)).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sameState compares in constant time.
func sameState(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// AuthorizeURL is the dashboard page where the person approves this computer. With a redirect
// the page sends the browser back to the loopback server; without one it shows a code to paste.
// keep ("30d", "90d", "1y", "never", or "" for a browser's limits) is preselected on the page,
// where the person can still change it.
func AuthorizeURL(base, challenge, redirect, name, keep string) string {
	q := url.Values{}
	q.Set("challenge", challenge)
	if redirect != "" {
		q.Set("redirect", redirect)
	}
	q.Set("name", name)
	if keep != "" {
		q.Set("keep", keep)
	}
	return base + "/cli/authorize?" + q.Encode()
}
