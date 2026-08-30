package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// noticeStateRoot plants the two things spec §9.5 says the user must be told
// about at startup: a catalog cache that will not parse, and an OAuth record
// under a name that is not a Codex instance. It returns the state root.
func noticeStateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	if err := os.MkdirAll(catalog, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalog, "models.dev.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(registry.Meta{FetchedAt: time.Now(), Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalog, "models.dev.meta.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(root, "auth")
	if err := os.MkdirAll(auth, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(auth, "left-behind.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// noticeClient loads a registry against that state root — with the cache left
// on, since the corrupt cache is the point — and returns a client whose one
// instance is a mute scripted adapter.
func noticeClient(t *testing.T, stateRoot string) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithNoUserLayer(),
		registry.WithStateRoot(stateRoot),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if len(r.Warnings()) == 0 || len(r.StrayOAuthRecords()) == 0 {
		t.Fatalf("fixture produced no notices: warnings=%v stray=%v", r.Warnings(), r.StrayOAuthRecords())
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(&scriptedProvider{name: "openai"})
	return client
}

// assertNotices checks that every registry notice reached out as a
// "warning: …" line.
func assertNotices(t *testing.T, out string, client *llm.Client) {
	t.Helper()
	r := client.Registry()
	for _, want := range append(r.Warnings(), r.StrayOAuthRecords()...) {
		if !strings.Contains(out, "warning: "+want) {
			t.Fatalf("startup output missing %q (spec §9.5)\ngot:\n%s", want, out)
		}
	}
}

// TestRunPrintsRegistryNoticesToItsOwnStderr pins that `evener run` announces
// the registry's load warnings and stray OAuth records, and that it writes
// them to the injected stderr rather than the process's.
func TestRunPrintsRegistryNoticesToItsOwnStderr(t *testing.T) {
	client := noticeClient(t, noticeStateRoot(t))
	old := runLoadClient
	t.Cleanup(func() { runLoadClient = old })
	runLoadClient = func(string) (*llm.Client, error) { return client, nil }

	var stdout, stderr bytes.Buffer
	// The run fails later (no prompt reaches a model here); the notices are
	// printed the moment the client loads, which is what this pins.
	_ = run(context.Background(), runConfig{
		prompt: "hi", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, stdout: &stdout, stderr: &stderr,
	})
	assertNotices(t, stderr.String(), client)
}

// TestServePrintsRegistryNoticesToItsWarningsWriter pins the same for the
// daemon, through the real client-construction path rather than the helper.
func TestServePrintsRegistryNoticesToItsWarningsWriter(t *testing.T) {
	client := noticeClient(t, noticeStateRoot(t))
	old := serveLoadClient
	t.Cleanup(func() { serveLoadClient = old })
	serveLoadClient = func(string) (*llm.Client, error) { return client, nil }

	var warnings bytes.Buffer
	got, closeClient, err := newUnloggedServeLLMClient(t.TempDir(), &warnings)
	if err != nil {
		t.Fatalf("newUnloggedServeLLMClient: %v", err)
	}
	defer closeClient() //nolint:errcheck
	if got != client {
		t.Fatal("serve did not return the loaded client")
	}
	assertNotices(t, warnings.String(), client)
}

// A nil writer is not a crash, and a nil client prints nothing.
func TestPrintRegistryNoticesGuards(t *testing.T) {
	client := noticeClient(t, noticeStateRoot(t))
	printRegistryNotices(nil, client)
	var out bytes.Buffer
	printRegistryNotices(&out, nil)
	if out.Len() != 0 {
		t.Fatalf("a nil client printed %q", out.String())
	}
	printRegistryNotices(io.Discard, client)
}
