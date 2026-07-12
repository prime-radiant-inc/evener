//go:build serffuzz

package cmdutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
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
		oldRaw := apiRawBodyEnabled
		oldFromEnv := newClientFromEnv
		oldAvailable := newClientFromAvailableProviders
		oldFindEnvVar := findEnvVar
		oldLoadCredentialStore := loadCredentialStore
		oldLookupProviderEnv := lookupProviderEnv
		t.Cleanup(func() {
			http.DefaultClient = oldClient
			apiRawBodyEnabled = oldRaw
			newClientFromEnv = oldFromEnv
			newClientFromAvailableProviders = oldAvailable
			findEnvVar = oldFindEnvVar
			loadCredentialStore = oldLoadCredentialStore
			lookupProviderEnv = oldLookupProviderEnv
		})
		apiRawBodyEnabled = func() bool { return true }
		closeLog, err := AttachAPILogger(llm.NewClient(), t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = closeLog()
		_, _ = ResolveReasoningEffort("none", "")

		responses := []struct {
			status int
			body   io.ReadCloser
			err    error
		}{
			{err: errors.New("offline")},
			{status: http.StatusUnauthorized, body: io.NopCloser(strings.NewReader("no"))},
			{status: http.StatusOK, body: failingReader{}},
			{status: http.StatusOK, body: io.NopCloser(strings.NewReader("{"))},
			{status: http.StatusOK, body: io.NopCloser(strings.NewReader(`{"data":[{"id":"other","context_length":1}]}`))},
			{status: http.StatusOK, body: io.NopCloser(strings.NewReader(`{"data":[{"id":"wanted","context_length":42}]}`))},
		}
		for _, response := range responses {
			response := response
			http.DefaultClient = &http.Client{Transport: coverageRoundTripper(func(req *http.Request) (*http.Response, error) {
				if response.err != nil {
					return nil, response.err
				}
				return &http.Response{StatusCode: response.status, Body: response.body, Header: make(http.Header)}, nil
			})}
			_ = queryModelContextWindow("kimi", "wanted", "https://offline.invalid", "key", map[string]string{"X-Test": "yes"})
		}
		for _, tc := range []struct {
			provider, base, key string
			headers             map[string]string
		}{
			{"ollama", "x", "k", nil},
			{"openai-compatible", "", "", nil},
			{"openai-compatible", "://bad", "", nil},
			{"openrouter", "", "", nil},
			{"openrouter", "", "", map[string]string{"Authorization": "token"}},
		} {
			_ = queryModelContextWindow(tc.provider, "m", tc.base, tc.key, tc.headers)
		}
		t.Setenv(envvars.OpenRouterAPIKey.Name, "key")
		_ = queryModelContextWindow("openrouter", "m", "https://offline.invalid", "", nil)

		t.Setenv("CMDUTIL_COVERAGE_KEY", "secret")
		cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
			Name: "custom", Type: "openai", APIStyle: "chat-completions", BaseURL: "https://offline.invalid",
			APIKey: "$CMDUTIL_COVERAGE_KEY", Models: map[string]providercfg.ModelConfig{"m": {ContextWindow: 7}},
			Headers: map[string]string{"Authorization": "$CMDUTIL_MISSING", "X-Bad": "$CMDUTIL_MISSING", "X-Good": "ok"},
		}}}
		_, _, _, _ = instanceEndpoint(cfg, "custom")
		_ = instanceConfiguresContextWindow(cfg, "missing", "m")
		if _, err := ResolveProfileWithLiveWindow(cfg, "missing/m"); err == nil {
			t.Fatal("missing instance resolved")
		}

		badState := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(badState, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.SERFStateDir.Name, badState)
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(badState, "providers.toml"))
		_, _, _ = LoadProviderConfig()
		_, _, _, _ = LoadClient()
		newClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) { return nil, errors.New("factory failed") }
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(t.TempDir(), "providers.toml"))
		t.Setenv(envvars.SERFProvidersConfig.Name, filepath.Join(t.TempDir(), "absent.toml"))
		_, _, _ = LoadProviderConfig()
		newClientFromEnv = oldFromEnv
		newClientFromAvailableProviders = func(providercfg.Config, ...llm.EnvOption) (*llm.Client, []error, error) {
			return nil, nil, errors.New("adapter failed")
		}
		_, _, _, _ = LoadClient()
		newClientFromAvailableProviders = oldAvailable

		validRoot := t.TempDir()
		t.Setenv(envvars.SERFStateDir.Name, validRoot)
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
		t.Setenv(envvars.SERFProvidersConfig.Name, invalidConfig)
		_, _, _ = LoadProviderConfig()
		_, _, _, _ = LoadClient()

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

		t.Setenv(envvars.SERFStateDir.Name, "")
		t.Setenv(envvars.XDGConfigHome.Name, "")
		t.Setenv("HOME", "")
		_ = DefaultStateRoot()
		_ = DefaultConfigRoot()

		badSessions := filepath.Join(t.TempDir(), "sessions-file")
		if err := os.WriteFile(badSessions, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = ResolveSessionMeta(badSessions, "", true)

		lookupProviderEnv = func(string) (envvars.ProviderEnv, bool) { return envvars.ProviderEnv{}, false }
		_ = queryModelContextWindowImpl("kimi", "m", "", "", map[string]string{"X-Auth": "token"})
		lookupProviderEnv = oldLookupProviderEnv

		credentialDir := t.TempDir()
		credentialConfig := filepath.Join(credentialDir, "providers.toml")
		if err := os.WriteFile(credentialConfig, []byte(validProvidersToml), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(credentialDir, "credentials.toml"), []byte("["), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.SERFProvidersConfig.Name, credentialConfig)
		loadCredentialStore = func(string) (*credentials.Store, error) { return nil, errors.New("load failed") }
		_, _, _ = LoadProviderConfig()
		_, _, _, _ = LoadClient()
		loadCredentialStore = oldLoadCredentialStore
		if err := os.WriteFile(credentialConfig, []byte(validProvidersToml), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(credentialDir, "credentials.toml")); err != nil {
			t.Fatal(err)
		}
		newClientFromAvailableProviders = func(providercfg.Config, ...llm.EnvOption) (*llm.Client, []error, error) {
			return nil, nil, errors.New("adapter failed")
		}
		_, _, _, _ = LoadClient()
		newClientFromAvailableProviders = oldAvailable

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
			{"load-valid", TestLoadClient_WithValidConfig},
			{"load-unused", TestLoadClient_SkipsUninitializedUnusedProvider},
			{"load-seed", TestLoadClient_NoFile_SeedsInMemory},
			{"load-corrupt", TestLoadClient_CorruptFile_ReturnsError},
			{"load-default-path", TestLoadClient_DefaultPath_UsedWhenEnvNotSet},
			{"resolve-config", TestLoadClient_ResolverPicksConfig_WhenHasConfig},
			{"resolve-seeded", TestLoadClient_ResolverPicksConfig_WhenSeeded},
			{"resolve-always-config", TestBuildResolveProfile_AlwaysUsesConfig},
			{"load-inject", TestLoadClientSeedsInMemoryAndInjects},
			{"load-auth-header", TestLoadProviderConfig_SkipsInjectionForAuthorizationHeaderInstances},
			{"load-noncompat-auth", TestLoadProviderConfig_NonCompatTypesStillInjectDespiteAuthHeader},
			{"load-custom-gateway", TestLoadProviderConfig_NoTypeEnvKeyForCustomGateways},
			{"list-models", TestListModelsFunc},
			{"api-jsonl", TestAttachAPILoggerWritesAPIJSONL},
			{"api-raw", TestAttachAPILoggerEnablesRawWhenProcessEnvSet},
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
			{"compat-tags", TestIsOpenAICompatTag},
			{"live-window", TestResolveProfileWithLiveWindow_OpenAICompatAppliesLiveWindow},
			{"live-window-zero", TestResolveProfileWithLiveWindow_FallsBackWhenLookupZero},
			{"live-window-noncompat", TestResolveProfileWithLiveWindow_NonCompatSkipsLookup},
			{"provider-profile", TestResolveProfileForProvider_NetworkFree},
			{"provider-unknown", TestResolveProfileForProvider_UnknownProvider},
			{"slice-flag", TestStringSliceFlag},
			{"query-instance", TestQueryModelContextWindow_UsesInstanceBaseURL},
			{"query-user-agent", TestQueryModelContextWindow_NonKimiOmitsCodingUserAgent},
			{"configured-window", TestResolveProfileWithLiveWindow_ConfiguredWindowSkipsLiveOverride},
			{"query-openai-compat", TestQueryModelContextWindow_OpenAICompatibleInstance},
			{"query-header-auth", TestQueryModelContextWindow_HeaderAuthenticatedGateway},
			{"query-env-header", TestQueryModelContextWindow_EnvKeyDoesNotClobberAuthHeader},
			{"unresolved-auth", TestResolveProfileWithLiveWindow_UnresolvableAuthSkipsProbe},
			{"qualified-part", TestModelRefQualifiedWithMissingPart},
			{"model-missing", TestResolveModelRefNoModel},
			{"resume-missing", TestResolveResumeModelRefNoModel},
			{"instance-endpoint", TestInstanceEndpoint},
			{"git-origin", TestGitOriginURLFromDir},
			{"session-meta", TestResolveSessionMeta},
			{"state-root", TestDefaultStateRoot},
			{"config-subdirs", TestDefaultConfigRootAndSubdirs},
			{"ensure-dirs", TestEnsureUserConfigDirs},
			{"ensure-dirs-error", TestEnsureUserConfigDirsSurfacesMkdirError},
			{"cpu-profile", TestStartCPUProfile},
			{"trace", TestStartTrace},
			{"api-logger", TestAttachAPILogger},
			{"api-logger-error", TestAttachAPILoggerDegradesWhenPathUnusable},
		}
		for _, scenario := range scenarios {
			t.Run(scenario.name, scenario.fn)
		}
	})
}
