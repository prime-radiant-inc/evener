//go:build evenerfuzz

package cmdutil

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

type coverageRoundTripper func(*http.Request) (*http.Response, error)

func (f coverageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingReader) Close() error             { return nil }

type coverageAdapter string

func (a coverageAdapter) Name() string { return string(a) }
func (coverageAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (coverageAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unused")
}

// FuzzCmdutilCoverage is a deterministic union seed for dependency-bound
// cmdutil branches. All HTTP is intercepted before it reaches a transport.
func FuzzCmdutilCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		oldClient := http.DefaultClient
		oldFromEnv := newClientFromEnv
		oldFindEnvVar := findEnvVar
		t.Cleanup(func() {
			http.DefaultClient = oldClient
			newClientFromEnv = oldFromEnv
			findEnvVar = oldFindEnvVar
		})
		closeLog, err := AttachAPILogger(llm.NewClient(), t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = closeLog()
		_, _ = ResolveReasoningEffort("none", "")

		badState := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(badState, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.XDGConfigHome.Name, badState)
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(badState, "providers.toml"))
		_, _ = LoadClient("")
		newClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) { return nil, errors.New("factory failed") }
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(t.TempDir(), "providers.toml"))
		t.Setenv(envvars.EVENERProvidersConfig.Name, filepath.Join(t.TempDir(), "absent.toml"))
		_, _ = LoadClient("")
		newClientFromEnv = oldFromEnv

		validRoot := t.TempDir()
		t.Setenv(envvars.XDGConfigHome.Name, validRoot)
		t.Setenv(envvars.OllamaBaseURL.Name, "http://ollama.invalid")
		t.Setenv(envvars.OllamaHost.Name, "http://host.invalid")
		_, _ = seedConfigFromEnv()
		t.Setenv(envvars.OllamaBaseURL.Name, "")
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(validRoot, "providers.toml"))

		invalidConfig := filepath.Join(validRoot, "invalid.toml")
		if err := os.WriteFile(invalidConfig, []byte("["), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.EVENERProvidersConfig.Name, invalidConfig)
		_, _ = LoadClient("")
		_, _ = LoadClientAt(invalidConfig, "")

		cpu := filepath.Join(t.TempDir(), "cpu")
		stopCPU, err := StartCPUProfile(cpu)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = StartCPUProfile(filepath.Join(t.TempDir(), "second"))
		stopCPU()
		trace := filepath.Join(t.TempDir(), "trace")
		stopTrace, err := StartTrace(trace)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = StartTrace(filepath.Join(t.TempDir(), "second-trace"))
		stopTrace()

		t.Setenv(envvars.XDGStateHome.Name, "")
		t.Setenv(envvars.XDGConfigHome.Name, "")
		t.Setenv("HOME", "")
		_ = DefaultStateRoot()
		_ = DefaultConfigRoot()

		badSessions := filepath.Join(t.TempDir(), "sessions-file")
		if err := os.WriteFile(badSessions, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = ResolveSessionMeta(badSessions, "", true)

		credentialDir := t.TempDir()
		credentialConfig := filepath.Join(credentialDir, "providers.toml")
		if err := os.WriteFile(credentialConfig, []byte(gatewayProvidersToml("http://offline.invalid")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(credentialDir, "credentials.toml"), []byte("["), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.EVENERProvidersConfig.Name, credentialConfig)
		t.Setenv(envvars.EVENERCredentialsConfig.Name, filepath.Join(credentialDir, "credentials.toml"))
		// A corrupt credentials.toml is a load failure, not a silent skip.
		if _, err := LoadClient(""); err == nil {
			t.Fatal("a corrupt credentials.toml must fail the load")
		}
		if err := os.Remove(filepath.Join(credentialDir, "credentials.toml")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadClient(""); err != nil {
			t.Fatalf("an absent credentials.toml is an empty store: %v", err)
		}

		client := llm.NewClient()
		for _, name := range []string{"ollama", "openai", "unknown"} {
			client.Register(coverageAdapter(name))
		}
		newClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) { return client, nil }
		t.Setenv(envvars.OllamaBaseURL.Name, "http://ollama-base.invalid")
		_, _ = seedConfigFromEnv()
		t.Setenv(envvars.OllamaBaseURL.Name, "")
		t.Setenv(envvars.OllamaHost.Name, "http://ollama-host.invalid")
		_, _ = seedConfigFromEnv()
		t.Setenv(envvars.OllamaHost.Name, "")
		_, _ = seedConfigFromEnv()
		findEnvVar = func(string) (envvars.Var, bool) { return envvars.Var{}, false }
		_, _ = seedConfigFromEnv()
		findEnvVar = oldFindEnvVar

		blockedParent := filepath.Join(t.TempDir(), "parent-file")
		if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = MaterializeProvidersConfig(filepath.Join(blockedParent, "providers.toml"))
	})
}

