package hub

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Wrong source ordering, absent-versus-empty handling, or lost source metadata
// makes this table fail against the shared resolver.
func TestEndpointConfigPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name              string
		flag, env, config EndpointInput
		want, source      string
	}{
		{"default", EndpointInput{}, EndpointInput{}, EndpointInput{}, "127.0.0.1:9180", "default"},
		{"config", EndpointInput{}, EndpointInput{}, EndpointInput{"0.0.0.0:9222", true}, "0.0.0.0:9222", "config"},
		{"env", EndpointInput{}, EndpointInput{"[::1]:9223", true}, EndpointInput{"0.0.0.0:9222", true}, "[::1]:9223", "env"},
		{"flag", EndpointInput{" :0 ", true}, EndpointInput{"[::1]:9223", true}, EndpointInput{"0.0.0.0:9222", true}, ":0", "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveEndpointConfig(tc.flag, tc.env, tc.config, "https://Example.COM:443/")
			if err != nil {
				t.Fatal(err)
			}
			if got.ListenAddr.String() != tc.want || got.ListenSource != tc.source || got.PublicURL == nil || got.PublicURL.String() != "https://example.com" {
				t.Fatalf("resolved = %+v, want listener %q from %q and canonical public origin", got, tc.want, tc.source)
			}
		})
	}
}

func TestEndpointConfigExplicitEmpty(t *testing.T) {
	for _, tc := range []struct {
		name              string
		flag, env, config EndpointInput
	}{
		{"flag", EndpointInput{"", true}, EndpointInput{"localhost:1234", true}, EndpointInput{}},
		{"env", EndpointInput{}, EndpointInput{"", true}, EndpointInput{"localhost:1234", true}},
		{"config", EndpointInput{}, EndpointInput{}, EndpointInput{"", true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveEndpointConfig(tc.flag, tc.env, tc.config, "")
			var invalid *hubendpoint.ValidationError
			if !errors.As(err, &invalid) || invalid.Code != "empty" || invalid.Field != "listen_addr" {
				t.Fatalf("error = %v, want empty listen_addr", err)
			}
		})
	}
}

func TestEndpointConfigValidationErrorSources(t *testing.T) {
	bad := "https://user:sentinel-secret@localhost"
	for _, tc := range []struct {
		name              string
		flag, env, config EndpointInput
		publicURL         string
		source, field     string
	}{
		{"flag", EndpointInput{bad, true}, EndpointInput{"localhost:9230", true}, EndpointInput{"localhost:9240", true}, "", "flag", "listen_addr"},
		{"env", EndpointInput{}, EndpointInput{bad, true}, EndpointInput{"localhost:9240", true}, "", "env", "listen_addr"},
		{"config", EndpointInput{}, EndpointInput{}, EndpointInput{bad, true}, "", "config", "listen_addr"},
		{"public URL", EndpointInput{"localhost:9230", true}, EndpointInput{}, EndpointInput{}, bad, "config", "userinfo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveEndpointConfig(tc.flag, tc.env, tc.config, tc.publicURL)
			assertEndpointErrorSource(t, err, tc.source, tc.field)
		})
	}
}

func TestEndpointConfigLoadedPublicURLValidationSource(t *testing.T) {
	_, err := decodeConfig("hub.toml", "public_url = \"https://user:sentinel-secret@localhost\"\n")
	assertEndpointErrorSource(t, err, "config", "userinfo")
}

func assertEndpointErrorSource(t *testing.T, err error, wantSource, wantField string) {
	t.Helper()
	var sourced *EndpointValidationError
	var invalid *hubendpoint.ValidationError
	if !errors.As(err, &sourced) || sourced.Source != wantSource || !errors.As(err, &invalid) || invalid.Field != wantField {
		t.Fatalf("error = %T %v, want typed source %q and validation field %q", err, err, wantSource, wantField)
	}
	if strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatal("validation error leaked supplied credential")
	}
}

