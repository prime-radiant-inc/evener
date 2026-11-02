package hub

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestLoginURLCanonicalOrigins(t *testing.T) {
	for _, tc := range []struct{ name, public, bound, want string }{
		{"IDNA default HTTPS port", "HTTPS://BÜCHER.EXAMPLE:443/", "127.0.0.1:9180", "https://xn--bcher-kva.example/auth/token"},
		{"IPv6 nondefault port", "https://[2001:DB8::20]:9443/", "127.0.0.1:9180", "https://[2001:db8::20]:9443/auth/token"},
		{"actual IPv6 listener", "", "[::1]:9180", "http://[::1]:9180/auth/token"},
		{"fallback actual ephemeral port", "", "0.0.0.0:49318", "http://hub.test:49318/auth/token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link, err := browserLoginURL(hubendpoint.Record{PublicURL: tc.public, BoundAddr: tc.bound}, func() (string, error) { return "hub.test", nil }, "token")
			if err != nil || link != tc.want {
				t.Fatalf("login link: %q, %v; want %q", link, err, tc.want)
			}
		})
	}
}

func TestLoginURLCommandUsesRunningPublicURL(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	base, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	startConfig := append(bytes.Clone(base), []byte("public_url = \"https://running.example.test:9443\"\n")...)
	if err := os.WriteFile(path, startConfig, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, hubedge.TokenFileName), []byte(publicationToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, stop := startPublicationHub(t, path, state, io.Discard)
	defer stop()
	if report.Running.PublicURL != "https://running.example.test:9443" {
		t.Fatalf("running publication: %+v", report.Running)
	}
	edited := append(bytes.Clone(base), []byte("public_url = \"https://edited.example.test\"\n")...)
	if err := os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	if current := endpointCommand(t, path); current.PublicURL != "https://edited.example.test" || current.Running.PublicURL != "https://running.example.test:9443" {
		t.Fatalf("current and running conflated: %+v", current)
	}
	var out, stderr bytes.Buffer
	if err := runLoginURL([]string{"--config", path}, &out, &stderr, defaultMainDeps()); err != nil {
		t.Fatal(err)
	}
	if out.String() != "https://running.example.test:9443/auth/"+publicationToken+"\n" || strings.Contains(stderr.String(), publicationToken) {
		t.Fatal("retrieval followed edited config, or leaked token to stderr")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(edited, after) {
		t.Fatalf("retrieval modified selected config: %v", err)
	}
}

func TestLoginURLCommandUnreadableTokenAndUnavailable(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	tokenPath := filepath.Join(state, hubedge.TokenFileName)
	// An unreadable token path must never be created or overwritten by retrieval.
	if err := os.Mkdir(tokenPath, 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	_, stop := startPublicationHub(t, path, state, &logs)
	var out, stderr bytes.Buffer
	err := runLoginURL([]string{"--config", path}, &out, &stderr, defaultMainDeps())
	if !errors.Is(err, ErrLoginURLToken) || out.Len() != 0 || strings.Contains(stderr.String(), publicationToken) {
		t.Fatalf("unreadable token: err=%v, output bytes=%d", err, out.Len())
	}
	info, err := os.Stat(tokenPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("unreadable token path changed: %v", err)
	}
	stop()
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(publicationToken), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out.Reset()
	stderr.Reset()
	err = runLoginURL([]string{"--config", path}, &out, &stderr, defaultMainDeps())
	if !errors.Is(err, ErrLoginURLUnavailable) || out.Len() != 0 || strings.Contains(stderr.String(), publicationToken) {
		t.Fatalf("starting hub: err=%v, output bytes=%d", err, out.Len())
	}
	if err := os.WriteFile(filepath.Join(state, "hub-endpoint.json"), []byte("invalid record"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	err = runLoginURL([]string{"--config", path}, &out, &stderr, defaultMainDeps())
	if !errors.Is(err, ErrLoginURLUnavailable) || out.Len() != 0 || strings.Contains(stderr.String(), publicationToken) {
		t.Fatalf("unavailable hub: err=%v, output bytes=%d", err, out.Len())
	}
}

func TestLoginURLCommandRejectsUnexpectedArgumentsWithoutTouchingState(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	var out, stderr bytes.Buffer
	err := runLoginURL([]string{"--config", path, "--addr", "127.0.0.1:1"}, &out, &stderr, defaultMainDeps())
	if err == nil || out.Len() != 0 {
		t.Fatalf("unexpected arguments: %v, %d bytes", err, out.Len())
	}
	if _, err := os.Stat(filepath.Join(state, "hub.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected lock: %v", err)
	}
}

func TestStartupWriterClassification(t *testing.T) {
	// Unknown wrappers cannot borrow interactivity from an unrelated descriptor.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminalWriter(w) || isTerminalWriter(io.Discard) || isTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("nonterminal writer disclosed link")
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "redirect.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if isTerminalWriter(file) {
		t.Fatal("regular file disclosed link")
	}
}