// FuzzCmdutilScenarioReplay makes the package's deterministic behavioral
// scenarios part of native fuzz seed replay. Keeping the scenarios as named
// subtests preserves their cleanup boundaries, especially for process-global
// HTTP transports, profiling state, and injectable provider constructors.
func FuzzCmdutilScenarioReplay(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		scenarios := []struct {
			name string
			fn   func(*testing.T)
		}{
			{"user-config-xdg", TestUserConfigDirsUseXDGConfigHome},
			{"user-config-create", TestEnsureUserConfigDirsCreatesExtensionRoots},
			{"seed", TestSeedDescriptorsOnly},
			{"base-url-env", TestBaseURLEnvVar},
			{"state-linked-worktree", TestDefaultProjectStateDir_LinkedWorktreeSameAsMain},
			{"state-non-repo", TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir},
			{"materialize", TestMaterializeProvidersConfig},
			{"materialize-oauth", TestMaterializeDetectsOpenAIOAuth},
			{"list-models", TestListModelsFunc},
			{"load-live", TestLoadClient_ListsTheDeclaredInstanceLive},
			{"load-window", TestResolveProfile_TakesTheServedWindow},
			{"load-codex", TestLoadClient_CodexAllowlistIsTheOneUnservableRef},
			{"load-explicit-path", TestLoadClientAt_ReadsTheExplicitPath},
			{"load-old-schema", TestLoadClient_OldSchemaFileIsReported},
			{"api-jsonl", TestAttachAPILoggerWritesAPIJSONL},
			{"max-rounds", TestMaxRoundsToConfig},
			{"reasoning", TestResolveReasoningEffort},
			{"model-cli", TestResolveModelRef_QualifiedModelSuppliesProvider},
			{"model-env", TestResolveModelRef_EnvModelSuppliesProvider},
			{"model-bare", TestResolveModelRef_RejectsBareStartupModel},
			{"model-resume", TestResolveModelRef_ResumeMetaSuppliesBareModelProvider},
			{"resume-meta", TestResolveResumeModelRef_PersistedMetaBeatsEnv},
			{"resume-cli", TestResolveResumeModelRef_CLIOverridesPersistedMeta},
			{"resume-env", TestResolveResumeModelRef_UsesEnvWhenMetaMissing},
			{"allowed-csv", TestParseAllowedDecisions_CommaSeparated},
			{"allowed-json", TestParseAllowedDecisions_JSONArray},
			{"allowed-empty", TestParseAllowedDecisions_Empty},
			{"allowed-space", TestParseAllowedDecisions_Whitespace},
			{"slice-flag", TestStringSliceFlag},
			{"qualified-part", TestModelRefQualifiedWithMissingPart},
			{"model-missing", TestResolveModelRefNoModel},
			{"resume-missing", TestResolveResumeModelRefNoModel},
			{"git-origin", TestGitOriginURLFromDir},
			{"session-meta", TestResolveSessionMeta},
			{"state-root", TestDefaultStateRoot},
			{"config-subdirs", TestDefaultConfigRootAndSubdirs},
			{"ensure-dirs", TestEnsureUserConfigDirs},
			{"ensure-dirs-error", TestEnsureUserConfigDirsSurfacesMkdirError},
			{"cpu-profile", TestStartCPUProfile},
			{"trace", TestStartTrace},
			{"api-logger", TestAttachAPILogger},
			{"api-logger-error", TestAttachAPILoggerFailsWhenPathUnusable},
		}
		for _, scenario := range scenarios {
			t.Run(scenario.name, scenario.fn)
		}
	})
}
