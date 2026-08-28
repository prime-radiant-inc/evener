package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubRPCDirsCreate(t *testing.T) {
	t.Run("creates nested directory and is idempotent", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "missing", "nested")
		client := newDirsCreateClient(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})

		created, err := client.DirsCreate(context.Background(), appwire.DirsCreateParams{
			Path: "  " + filepath.Join(target, "..", "nested") + "  ",
		})
		if err != nil {
			t.Fatalf("DirsCreate: %v", err)
		}
		if created.Path != target || !created.Created {
			t.Fatalf("created=%+v, want cleaned path %q and Created=true", created, target)
		}
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			t.Fatalf("created path stat: info=%v err=%v", info, err)
		}

		existing, err := client.DirsCreate(context.Background(), appwire.DirsCreateParams{Path: target})
		if err != nil {
			t.Fatalf("DirsCreate existing: %v", err)
		}
		if existing.Path != target || existing.Created {
			t.Fatalf("existing=%+v, want Created=false", existing)
		}
	})

	t.Run("expands home", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		client := newDirsCreateClient(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})

		want := filepath.Join(home, "nested")
		got, err := client.DirsCreate(context.Background(), appwire.DirsCreateParams{Path: "~/nested"})
		if err != nil {
			t.Fatalf("DirsCreate: %v", err)
		}
		if got.Path != want || !got.Created {
			t.Fatalf("got=%+v, want path %q and Created=true", got, want)
		}
	})

	t.Run("classifies invalid and conflicting paths", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "file")
		if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
		client := newDirsCreateClient(t, hubcore.WebConfig{HubStateRoot: t.TempDir()})

		for _, test := range []struct {
			name string
			path string
			code int
			text string
		}{
			{name: "required", code: appwire.CodeInvalidParams, text: "path is required"},
			{name: "absolute", path: "relative", code: appwire.CodeInvalidParams, text: "absolute path required"},
			{name: "file", path: file, code: appwire.CodeConflict, text: "a file already exists at that path"},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, err := client.DirsCreate(context.Background(), appwire.DirsCreateParams{Path: test.path})
				assertDirsCreateError(t, err, test.code, test.text)
			})
		}
	})

	t.Run("classifies mkdir failure as internal", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "new")
		var gotPath string
		var gotMode os.FileMode
		client := newDirsCreateClient(t, hubcore.WebConfig{
			HubStateRoot: t.TempDir(),
			MkdirAll: func(path string, mode os.FileMode) error {
				gotPath = path
				gotMode = mode
				return errors.New("mkdir failed")
			},
		})

		_, err := client.DirsCreate(context.Background(), appwire.DirsCreateParams{Path: target})
		assertDirsCreateError(t, err, appwire.CodeInternalError, "mkdir failed")
		if gotPath != target || gotMode != 0o755 {
			t.Fatalf("MkdirAll(%q, %#o), want (%q, %#o)", gotPath, gotMode, target, os.FileMode(0o755))
		}
		if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("target stat err=%v, want not-exist", statErr)
		}
	})
}

func newDirsCreateClient(t *testing.T, cfg hubcore.WebConfig) *appwire.Client {
	t.Helper()
	hub := newHubRPCTestServer(t, cfg)
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client
}

func assertDirsCreateError(t *testing.T, err error, code int, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected AppWire error code=%d message=%q", code, message)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error=%T %v, want appwire.WireError", err, err)
	}
	if wire.Code != code || !strings.Contains(wire.Message, message) {
		t.Fatalf("wire=%+v, want code=%d message containing %q", wire, code, message)
	}
}
