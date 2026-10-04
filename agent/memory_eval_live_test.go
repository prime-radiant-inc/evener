package agent

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/liveeval"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

func TestMemoryEvalSourceCopies(t *testing.T) {
	for _, tc := range []struct {
		name, value, root, want string
		set, wantErr            bool
	}{
		{name: "unset", root: "/fixture/config", want: "/fixture/config/providers.toml"},
		{name: "nonempty", value: " /fixture/exact.toml ", set: true, want: " /fixture/exact.toml "},
		{name: "present-empty", set: true, wantErr: true},
		{name: "present-whitespace", value: " \t", set: true, wantErr: true},
		{name: "unresolved-root", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := memoryEvalConfigSource(func() (string, bool) { return tc.value, tc.set }, func() string { return tc.root })
			if path != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("source=%q error=%t want=%q error=%t", path, err != nil, tc.want, tc.wantErr)
			}
		})
	}
}
func memoryEvalConfigSource(lookup func() (string, bool), root func() string) (string, error) {
	value, set := lookup()
	if set {
		if strings.TrimSpace(value) == "" {
			return "", errors.New("memory eval no user provider layer")
		}
		return value, nil
	}
	resolved := root()
	if strings.TrimSpace(resolved) == "" {
		return "", errors.New("memory eval config root unresolved")
	}
	return filepath.Join(resolved, "providers.toml"), nil
}

// Set once before TestMain replaces HOME and XDG_CONFIG_HOME. No source file
// is opened here, and ordinary startup does not even select paths.
var memoryEvalStartupSources *struct {
	config, auth string
	err          error
}

func memoryEvalCaptureSources() {
	if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) {
		return
	}
	config, auth, err := memoryEvalSelectSources()
	memoryEvalStartupSources = &struct {
		config, auth string
		err          error
	}{config, auth, err}
}

func memoryEvalDiscover() (string, string, error) {
	if memoryEvalStartupSources == nil {
		return "", "", errors.New("memory eval startup sources not captured")
	}
	s := memoryEvalStartupSources
	return s.config, s.auth, s.err
}

func memoryEvalSelectSources() (string, string, error) {
	config, err := memoryEvalConfigSource(envvars.EVENERProvidersConfig.LookupEnv, userdirs.DefaultConfigRoot)
	if err != nil {
		return "", "", err
	}
	root := openai.DefaultStateDir()
	if root == "" {
		return "", "", errors.New("memory eval auth root unresolved")
	}
	return config, openai.AuthFilePath(root, "codex-jesse-at-pr"), nil
}
func TestMemoryEvalLivePairs(t *testing.T) {
	memoryEvalLivePairs(t, liveeval.Enabled(os.Getenv(liveeval.OptInEnv)), memoryEvalDiscover)
}
func memoryEvalLivePairs(t *testing.T, enabled bool, discover func() (string, string, error)) {
	t.Helper()
	if !enabled {
		t.Skip("live memory pairs require explicit opt-in after offline review")
	}
	memoryEvalLivePairsOnHost(t, discover, sandbox.RealProber{}.Probe())
}

