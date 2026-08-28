package hub

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

func TestPrintHubEnvVars(t *testing.T) {
	var buf bytes.Buffer
	printHubEnvVars(&buf)
	out := buf.String()

	// Each documented var must appear on its own line alongside a
	// non-empty Summary; otherwise dropping the description from the
	// format string would silently pass.
	wantSummary := map[string]string{
		"EVENER_PROVIDERS_CONFIG": "Path to providers.toml.",
		"EVENER_STATE_DIR":        "Overrides the per-invocation project/session state directory",
		"OPENAI_API_KEY":          "OpenAI API key.",
		"ANTHROPIC_API_KEY":       "Anthropic API key.",
		"GEMINI_API_KEY":          "Google Gemini API key; checked before GOOGLE_API_KEY.",
		"GOOGLE_API_KEY":          "Google Gemini API key fallback.",
		"OPENROUTER_API_KEY":      "OpenRouter API key.",
	}

	if !strings.Contains(out, "<ID>_API_KEY / <ID>_BASE_URL") {
		t.Fatalf("hub env help does not point at per-instance provider vars: %s", out)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for name, summary := range wantSummary {
		var line string
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), name) {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("output missing %q:\n%s", name, out)
			continue
		}
		if !strings.Contains(line, summary) {
			t.Errorf("line for %q missing summary %q: got %q", name, summary, line)
		}
		// The description must follow the name, not just appear somewhere
		// on the line: trimming the name must leave non-empty help text.
		_, after, _ := strings.Cut(line, name)
		rest := strings.TrimSpace(after)
		if rest == "" {
			t.Errorf("line for %q has no description text: got %q", name, line)
		}
	}
}

func TestCurrentExecutable(t *testing.T) {
	// The documented contract is to prefer os.Executable(), which always
	// returns an absolute path. Verify absoluteness so that a mutation
	// that drops the os.Executable() branch and falls back to os.Args[0]
	// (which may be relative) is detected.
	exe := currentExecutable()
	if exe == "" {
		t.Fatal("currentExecutable() returned empty string")
	}
	if !filepath.IsAbs(exe) {
		t.Fatalf("currentExecutable() = %q, want an absolute path (os.Executable() preference)", exe)
	}
}

func TestRunMainHelpReturnsNil(t *testing.T) {
	var stderr bytes.Buffer
	err := runMain([]string{"--help"}, &stderr, defaultMainDeps())
	if err != nil {
		t.Fatalf("runMain(--help) err = %v, want nil", err)
	}
	if !strings.Contains(stderr.String(), "Usage: evener-hub") {
		t.Fatalf("help output missing usage:\n%s", stderr.String())
	}
}

func TestParseHubOptionsAcceptsAppWireTracePath(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseHubOptions([]string{"-appwire-trace", "/tmp/hub-appwire.jsonl"}, &stderr)
	if err != nil {
		t.Fatalf("parseHubOptions: %v, stderr=%s", err, stderr.String())
	}
	if opts.appwireTrace != "/tmp/hub-appwire.jsonl" {
		t.Fatalf("appwire trace path = %q, want /tmp/hub-appwire.jsonl", opts.appwireTrace)
	}
}

func newTraceMainTestDeps(t *testing.T) (string, Config, mainDeps) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("EVENER_PROVIDERS_CONFIG", filepath.Join(root, "config", "evener", "providers.toml"))
	t.Setenv("EVENER_CREDENTIALS_CONFIG", filepath.Join(root, "config", "evener", "credentials.toml"))

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.RunDir = filepath.Join(root, "run")
	cfg.StateGlob = filepath.Join(root, "projects", "*")
	cfg.PastIndexDB = filepath.Join(root, "hub", "index.db")
	cfg.HubStateRoot = filepath.Join(root, "hub")
	cfg.PluginAutoUpgrade = false
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatalf("create hub state root: %v", err)
	}

	ctx := t.Context()
	deps := mainDeps{
		loadRegistry:    hermeticRegistryLoader,
		loadConfig:      func(string) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(context.Context, hubHTTPServer, hubShutdowner) error { return nil },
	}
	return root, cfg, deps
}