func TestEndpointConfigLoadedInputsAndMetadata(t *testing.T) {
	t.Setenv("EVENER_HUB_ADDR", "")
	if err := os.Unsetenv("EVENER_HUB_ADDR"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_HUB_LISTEN_ADDR", "")
	if err := os.Unsetenv("EVENER_HUB_LISTEN_ADDR"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hub.toml")
	_, missing, err := loadResolvedConfig(path, false, EndpointInput{})
	if err != nil || missing.ConfigPath != path || missing.ConfigExists || missing.ListenSource != "default" || missing.ListenAddr.String() != "127.0.0.1:9180" {
		t.Fatalf("missing config: %+v, %v", missing, err)
	}
	if err := os.WriteFile(path, []byte("addr = \"0.0.0.0:9340\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		args              []string
		env, want, source string
	}{
		{"TOML", nil, "", "0.0.0.0:9340", "config"},
		{"environment", nil, "127.0.0.1:9341", "127.0.0.1:9341", "env"},
		{"long equals", []string{"--addr=127.0.0.1:9342"}, "127.0.0.1:9341", "127.0.0.1:9342", "flag"},
		{"short equals", []string{"-addr=127.0.0.1:9343"}, "127.0.0.1:9341", "127.0.0.1:9343", "flag"},
		{"separated", []string{"--addr", "127.0.0.1:9344"}, "127.0.0.1:9341", "127.0.0.1:9344", "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("EVENER_HUB_LISTEN_ADDR", tc.env)
			}
			var stderr bytes.Buffer
			opts, err := parseHubOptions(tc.args, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			cfg, endpoint, err := loadResolvedConfig(path, false, EndpointInput{opts.addr, opts.addrPresent})
			if err != nil || cfg.Addr != tc.want || endpoint.ListenAddr.String() != tc.want || endpoint.ListenSource != tc.source || !endpoint.ConfigExists || endpoint.ConfigPath != path {
				t.Fatalf("cfg=%+v endpoint=%+v err=%v, want %q from %s", cfg, endpoint, err, tc.want, tc.source)
			}
		})
	}
	if err := os.WriteFile(path, []byte("addr = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = loadResolvedConfig(path, false, EndpointInput{})
	var invalid *hubendpoint.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != "empty" {
		t.Fatalf("explicit empty TOML: %v", err)
	}
	if err := os.WriteFile(path, []byte("addr = \"localhost:9340\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_HUB_LISTEN_ADDR", "")
	_, _, err = loadResolvedConfig(path, false, EndpointInput{})
	invalid = nil
	if !errors.As(err, &invalid) || invalid.Code != "empty" {
		t.Fatalf("explicit empty env: %v", err)
	}
}

func TestEndpointConfigKeepsRequestedListenerAfterBind(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	var requested, bound string
	deps.afterWeb = func(web *WebServer) {
		bound = web.cfg.HubAddr
		spawner, ok := web.cfg.Spawner.(*HubSpawner)
		if !ok {
			t.Fatalf("spawner = %T", web.cfg.Spawner)
		}
		requested = spawner.Cfg.Addr
	}
	var stderr bytes.Buffer
	if err := runMain([]string{"--addr", cfg.Addr, "--evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatal(err)
	}
	if requested != "127.0.0.1:0" || bound == requested {
		t.Fatalf("configured = %q, bound = %q", requested, bound)
	}
	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n == 0 {
		t.Fatalf("bound port = %q, %v", port, err)
	}
}

func TestEndpointConfigRetiredSettings(t *testing.T) {
	for _, tc := range []struct{ name, document, envName, replacement string }{
		{"old TOML with new TOML", "public_url = \"https://example.test\"\nmobile_base_url = \"https://user:sentinel-secret@example.test/path\"\n", "mobile_base_url", "public_url"},
		{"old environment with new environment", "addr = \"localhost:9123\"\n", "EVENER_HUB_ADDR", "EVENER_HUB_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EVENER_HUB_ADDR", "")
			if err := os.Unsetenv("EVENER_HUB_ADDR"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EVENER_HUB_LISTEN_ADDR", "localhost:9300")
			if tc.envName == "EVENER_HUB_ADDR" {
				t.Setenv("EVENER_HUB_ADDR", "https://user:sentinel-secret@example.test/path")
			}
			path := filepath.Join(t.TempDir(), "hub.toml")
			if err := os.WriteFile(path, []byte(tc.document), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadResolvedConfig(path, true, EndpointInput{"localhost:9400", true})
			var migration *EndpointMigrationError
			if !errors.As(err, &migration) || migration.Retired != tc.envName || migration.Replacement != tc.replacement {
				t.Fatalf("error = %T %v, want typed migration %s -> %s", err, err, tc.envName, tc.replacement)
			}
			if strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatal("migration error leaked supplied credential")
			}
		})
	}
}

func TestEndpointConfigRetiredKeyDoesNotHideTypeError(t *testing.T) {
	_, err := decodeConfig("hub.toml", "mobile_base_url = \"https://example.test\"\naddr = 42\n")
	var migration *EndpointMigrationError
	if err == nil || errors.As(err, &migration) || errors.Unwrap(err) == nil {
		t.Fatalf("decode error = %T %v, want wrapped TOML type failure, not migration", err, err)
	}
}

func TestEndpointConfigValidationBeforeEffects(t *testing.T) {
	t.Setenv("EVENER_HUB_ADDR", "") // cleared below: an empty retired env is still a migration error
	if err := os.Unsetenv("EVENER_HUB_ADDR"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_HUB_LISTEN_ADDR", "localhost:9230")
	path := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(path, []byte("addr = \"localhost:9240\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--addr=https://user:sentinel-secret@localhost"}, {"-addr=https://user:sentinel-secret@localhost"}, {"--addr", "https://user:sentinel-secret@localhost"}} {
		var effects []string
		deps := defaultMainDeps()
		deps.ensureDirs = func() error { effects = append(effects, "dirs"); return nil }
		deps.acquireLock = func(string) (func(), error) { effects = append(effects, "lock"); return func() {}, nil }
		deps.newToken = func() (string, error) { effects = append(effects, "token"); return "", nil }
		deps.listen = func(context.Context, string, string) (net.Listener, error) {
			effects = append(effects, "bind")
			return nil, errors.New("unexpected bind")
		}
		var stderr bytes.Buffer
		err := runMain(append([]string{"--config", path}, args...), &stderr, deps)
		var invalid *hubendpoint.ValidationError
		if !errors.As(err, &invalid) || invalid.Field != "listen_addr" || len(effects) != 0 {
			t.Fatalf("args %v: error=%v effects=%v", args, err, effects)
		}
		if strings.Contains(err.Error(), "sentinel-secret") || strings.Contains(stderr.String(), "sentinel-secret") {
			t.Fatal("invalid endpoint leaked credential")
		}
	}
	for _, tc := range []struct {
		name, document, envValue, field string
	}{
		{"env listener", "addr = \"localhost:9240\"\n", "https://user:sentinel-secret@localhost", "listen_addr"},
		{"TOML listener", "addr = \"https://user:sentinel-secret@localhost\"\n", "", "listen_addr"},
		{"public origin", "addr = \"localhost:9240\"\npublic_url = \"https://user:sentinel-secret@localhost\"\n", "", "userinfo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EVENER_HUB_LISTEN_ADDR", tc.envValue)
			if tc.envValue == "" {
				if err := os.Unsetenv("EVENER_HUB_LISTEN_ADDR"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(tc.document), 0o600); err != nil {
				t.Fatal(err)
			}
			var effects []string
			deps := defaultMainDeps()
			deps.ensureDirs = func() error { effects = append(effects, "dirs"); return nil }
			deps.acquireLock = func(string) (func(), error) { effects = append(effects, "lock"); return func() {}, nil }
			deps.newToken = func() (string, error) { effects = append(effects, "token"); return "", nil }
			deps.listen = func(context.Context, string, string) (net.Listener, error) {
				effects = append(effects, "bind")
				return nil, errors.New("unexpected bind")
			}
			var stderr bytes.Buffer
			err := runMain([]string{"--config", path}, &stderr, deps)
			var invalid *hubendpoint.ValidationError
			if !errors.As(err, &invalid) || invalid.Field != tc.field || len(effects) != 0 {
				t.Fatalf("error=%v effects=%v", err, effects)
			}
			if strings.Contains(err.Error(), "sentinel-secret") || strings.Contains(stderr.String(), "sentinel-secret") {
				t.Fatal("invalid endpoint leaked credential")
			}
		})
	}
}

func TestEndpointConfigPreservesOperatorKeys(t *testing.T) {
	// A machine-managed host rewrite must not erase operator TOML or host entries.
	path := filepath.Join(t.TempDir(), "hub.toml")
	contents := "addr = \"0.0.0.0:9180\"\npublic_url = \"https://example.test\"\noperator_custom = \"kept\"\n\n[[hosts]]\nname = \"alpha\"\nssh = \"alpha.example\"\naddr = \"127.0.0.1:0\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	m := bootHostManager(t, path)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "beta", Address: "beta.example"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "operator_custom = \"kept\"") || !strings.Contains(string(data), "public_url = \"https://example.test\"") {
		t.Fatalf("operator keys lost on rewrite: %s", data)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "0.0.0.0:9180" || len(cfg.Hosts) != 2 || cfg.Hosts[0].Name != "alpha" || cfg.Hosts[0].Addr != "127.0.0.1:0" || cfg.Hosts[1].Name != "beta" {
		t.Fatalf("host rewrite changed existing host: %+v", cfg.Hosts)
	}
}

func TestEndpointConfigEmptyAndRetiredStartupEffects(t *testing.T) {
	bin := filepath.Join(liveStackBinaries(t, repoRootForTestMain()), "evener")
	for _, tc := range []struct {
		name, document, env string
		args                []string
		migration           bool
	}{
		{name: "empty flag", args: []string{"--addr="}},
		{name: "empty environment", env: "EVENER_HUB_LISTEN_ADDR"},
		{name: "empty TOML", document: "addr = \"\"\n"},
		{name: "retired empty environment", env: "EVENER_HUB_ADDR", migration: true},
		{name: "retired populated environment", env: "EVENER_HUB_ADDR=TASK8_RETIRED_72c9", migration: true},
		{name: "retired TOML beside replacement", document: "addr = \"127.0.0.1:0\"\npublic_url = \"https://example.test\"\nmobile_base_url = \"https://retired.test\"\n", migration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, state := endpointTestRoot(t)
			config := endpointTestConfig(t, root, state, "127.0.0.1:0")
			if tc.document != "" {
				data, err := os.ReadFile(config)
				if err != nil {
					t.Fatal(err)
				}
				_, rest, _ := strings.Cut(string(data), "\n")
				if err := os.WriteFile(config, []byte(tc.document+rest), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.env != "" {
				key, value, _ := strings.Cut(tc.env, "=")
				t.Setenv(key, value)
			}
			before, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			var effects []string
			deps := defaultMainDeps()
			deps.ensureDirs = func() error { effects = append(effects, "dirs"); return nil }
			deps.acquireLock = func(string) (func(), error) { effects = append(effects, "lock"); return func() {}, nil }
			deps.loadAuthToken = func(string) (string, error) { effects = append(effects, "token"); return "", nil }
			deps.listen = func(context.Context, string, string) (net.Listener, error) {
				effects = append(effects, "listen")
				return nil, errors.New("unexpected listener")
			}
			args := append([]string{"--config", config}, tc.args...)
			var stderr bytes.Buffer
			err = runMain(args, &stderr, deps)
			var invalid *hubendpoint.ValidationError
			var migration *EndpointMigrationError
			if (tc.migration && !errors.As(err, &migration)) || (!tc.migration && (!errors.As(err, &invalid) || invalid.Code != "empty")) || len(effects) != 0 {
				t.Fatalf("error=%v effects=%v", err, effects)
			}
			// Repeat through the shipped entrypoint with real filesystem dependencies.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			cmd := exec.CommandContext(ctx, bin, append([]string{"hub"}, args...)...)
			err = cmd.Run()
			cancel()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 1 {
				t.Fatalf("startup exit=%v", err)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid startup mutated state: %v %v", entries, err)
			}
			after, err := os.ReadFile(config)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid startup rewrote config")
			}
		})
	}
}