func memoryEvalLivePairsOnHost(t *testing.T, discover func() (string, string, error), facts sandbox.HostFacts) {
	t.Helper()
	memoryEvalRequireHost(t, true, facts)
	// Controller authorization is the external staged gate. Opt-in alone is not
	// permission for the offline implementer to invoke this test.
	runStart := time.Now()
	configSource, authSource, err := discover()
	if err != nil {
		t.Fatal("memory eval provider source unavailable, no fallback")
	}
	private := memoryEvalDisposable(t)
	config, _, err := memoryEvalCopySources(private, configSource, authSource)
	if err != nil {
		t.Fatal("memory eval selected config/auth private copy failed")
	}
	home := memoryEvalIsolateProcess(t)
	r := memoryEvalRegistry(t, private, config)
	p, err := provider.Resolve(r, memoryEvalModel)
	if err != nil {
		t.Fatal("memory eval approved model unavailable offline, no fallback")
	}
	resolved := p.Resolved()
	const endpoint = "https://chatgpt.com/backend-api/codex/responses"
	if !memoryEvalApprovedRoute(resolved) {
		t.Fatal("memory eval approved model has an uninstrumented route")
	}
	record, err := openai.LoadAuth(private, "codex-jesse-at-pr")
	if err != nil {
		t.Fatal("memory eval private auth unavailable")
	}
	p = provider.WithCheapModel(p, memoryEvalModel)
	b := &memoryEvalAdmission{runStart: runStart, authRoot: private, boundModel: true}
	b.sensitive = append(b.sensitive, resolved.Credential.Value, record.AccessToken, record.RefreshToken, record.IDToken, record.Email, record.AccountID, record.WorkspaceID)
	for _, value := range resolved.CredentialHeaders {
		b.sensitive = append(b.sensitive, value)
	}
	for _, value := range resolved.Headers {
		b.sensitive = append(b.sensitive, value)
	}
	c := llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(private))
	c.Use(b.middleware(tokenauth.ScopedCodex(private)))
	// This top-level test is serial. No process-global auth directory is mutated.
	old := responses.DefaultProtocol.Client
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	responses.DefaultProtocol.Client = &http.Client{Transport: &memoryEvalTransport{budget: b, base: transport, endpoint: endpoint}}
	defer func() { b.active.Wait(); responses.DefaultProtocol.Client = old; transport.CloseIdleConnections() }()
	evidenceRoot, err := os.MkdirTemp(os.Getenv("EVENER_SCRATCH_DIR"), "memory-eval-evidence-")
	if err != nil {
		t.Fatal("memory eval evidence root unavailable")
	}
	t.Logf("credential-free evidence: %s", evidenceRoot)
	b.evidenceRoot = evidenceRoot
	defer func() {
		memoryEvalWriteEvidence(t, b, filepath.Join(evidenceRoot, "usage.json"), b.observedUsage())
		memoryEvalWrite(t, filepath.Join(evidenceRoot, "limits.txt"), memoryEvalSnapshot(b)+"\nObserved root session usage is in stage files. Aggregate reported final-response usage is in usage.json, unfinished responses are unknown. Cost is unknown, no cited price. Cancellation does not prove billing stops.\n")
	}()
	episodes := memoryEvalRunPairs(t, b, c, p, home, nil)
	memoryEvalWriteEvidence(t, b, filepath.Join(evidenceRoot, "episodes.json"), episodes)
	for _, e := range episodes {
		for _, stage := range e.Stages {
			t.Logf("%s disabled=%t stage=%s task=%t capture=%t retrieval=%t application=%t counterevidence=%t correction=%t %s elapsed=%s", e.Pair, e.Disabled, stage.Name, stage.Task, stage.Capture, stage.Retrieval, stage.Application, stage.Counterevidence, stage.Correction, stage.Counters, stage.Elapsed)
			if !stage.Task || stage.Limitation != "" {
				t.Errorf("behavior failure, %s disabled=%t stage=%s, no episode retry", e.Pair, e.Disabled, stage.Name)
			}
			if !e.Disabled && e.Pair == "correction" && stage.Name == "B" && !stage.Correction {
				t.Errorf("correction not proven before completion, review retained evidence")
			}
		}
	}
}
func TestMemoryEvalSkipBeforeDiscovery(t *testing.T) {
	called := false
	t.Run("not-opted-in", func(t *testing.T) {
		memoryEvalLivePairs(t, false, func() (string, string, error) { called = true; return "", "", errors.New("must never discover") })
	})
	if called {
		t.Fatal("skip performed source discovery")
	}
}