func TestRunMainCreatesAppWireTraceAndWarnsAboutRawPayloads(t *testing.T) {
	root, cfg, deps := newTraceMainTestDeps(t)

	tracePath := filepath.Join(root, "hub-appwire.jsonl")
	var stderr bytes.Buffer
	if err := runMain([]string{"-appwire-trace", tracePath, "-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	info, err := os.Stat(tracePath)
	if err != nil {
		t.Fatalf("stat AppWire trace: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("trace permissions = %04o, want 0600", got)
	}
	if output := stderr.String(); !strings.Contains(output, tracePath) || !strings.Contains(output, "raw") || !strings.Contains(output, "sensitive") {
		t.Errorf("trace startup diagnostic must name the path and warn that raw payloads are sensitive:\n%s", output)
	}
}

func TestRunMainAppWireTraceCapturesRPCConnection(t *testing.T) {
	root, cfg, deps := newTraceMainTestDeps(t)
	ctx := t.Context()
	var web *WebServer
	var transport *appwire.WSTransport
	var rpcServer *httptest.Server
	serveErr := errors.New("serve failed")
	deps.afterWeb = func(created *WebServer) { web = created }
	deps.serve = func(context.Context, hubHTTPServer, hubShutdowner) error {
		if web == nil {
			t.Fatal("serve reached before WebServer construction")
		}
		rpcServer = httptest.NewServer(http.HandlerFunc(web.appRPC.ServeWebSocket))
		var err error
		transport, err = appwire.DialWebSocket(ctx, "ws"+rpcServer.URL[len("http"):], rpcServer.Client())
		if err != nil {
			rpcServer.Close()
			t.Fatalf("dial traced RPC: %v", err)
		}
		client := appwire.NewClient(transport)
		client.Start(ctx)
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			transport.Close() //nolint:errcheck // failure cleanup
			rpcServer.Close()
			t.Fatalf("initialize traced RPC: %v", err)
		}
		return serveErr
	}

	tracePath := filepath.Join(root, "hub-appwire.jsonl")
	var stderr bytes.Buffer
	if err := runMain([]string{"-appwire-trace", tracePath, "-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); !errors.Is(err, serveErr) {
		t.Fatalf("runMain error = %v, want %v; stderr=%s", err, serveErr, stderr.String())
	}
	transport.Close() //nolint:errcheck // the server-side drain may already have closed it
	rpcServer.Close()
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read AppWire trace: %v", err)
	}
	output := string(data)
	for _, want := range []string{`"event":"open"`, `"event":"close"`, `"direction":"browser_to_hub"`, `"direction":"hub_to_browser"`, `initialize`} {
		if !strings.Contains(output, want) {
			t.Errorf("AppWire trace missing %q:\n%s", want, output)
		}
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if got := lines[len(lines)-1]; !strings.Contains(got, `"event":"close"`) {
		t.Fatalf("final trace record = %s, want close after serve failure", got)
	}
}

func TestPrintVersionInfo(t *testing.T) {
	var buf bytes.Buffer
	err := printVersionInfo(&buf)
	if err != nil {
		t.Fatalf("printVersionInfo err = %v, want nil", err)
	}
	output := buf.String()
	if !strings.Contains(output, "evener-hub version:") {
		t.Fatalf("output missing version label: %q", output)
	}
	if !strings.Contains(output, "frontend hash:") {
		t.Fatalf("output missing frontend hash label: %q", output)
	}
}

type failingVersionWriter struct {
	err error
}

func (w failingVersionWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPrintVersionInfoReturnsWriteError(t *testing.T) {
	want := errors.New("write failed")
	err := printVersionInfo(failingVersionWriter{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("printVersionInfo error = %v, want %v", err, want)
	}
}

func TestRunMainVersionFlag(t *testing.T) {
	var stderr bytes.Buffer
	err := runMain([]string{"--version"}, &stderr, defaultMainDeps())
	if err != nil {
		t.Fatalf("runMain(--version) err = %v, want nil", err)
	}
	output := stderr.String()
	if !strings.Contains(output, "evener-hub version:") {
		t.Fatalf("version output missing label: %q", output)
	}
}

func TestResolveEvenerBinaryPath(t *testing.T) {
	t.Run("explicit wins", func(t *testing.T) {
		got := resolveEvenerBinaryPath("/usr/bin/evener", "", nil)
		if got != "/usr/bin/evener" {
			t.Fatalf("explicit = %q, want /usr/bin/evener", got)
		}
	})

	t.Run("sibling resolution", func(t *testing.T) {
		dir := t.TempDir()
		// resolveEvenerBinaryPath resolves symlinks in the executable's
		// directory; on macOS t.TempDir() is under /var, a symlink to
		// /private/var, so the expectation must use the resolved form.
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		evenerPath := filepath.Join(dir, "evener")
		if err := os.WriteFile(evenerPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		// currentExecutable is the evener binary itself (the hub runs as
		// `evener hub`, so the running binary is `evener`).
		got := resolveEvenerBinaryPath("", evenerPath, func(string) (string, error) {
			return "", errors.New("should not call lookPath")
		})
		if got != evenerPath {
			t.Fatalf("sibling resolution = %q, want %q", got, evenerPath)
		}
	})

	t.Run("PATH resolution", func(t *testing.T) {
		got := resolveEvenerBinaryPath("", "/no/such/hub", func(name string) (string, error) {
			if name != "evener" {
				t.Fatalf("lookPath called with %q, want evener", name)
			}
			return "/usr/local/bin/evener", nil
		})
		if got != "/usr/local/bin/evener" {
			t.Fatalf("PATH resolution = %q, want /usr/local/bin/evener", got)
		}
	})

	t.Run("lookPath error returns empty", func(t *testing.T) {
		got := resolveEvenerBinaryPath("", "/no/such/hub", func(string) (string, error) {
			return "", errors.New("not found")
		})
		if got != "" {
			t.Fatalf("lookPath error = %q, want empty", got)
		}
	})

	t.Run("nil lookPath uses exec.LookPath", func(t *testing.T) {
		// Create a temp directory with a "evener" binary and put it on PATH.
		bindir := t.TempDir()
		evenerPath := filepath.Join(bindir, "evener")
		if err := os.WriteFile(evenerPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldPath := os.Getenv("PATH")
		defer os.Setenv("PATH", oldPath)
		os.Setenv("PATH", bindir)

		got := resolveEvenerBinaryPath("", "/no/such/hub", nil)
		if got != evenerPath {
			t.Fatalf("nil lookPath resolution = %q, want %q", got, evenerPath)
		}
	})
}

// TestRunMainLeavesAnAbsentProvidersConfigAlone pins the write side of the
// registry cut-over: the hub must not conjure a providers.toml, because the
// only schema it knew how to write is the pre-registry one its own children
// now refuse. An absent path is a valid configuration — the registry reads it
// as "user layer: none" — so the hub starts, writes nothing, and a child
// pointed at the same path builds a working client.
func TestRunMainLeavesAnAbsentProvidersConfigAlone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	providersPath := filepath.Join(root, "config", "evener", "providers.toml")
	t.Setenv("EVENER_PROVIDERS_CONFIG", providersPath)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", filepath.Join(root, "config", "evener", "credentials.toml"))

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.RunDir = filepath.Join(root, "run")
	cfg.StateGlob = filepath.Join(root, "projects", "*")
	cfg.PastIndexDB = filepath.Join(root, "hub", "index.db")
	cfg.HubStateRoot = filepath.Join(root, "hub")
	cfg.PluginAutoUpgrade = false
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()

	served := false
	deps := mainDeps{
		loadRegistry:    hermeticRegistryLoader,
		loadConfig:      func(string) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(context.Context, hubHTTPServer, hubShutdowner) error {
			served = true
			return nil
		},
	}

	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if !served {
		t.Fatalf("the hub did not reach its serve boundary: %s", stderr.String())
	}
	if _, err := os.Stat(providersPath); !os.IsNotExist(err) {
		t.Fatalf("the hub wrote %s (stat err=%v); an absent providers.toml must stay absent", providersPath, err)
	}

	// A child pointed at this same path builds a client: an absent user layer
	// is a valid configuration, not a missing one.
	client, err := cmdutil.LoadClientAt(providersPath, t.TempDir())
	if err != nil {
		t.Fatalf("LoadClientAt(%q): %v — a child spawned with EVENER_PROVIDERS_CONFIG=%s must build a client", providersPath, err, providersPath)
	}
	if _, err := client.Resolve("openai/gpt-5.2"); err != nil {
		t.Fatalf("the child's client resolves nothing: %v", err)
	}
}

// hermeticRegistryLoader is the registry loader every runMain test injects:
// cmdutil's own, with the network and the catalog cache taken away so the
// hub under test observes only the environment the test set up.
func hermeticRegistryLoader(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
	return cmdutil.LoadRegistry(append(extra, registry.WithOffline(true), registry.WithoutCache())...)
}

// TestRunMainDegradesOnAnOldSchemaProvidersConfig is spec §14.1's flag-day
// row for the hub: an old-schema providers.toml fails to load, and the hub
// starts anyway on implicit instances alone, surfaces the pointer as a
// diagnostic, refuses instance writes, and hands every child it spawns
// EVENER_PROVIDERS_CONFIG= (present, empty) plus EVENER_CREDENTIALS_CONFIG so
// the child computes the same instance set from the environment and the store.
func TestRunMainDegradesOnAnOldSchemaProvidersConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GROQ_API_KEY", "gk")

	providersPath := filepath.Join(root, "providers.toml")
	credentialsPath := filepath.Join(root, "credentials.toml")
	const oldSchema = "default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"
	if err := os.WriteFile(providersPath, []byte(oldSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", providersPath)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", credentialsPath)

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.RunDir = filepath.Join(root, "run")
	cfg.StateGlob = filepath.Join(root, "projects", "*")
	cfg.PastIndexDB = filepath.Join(root, "hub", "index.db")
	cfg.HubStateRoot = filepath.Join(root, "hub")
	cfg.PluginAutoUpgrade = false
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	served := false
	var web *WebServer
	deps := mainDeps{
		loadRegistry:    hermeticRegistryLoader,
		loadConfig:      func(string) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: credentials.LoadStore,
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(context.Context, hubHTTPServer, hubShutdowner) error {
			served = true
			return nil
		},
		afterWeb: func(w *WebServer) { web = w },
	}

	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if !served {
		t.Fatalf("the hub did not reach its serve boundary: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "providers config:") || !strings.Contains(stderr.String(), "implicit instances only") {
		t.Fatalf("startup did not announce the degraded load: %s", stderr.String())
	}
	if data, err := os.ReadFile(providersPath); err != nil || string(data) != oldSchema {
		t.Fatalf("the hub rewrote the file it could not read (err=%v):\n%s", err, data)
	}

	// The instances pane reports the refusal and still lists the implicit set.
	if web == nil {
		t.Fatal("afterWeb never ran")
	}
	list, ok := hubInstanceListOverRPC(t, web)
	if !ok {
		t.Fatal("evener/instance/list is not registered")
	}
	if !list.WritesRefused {
		t.Fatalf("instance/list writesRefused = false; want the write refusal: %+v", list)
	}
	if !strings.Contains(strings.Join(list.Diagnostics, "\n"), "§14.1") {
		t.Fatalf("instance/list diagnostics carry the flag-day pointer: %v", list.Diagnostics)
	}
	found := false
	for _, inst := range list.Instances {
		if inst.Name == "groq" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the hub still launches against the implicit set: %+v", list.Instances)
	}

	// And the child it spawns is pointed at no user layer, with the hub's own
	// credentials.toml. Asked through the spawner's own env-building path, not
	// a copy of it.
	spawner, ok := web.cfg.Spawner.(*HubSpawner)
	if !ok {
		t.Fatalf("spawner = %T, want *HubSpawner", web.cfg.Spawner)
	}
	var env []string
	oldFn := listEvenerLaunchModelContractFn
	listEvenerLaunchModelContractFn = func(_ context.Context, _ string, childEnv []string) (appwire.ModelListResponse, error) {
		env = childEnv
		return appwire.ModelListResponse{}, nil
	}
	t.Cleanup(func() { listEvenerLaunchModelContractFn = oldFn })
	if _, err := spawner.ListLaunchModelContract(ctx); err != nil {
		t.Fatalf("ListLaunchModelContract: %v", err)
	}
	if !slices.Contains(env, "EVENER_PROVIDERS_CONFIG=") {
		t.Fatalf("child env must carry a present, empty EVENER_PROVIDERS_CONFIG: %v", env)
	}
	if !slices.Contains(env, "EVENER_CREDENTIALS_CONFIG="+credentialsPath) {
		t.Fatalf("child env must name the hub's credentials.toml: %v", env)
	}
}

// hubInstanceListOverRPC dispatches evener/instance/list on a hub's app server.
func hubInstanceListOverRPC(t *testing.T, web *WebServer) (appwire.InstanceListResponse, bool) {
	t.Helper()
	raw, err := web.appRPC.Router().Dispatch(t.Context(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerInstanceList,
		Params: mustMarshal(t, appwire.EmptyParams{}),
	})
	if err != nil {
		t.Fatalf("evener/instance/list: %v", err)
	}
	resp, ok := raw.(appwire.InstanceListResponse)
	return resp, ok
}
