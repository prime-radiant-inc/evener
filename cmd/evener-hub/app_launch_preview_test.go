package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// These consumer tests pin the directory lifecycle before sharing preview
// preparation. Dropping either cleanup path would leave a probe behind;
// removing the target or ancestor instead would destroy fixture files.
func TestLaunchPreviewRPCMissingCWDCleansProbe(t *testing.T) {
	for _, method := range []string{appwire.MethodEvenerPluginPreview, appwire.MethodEvenerSpawnSlashCatalog} {
		for _, malformed := range []bool{false, true} {
			name := "success"
			if malformed {
				name = "resolution-error"
			}
			t.Run(method+"/"+name, func(t *testing.T) {
				ancestor := t.TempDir()
				marker := filepath.Join(ancestor, "marker")
				if err := os.WriteFile(marker, []byte("ancestor-sentinel"), 0o644); err != nil {
					t.Fatal(err)
				}
				localDir := filepath.Join(ancestor, ".evener")
				if err := os.Mkdir(localDir, 0o755); err != nil {
					t.Fatal(err)
				}
				local := filepath.Join(localDir, "launch.local.toml")
				if err := os.WriteFile(local, []byte("agent = \"ancestor-only\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				launchRoot := t.TempDir()
				if malformed {
					if err := os.WriteFile(filepath.Join(launchRoot, "launch.toml"), []byte("plugin_dirs = ["), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: launchRoot})
				defer hub.Close()
				client := dialHubRPC(t, hub)
				defer client.Close()
				if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(ancestor, "new-parent", "new-session")
				var response any
				err := client.Request(context.Background(), method, map[string]string{"cwd": target}, &response)
				if malformed {
					assertWireCode(t, err, appwire.CodeInternalError)
				} else if err != nil {
					t.Fatalf("preview: %v", err)
				}
				for _, path := range []string{target, filepath.Dir(target)} {
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("preview created target path %q: %v", path, err)
					}
				}
				entries, err := os.ReadDir(ancestor)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 2 || entries[0].Name() != ".evener" || entries[1].Name() != "marker" {
					t.Fatalf("ancestor entries after preview = %v, want original .evener and marker only", entries)
				}
				for path, want := range map[string]string{marker: "ancestor-sentinel", local: "agent = \"ancestor-only\"\n"} {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != want {
						t.Fatalf("ancestor file %q = %q, err=%v, want %q", path, got, err, want)
					}
				}
			})
		}
	}
}

func TestLaunchPreviewRPCExistingMalformedGitErrorClassification(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".git"), []byte("invalid-pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	_, err := client.PluginPreview(context.Background(), appwire.PluginPreviewParams{CWD: cwd})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	var previewErr appwire.WireError
	if !errors.As(err, &previewErr) || !wireErrorInfoIs(previewErr.Data, appwire.ErrorInvalidParams) {
		t.Fatalf("plugin preview error = %v, want invalid-params shape", err)
	}
	_, err = client.SpawnSlashCatalog(context.Background(), appwire.SpawnSlashCatalogParams{CWD: cwd})
	assertWireCode(t, err, appwire.CodeInternalError)
	var slashErr appwire.WireError
	if !errors.As(err, &slashErr) || !wireErrorInfoIs(slashErr.Data, appwire.ErrorInternal) {
		t.Fatalf("slash catalog error = %v, want internal-error shape", err)
	}
}