func TestMemoryEvalPrivateCopies(t *testing.T) {
	source, private := t.TempDir(), t.TempDir()
	config := filepath.Join(source, "providers.toml")
	auth := filepath.Join(source, "auth", "codex-jesse-at-pr.json")
	memoryEvalWrite(t, config, "opaque-selected-config-651\n")
	memoryEvalWrite(t, auth, "opaque-selected-auth-652\n")
	memoryEvalWrite(t, filepath.Join(source, "credentials.toml"), "opaque-denied-credentials-653")
	memoryEvalWrite(t, filepath.Join(source, "auth", "other.json"), "opaque-denied-account-654")
	copiedConfig, copiedAuth, err := memoryEvalCopySources(private, config, auth)
	if err != nil {
		t.Fatal("fixture copying failed")
	}
	if copiedConfig == config || copiedAuth == auth {
		t.Fatal("source reused instead of exact private copy")
	}
	for _, pair := range [][2]string{{config, copiedConfig}, {auth, copiedAuth}} {
		a, _ := os.ReadFile(pair[0])
		b, _ := os.ReadFile(pair[1])
		if string(a) != string(b) {
			t.Fatal("copy differs")
		}
		info, err := os.Stat(pair[1])
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private file permissions")
		}
	}
	memoryEvalWrite(t, copiedAuth, "opaque-private-refresh-655")
	original, _ := os.ReadFile(auth)
	if string(original) != "opaque-selected-auth-652\n" {
		t.Fatal("private refresh mutated source")
	}
	var files []string
	filepath.WalkDir(private, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if d.IsDir() {
			info, _ := d.Info()
			if info.Mode().Perm() != 0700 {
				t.Fatal("private directory permissions")
			}
		} else {
			rel, _ := filepath.Rel(private, path)
			files = append(files, rel)
		}
		return nil
	})
	if len(files) != 2 {
		t.Fatalf("copied %v want exactly config plus one auth record", files)
	}
}

// Copy only the two selected paths, before any parsing/resolution. Never glob
// auth records, copy credentials/catalogs/history, or sync a refresh back.
func memoryEvalCopySources(private, config, auth string) (string, string, error) {
	configCopy := filepath.Join(private, "providers.toml")
	authCopy := openai.AuthFilePath(private, "codex-jesse-at-pr")
	for _, pair := range [][2]string{{config, configCopy}, {auth, authCopy}} {
		if err := os.MkdirAll(filepath.Dir(pair[1]), 0700); err != nil {
			return "", "", errors.New("private directory failed")
		}
		in, err := os.Open(pair[0])
		if err != nil {
			return "", "", errors.New("selected source unavailable")
		}
		out, err := os.OpenFile(pair[1], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			in.Close()
			return "", "", errors.New("private copy unavailable")
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		outErr := out.Close()
		if copyErr != nil || inErr != nil || outErr != nil {
			return "", "", errors.New("private copy incomplete")
		}
	}
	return configCopy, authCopy, nil
}

func memoryEvalApprovedRoute(res registry.Resolved) bool {
	const endpoint = "https://chatgpt.com/backend-api/codex/responses"
	return res.Protocol == registry.ProtocolOpenAIResponses && res.Transport.Auth == registry.AuthOAuthOpenAICodex && strings.TrimRight(res.Transport.BaseURL, "/")+res.Transport.Endpoint == endpoint && res.Transport.StreamEndpoint == res.Transport.Endpoint
}
func TestMemoryEvalRoute(t *testing.T) {
	b := &memoryEvalAdmission{}
	_, p := memoryEvalFixtureClient(t, b, &memoryEvalAdapter{})
	res := p.Resolved()
	if !memoryEvalApprovedRoute(res) {
		t.Fatal("approved fixture route refused")
	}
	for _, kind := range []string{"protocol", "auth", "endpoint", "stream"} {
		wrong := res
		switch kind {
		case "protocol":
			wrong.Protocol = registry.ProtocolOpenAIChat
		case "auth":
			wrong.Transport.Auth = registry.AuthBearer
		case "endpoint":
			wrong.Transport.BaseURL = "https://decoy.invalid"
		case "stream":
			wrong.Transport.StreamEndpoint = "/elsewhere"
		}
		if memoryEvalApprovedRoute(wrong) {
			t.Fatalf("accepted %s route mismatch", kind)
		}
	}
}

func TestMemoryEvalStartupDiscovery(t *testing.T) {
	for _, mode := range []string{"defaults", "xdg", "override", "empty", "no-opt-in"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			configRoot, stateRoot := filepath.Join(root, ".config"), filepath.Join(root, ".local", "state")
			if mode == "xdg" {
				configRoot, stateRoot = filepath.Join(root, "config-xdg"), filepath.Join(root, "state-xdg")
			}
			config := filepath.Join(configRoot, "evener", "providers.toml")
			if mode == "override" {
				config = filepath.Join(root, "selected.toml")
			}
			auth := openai.AuthFilePath(filepath.Join(stateRoot, "evener"), "codex-jesse-at-pr")
			memoryEvalWrite(t, config, "[providers.codex-jesse-at-pr]\nbase = \"openai-codex\"\n")
			memoryEvalWrite(t, auth, memoryEvalJSON(openai.AuthRecord{Version: 1, Source: openai.AuthSourceOAuth, TokenType: "Bearer", AccessToken: "opaque-startup-701", RefreshToken: "opaque-startup-refresh-702", ObtainedAt: time.Now(), Expiry: time.Now().Add(time.Hour)}))
			cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryEvalStartupProbe$", "-test.v")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				switch key {
				case "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "EVENER_PROVIDERS_CONFIG", liveeval.OptInEnv:
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "HOME="+root, "MEMORY_EVAL_STARTUP_MODE="+mode, "MEMORY_EVAL_STARTUP_CONFIG="+config, "MEMORY_EVAL_STARTUP_AUTH="+auth)
			if mode != "no-opt-in" {
				cmd.Env = append(cmd.Env, liveeval.OptInEnv+"=1")
			}
			if mode == "xdg" {
				cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+configRoot, "XDG_STATE_HOME="+stateRoot)
			}
			if mode == "override" || mode == "empty" {
				value := config
				if mode == "empty" {
					value = ""
				}
				cmd.Env = append(cmd.Env, "EVENER_PROVIDERS_CONFIG="+value)
			}
			out, err := cmd.CombinedOutput()
			t.Log(string(out))
			if err != nil {
				t.Fatalf("real TestMain startup regression: %v", err)
			}
		})
	}
}

