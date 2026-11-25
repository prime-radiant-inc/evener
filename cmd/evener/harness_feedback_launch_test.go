package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestHarnessFeedbackRunAndServeLaunch(t *testing.T) {
	const (
		envWebhook  = "https://hooks.example.test/services/ENV_SECRET_1"
		flagWebhook = "https://hooks.example.test/services/FLAG_SECRET_2"
	)
	for _, entry := range []string{"run", "serve"} {
		for _, mode := range []string{"unconfigured", "env", "flags-win-over-env"} {
			t.Run(entry+"/"+mode, func(t *testing.T) {
				dir := memoryLaunchFixture(t)
				state := t.TempDir()
				envFile := filepath.Join(t.TempDir(), "env.jsonl")
				flagFile := filepath.Join(t.TempDir(), "flag.jsonl")
				t.Setenv(envvars.EVENERHarnessFeedbackWebhook.Name, "")
				t.Setenv(envvars.EVENERHarnessFeedbackCommand.Name, "")
				t.Setenv(envvars.EVENERHarnessFeedbackFile.Name, "")

				var want agent.HarnessFeedbackConfig
				args := []string{"--dir", dir, "--state-dir", state, "--model", "openai/gpt-test"}
				switch mode {
				case "env":
					t.Setenv(envvars.EVENERHarnessFeedbackWebhook.Name, envWebhook)
					t.Setenv(envvars.EVENERHarnessFeedbackCommand.Name, "cat >/dev/null")
					t.Setenv(envvars.EVENERHarnessFeedbackFile.Name, envFile)
					want = agent.HarnessFeedbackConfig{
						WebhookURL: envWebhook,
						Command:    "cat >/dev/null",
						FilePath:   envFile,
					}
				case "flags-win-over-env":
					t.Setenv(envvars.EVENERHarnessFeedbackWebhook.Name, envWebhook)
					t.Setenv(envvars.EVENERHarnessFeedbackCommand.Name, "env-cmd")
					t.Setenv(envvars.EVENERHarnessFeedbackFile.Name, envFile)
					args = append(args,
						"--harness-feedback-webhook", flagWebhook,
						"--harness-feedback-command", "flag-cmd",
						"--harness-feedback-file", flagFile,
					)
					want = agent.HarnessFeedbackConfig{
						WebhookURL: flagWebhook,
						Command:    "flag-cmd",
						FilePath:   flagFile,
					}
				}

				stop := errors.New("fixture constructor reached")
				captured := false
				capture := func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
					defer env.Cleanup()
					captured = true
					if cfg.HarnessFeedback != want {
						t.Errorf("HarnessFeedback = %+v, want %+v", cfg.HarnessFeedback, want)
					}
					return nil, stop
				}

				var err error
				if entry == "serve" {
					installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
					deps := defaultServeDeps()
					deps.newSession = capture
					err = runServeWithDeps(append(args, "--run-dir", t.TempDir()), deps)
				} else {
					installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
					old := runNewSession
					runNewSession = capture
					t.Cleanup(func() { runNewSession = old })
					deps := defaultMainDeps()
					deps.args = args
					deps.args = append(deps.args, "fixture input")
					deps.stdin, deps.stdout, deps.stderr = strings.NewReader(""), io.Discard, io.Discard
					deps.stdinMode = func() (os.FileMode, error) { return os.ModeCharDevice, nil }
					deps.exit = func(int) {}
					deps.run = func(ctx context.Context, cfg runConfig) error { err = run(ctx, cfg); return err }
					mainWithDeps(deps)
				}
				if !captured || !errors.Is(err, stop) {
					t.Fatalf("captured=%t err=%v", captured, err)
				}
			})
		}
	}
}

func TestHarnessFeedbackRunAndServeResumeLaunch(t *testing.T) {
	const envWebhook = "https://hooks.example.test/services/RESUME_SECRET_3"
	for _, entry := range []string{"run", "serve"} {
		t.Run(entry, func(t *testing.T) {
			dir := memoryLaunchFixture(t)
			state := t.TempDir()
			id := identifier.MustNewSessionID()
			if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-test"}); err != nil {
				t.Fatal(err)
			}
			fbFile := filepath.Join(t.TempDir(), "resume.jsonl")
			t.Setenv(envvars.EVENERHarnessFeedbackWebhook.Name, envWebhook)
			t.Setenv(envvars.EVENERHarnessFeedbackCommand.Name, "")
			t.Setenv(envvars.EVENERHarnessFeedbackFile.Name, fbFile)

			want := agent.HarnessFeedbackConfig{
				WebhookURL: envWebhook,
				FilePath:   fbFile,
			}
			stop := errors.New("fixture resume reached")
			captured := false
			capture := func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, _ schema.SessionMeta, cfg agent.RestoreSessionConfig) (*agent.Session, error) {
				defer env.Cleanup()
				captured = true
				if cfg.HarnessFeedback != want {
					t.Errorf("resume HarnessFeedback = %+v, want %+v", cfg.HarnessFeedback, want)
				}
				return nil, stop
			}
			args := []string{"--dir", dir, "--state-dir", state, "--resume", id}
			var err error
			if entry == "serve" {
				installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
				deps := defaultServeDeps()
				deps.restoreSession = capture
				err = runServeWithDeps(append(args, "--run-dir", t.TempDir()), deps)
			} else {
				installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
				old := runRestoreSession
				runRestoreSession = capture
				t.Cleanup(func() { runRestoreSession = old })
				deps := defaultMainDeps()
				deps.args = args
				deps.stdin, deps.stdout, deps.stderr = strings.NewReader(""), io.Discard, io.Discard
				deps.stdinMode = func() (os.FileMode, error) { return os.ModeCharDevice, nil }
				deps.exit = func(int) {}
				deps.run = func(ctx context.Context, cfg runConfig) error { err = run(ctx, cfg); return err }
				mainWithDeps(deps)
			}
			if !captured || !errors.Is(err, stop) {
				t.Fatalf("captured=%t err=%v", captured, err)
			}
		})
	}
}

func TestResolveHarnessFeedbackConfigRejectsBadWebhookWithoutLeak(t *testing.T) {
	const secret = "SECRET_BAD_URL_TOKEN_994"
	t.Setenv(envvars.EVENERHarnessFeedbackWebhook.Name, "")
	t.Setenv(envvars.EVENERHarnessFeedbackCommand.Name, "")
	t.Setenv(envvars.EVENERHarnessFeedbackFile.Name, "")

	_, err := resolveHarnessFeedbackConfig("ftp://example.com/"+secret, "", "")
	if err == nil {
		t.Fatal("expected error for unsupported webhook scheme")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked secret: %v", err)
	}
}
