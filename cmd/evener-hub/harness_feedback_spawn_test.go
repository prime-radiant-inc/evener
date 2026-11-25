package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/envvars"
)

func TestLoadConfig_HarnessFeedback(t *testing.T) {
	t.Run("absent defaults empty", func(t *testing.T) {
		cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml"))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.HarnessFeedbackWebhook != "" || cfg.HarnessFeedbackCommand != "" || cfg.HarnessFeedbackFile != "" {
			t.Fatalf("unexpected non-empty defaults: %+v", cfg)
		}
	})

	t.Run("decodes and trims all three sinks", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hub.toml")
		body := `
harness_feedback_webhook = "  https://hooks.example.test/services/T1/B1/TOK1  "
harness_feedback_command = "  cat >/dev/null  "
harness_feedback_file = "  /var/tmp/evener-feedback.jsonl  "
`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.HarnessFeedbackWebhook != "https://hooks.example.test/services/T1/B1/TOK1" {
			t.Errorf("HarnessFeedbackWebhook = %q", cfg.HarnessFeedbackWebhook)
		}
		if cfg.HarnessFeedbackCommand != "cat >/dev/null" {
			t.Errorf("HarnessFeedbackCommand = %q", cfg.HarnessFeedbackCommand)
		}
		if cfg.HarnessFeedbackFile != "/var/tmp/evener-feedback.jsonl" {
			t.Errorf("HarnessFeedbackFile = %q", cfg.HarnessFeedbackFile)
		}
		// Host rewrite must preserve the operator's feedback settings.
		if err := writeHubTOMLHosts(path, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
			t.Fatalf("writeHubTOMLHosts: %v", err)
		}
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig after rewrite: %v", err)
		}
		if reloaded.HarnessFeedbackWebhook != cfg.HarnessFeedbackWebhook ||
			reloaded.HarnessFeedbackCommand != cfg.HarnessFeedbackCommand ||
			reloaded.HarnessFeedbackFile != cfg.HarnessFeedbackFile {
			t.Fatalf("rewrite dropped harness feedback settings: got %+v", reloaded)
		}
	})

	t.Run("rejects invalid webhook scheme without leaking secret", func(t *testing.T) {
		const secret = "SECRET_HUB_TOML_TOKEN_882"
		path := filepath.Join(t.TempDir(), "hub.toml")
		if err := os.WriteFile(path, []byte("harness_feedback_webhook = \"ftp://hooks.example.test/"+secret+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path)
		if err == nil {
			t.Fatal("expected error for unsupported webhook scheme")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("hub.toml validation error leaked secret: %v", err)
		}
	})
}

func TestHubSpawnerSpawnAndResumeHarnessFeedback(t *testing.T) {
	const secretWebhook = "https://hooks.example.test/services/T/B/HUB_SPAWN_SECRET_661"
	for _, op := range []string{"spawn", "resume"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			runDir := filepath.Join(dir, "run")
			argsOut := filepath.Join(dir, "args.txt")
			envOut := filepath.Join(dir, "env.txt")
			t.Setenv("ARGS_OUT", argsOut)
			t.Setenv("ENV_OUT", envOut)
			t.Setenv(envvars.EVENERHarnessFeedbackWebhook.Name, "")
			t.Setenv(envvars.EVENERHarnessFeedbackCommand.Name, "")
			t.Setenv(envvars.EVENERHarnessFeedbackFile.Name, "")

			bin := filepath.Join(dir, "fake-evener")
			script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"evener-appwire-v7","launch_flags":["api-log"]}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s\n' "$@" > "$ARGS_OUT"
  printf 'WEBHOOK=%s\nCOMMAND=%s\nFILE=%s\n' "$EVENER_HARNESS_FEEDBACK_WEBHOOK" "$EVENER_HARNESS_FEEDBACK_COMMAND" "$EVENER_HARNESS_FEEDBACK_FILE" > "$ENV_OUT"
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
			writeFakeEvener(t, bin, script)

			cfg := DefaultConfig()
			cfg.SpawnTimeout = 2 * time.Second
			cfg.HarnessFeedbackWebhook = secretWebhook
			cfg.HarnessFeedbackCommand = "cat >/dev/null"
			cfg.HarnessFeedbackFile = filepath.Join(dir, "fb.jsonl")
			spawner := HubSpawner{Cfg: cfg, EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}

			if op == "spawn" {
				if _, err := spawner.Spawn(context.Background(), hubcore.SpawnRequest{
					Resolved:   launchconfig.Resolved{Effective: launchconfig.Layer{Model: "ollama/test"}},
					WorkingDir: dir,
					Provider:   "ollama",
				}); err != nil {
					t.Fatalf("Spawn: %v", err)
				}
			} else {
				if _, err := spawner.Resume(context.Background(), hubcore.ResumeRequest{
					SessionID:  hubtest.SessionID(t),
					WorkingDir: dir,
					StateDir:   filepath.Join(dir, "state"),
				}); err != nil {
					t.Fatalf("Resume: %v", err)
				}
			}

			argsData, err := os.ReadFile(argsOut)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			if strings.Contains(string(argsData), secretWebhook) || strings.Contains(string(argsData), "HUB_SPAWN_SECRET_661") {
				t.Fatalf("daemon argv leaked webhook secret:\n%s", argsData)
			}

			envData, err := os.ReadFile(envOut)
			if err != nil {
				t.Fatalf("read env: %v", err)
			}
			wantLines := []string{
				"WEBHOOK=" + secretWebhook,
				"COMMAND=cat >/dev/null",
				"FILE=" + cfg.HarnessFeedbackFile,
			}
			for _, want := range wantLines {
				if !strings.Contains(string(envData), want) {
					t.Errorf("daemon env missing %q\ngot:\n%s", want, envData)
				}
			}
		})
	}
}