func TestMemoryEvalStartupProbe(t *testing.T) {
	mode := os.Getenv("MEMORY_EVAL_STARTUP_MODE")
	if mode == "" {
		t.Skip("subprocess-only fixture startup probe")
	}
	if mode == "no-opt-in" {
		if memoryEvalStartupSources != nil {
			t.Fatal("ordinary startup selected source paths")
		}
		called := false
		t.Run("skip", func(t *testing.T) {
			memoryEvalLivePairs(t, false, func() (string, string, error) { called = true; return "", "", nil })
		})
		if called {
			t.Fatal("ordinary startup discovered sources")
		}
		return
	}
	config, auth, err := memoryEvalDiscover()
	if mode == "empty" {
		if err == nil {
			t.Fatal("present-empty provider override fell back")
		}
		return
	}
	if err != nil || config != os.Getenv("MEMORY_EVAL_STARTUP_CONFIG") || auth != os.Getenv("MEMORY_EVAL_STARTUP_AUTH") {
		t.Fatalf("TestMain lost original source selection: config=%q auth=%q error=%v", config, auth, err)
	}
	private := t.TempDir()
	copyConfig, copyAuth, err := memoryEvalCopySources(private, config, auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{config, copyConfig}, {auth, copyAuth}} {
		a, _ := os.ReadFile(pair[0])
		b, _ := os.ReadFile(pair[1])
		if string(a) != string(b) {
			t.Fatal("startup private copy changed bytes")
		}
		if err := os.Remove(pair[0]); err != nil {
			t.Fatal(err)
		}
	}
	r := memoryEvalRegistry(t, private, copyConfig)
	if _, err := provider.Resolve(r, memoryEvalModel); err != nil {
		t.Fatal("private-only resolution failed")
	}
	if _, err := openai.LoadAuth(private, "codex-jesse-at-pr"); err != nil {
		t.Fatal("private-only auth parsing failed")
	}
}
