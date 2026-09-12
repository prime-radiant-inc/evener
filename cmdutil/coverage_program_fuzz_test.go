//go:build evenerfuzz

package cmdutil

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

// FuzzCmdutilCoverage is a deterministic union seed for dependency-bound
// cmdutil branches, plus a fuzzed providers.toml at EVENER_PROVIDERS_CONFIG:
// LoadClient must either build a client or report an error for arbitrary
// bytes, never panic. All HTTP is intercepted before it reaches a transport.
func FuzzCmdutilCoverage(f *testing.F) {
	f.Add(uint8(0), []byte(""))
	f.Add(uint8(1), []byte("["))
	f.Add(uint8(2), []byte("default = \"gw\"\n[providers.gw]\nbase = \"openai\"\n"))
	f.Add(uint8(3), []byte("[instances.work]\ntype = \"openai\"\n"))
	f.Fuzz(func(t *testing.T, _ uint8, providersTOML []byte) {
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
		_, _ = LoadClient("")
		t.Setenv(envvars.EVENERProvidersConfig.Name, filepath.Join(t.TempDir(), "absent.toml"))
		if _, err := LoadClient(""); err != nil {
			t.Fatalf("an absent providers.toml is a valid configuration: %v", err)
		}

		validRoot := t.TempDir()
		t.Setenv(envvars.XDGConfigHome.Name, validRoot)

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
		t.Setenv(envvars.XDGStateHome.Name, t.TempDir())
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

		// The fuzzed layer: whatever bytes arrive, loading is a verdict, not
		// a crash, and a client that loads resolves its own default.
		fuzzedRoot := t.TempDir()
		fuzzedConfig := filepath.Join(fuzzedRoot, "providers.toml")
		if err := os.WriteFile(fuzzedConfig, providersTOML, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.EVENERProvidersConfig.Name, fuzzedConfig)
		t.Setenv(envvars.EVENERCredentialsConfig.Name, filepath.Join(fuzzedRoot, "credentials.toml"))
		if client, err := LoadClient(""); err == nil {
			_ = client.ProviderNames()
			_ = client.DefaultProvider()
		}
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
			{"state-linked-worktree", TestDefaultProjectStateDir_LinkedWorktreeSameAsMain},
			{"state-non-repo", TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir},
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
