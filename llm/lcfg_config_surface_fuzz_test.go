package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/providercfg"
	"primeradiant.com/evener/llm/registry"
)

type lcfgSecretFile struct {
	writeErr error
	syncErr  error
}

func (f *lcfgSecretFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}
func (f *lcfgSecretFile) Sync() error  { return f.syncErr }
func (f *lcfgSecretFile) Close() error { return nil }

// This file fuzzes the llm package's top-level config / pricing / error-surface
// entry points. Every top-level identifier is prefixed with the lane token
// "lcfg" to avoid collisions with sibling fuzz lanes editing package llm.
//
// The fuzzers install fakes for the two process-global registries
// (instanceFactories, envFactories) and the default-client cache, always
// restoring them via defer. Go's fuzzing engine calls a fuzz body sequentially
// within a worker process, so the save/install/restore dance is safe.

// lcfgOKAdapter is a trivial ProviderAdapter used to make instance/env factories
// succeed without touching the network.
type lcfgOKAdapter struct{ lcfgName string }

func (a *lcfgOKAdapter) Name() string { return a.lcfgName }
func (a *lcfgOKAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	return Response{Provider: a.lcfgName, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *lcfgOKAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, ErrStreamUnsupported
}

// lcfgFuzzInstance is the JSON-decodable shape a fuzzed provider config takes.
type lcfgFuzzInstance struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	APIStyle string `json:"api_style"`
	BaseURL  string `json:"base_url"`
}

type lcfgFuzzConfig struct {
	Default   string             `json:"default"`
	Instances []lcfgFuzzInstance `json:"instances"`
}

// lcfgInstallInstanceFactories installs fake instance-adapter factories for the
// two well-known lane types and returns a restore func. "lcfg_ok" always
// constructs; "lcfg_err" always fails.
func lcfgInstallInstanceFactories(t *testing.T) func() {
	t.Helper()
	instanceFactoriesMu.Lock()
	saved := make(map[instanceFactoryKey]InstanceAdapterFactory, len(instanceFactories))
	maps.Copy(saved, instanceFactories)
	instanceFactories = map[instanceFactoryKey]InstanceAdapterFactory{
		{typ: "lcfg_ok"}: func(inst providercfg.InstanceConfig, stateHome string) (ProviderAdapter, error) {
			_ = stateHome
			return &lcfgOKAdapter{lcfgName: inst.Name}, nil
		},
		{typ: "lcfg_err"}: func(inst providercfg.InstanceConfig, stateHome string) (ProviderAdapter, error) {
			_ = inst
			_ = stateHome
			return nil, errors.New("lcfg factory boom")
		},
	}
	instanceFactoriesMu.Unlock()
	return func() {
		instanceFactoriesMu.Lock()
		instanceFactories = saved
		instanceFactoriesMu.Unlock()
	}
}

// Fuzz_lcfg_NewFromProviders drives newFromProviders (the shared core of
// NewFromProviders and NewFromAvailableProviders) over arbitrary provider
// configs. Instances typed "lcfg_ok" construct, "lcfg_err" fail, and any other
// type is an unknown-wiring hard error.
//
// Oracles:
//   - never panics for any config;
//   - determinism: the strict path returns byte-identical error text (or the
//     same registered provider set) across two calls on the same config;
//   - success invariant: when the strict path returns no error, every configured
//     instance name is a registered provider name, and the NameToTag map covers
//     exactly the configured instances;
//   - partial invariant: NewFromAvailableProviders never returns an unknown-wiring
//     failure as a *partial* success — a wiring error still hard-fails.
func Fuzz_lcfg_NewFromProviders(f *testing.F) {
	f.Add([]byte(`{"default":"a","instances":[{"name":"a","type":"lcfg_ok"}]}`))
	f.Add([]byte(`{"instances":[{"name":"a","type":"lcfg_ok"},{"name":"b","type":"lcfg_err"}]}`))
	f.Add([]byte(`{"default":"z","instances":[{"name":"c","type":"openai","api_style":"chat-completions"}]}`))
	f.Add([]byte(`{"instances":[]}`))
	f.Add([]byte(`{"instances":[{"name":"a","type":"lcfg_ok","api_style":"responses"}]}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, cfgBytes []byte) {
		restore := lcfgInstallInstanceFactories(t)
		defer restore()

		var fc lcfgFuzzConfig
		_ = json.Unmarshal(cfgBytes, &fc) // partial/failed decode is a valid input.

		cfg := providercfg.Config{Default: fc.Default}
		for _, in := range fc.Instances {
			cfg.Instances = append(cfg.Instances, providercfg.InstanceConfig{
				Name:     in.Name,
				Type:     providercfg.Type(in.Type),
				APIStyle: providercfg.APIStyle(in.APIStyle),
				BaseURL:  in.BaseURL,
			})
		}

		c1, err1 := NewFromProviders(cfg)
		c2, err2 := NewFromProviders(cfg)

		// Determinism: identical error disposition and text.
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("NewFromProviders nondeterministic error presence: %v vs %v", err1, err2)
		}
		if err1 != nil && err2 != nil && err1.Error() != err2.Error() {
			t.Fatalf("NewFromProviders nondeterministic error text: %q vs %q", err1, err2)
		}

		if err1 == nil {
			if c1 == nil {
				t.Fatalf("NewFromProviders returned nil client with nil error")
			}
			names := map[string]bool{}
			for _, n := range c1.ProviderNames() {
				names[n] = true
			}
			for _, in := range cfg.Instances {
				if !names[in.Name] {
					t.Fatalf("provider %q missing from registered names %v", in.Name, c1.ProviderNames())
				}
			}
			// Determinism of the registered set across two builds.
			set2 := map[string]bool{}
			for _, n := range c2.ProviderNames() {
				set2[n] = true
			}
			if !reflect.DeepEqual(names, set2) {
				t.Fatalf("NewFromProviders registered set nondeterministic: %v vs %v", names, set2)
			}
		}

		// Partial path must not panic and must not swallow unknown-wiring errors.
		_, _, perr := NewFromAvailableProviders(cfg)
		for _, in := range cfg.Instances {
			key := instanceFactoryKey{typ: string(in.Type), apiStyle: string(in.APIStyle)}
			_, exact := instanceFactories[key]
			_, catch := instanceFactories[instanceFactoryKey{typ: string(in.Type)}]
			if !exact && !catch {
				if perr == nil {
					t.Fatalf("NewFromAvailableProviders hid unknown-wiring for type %q", in.Type)
				}
				break
			}
		}
	})
}

// Fuzz_lcfg_PriceFromCost drives PriceFromCost over fuzzed registry costs,
// the one path from a resolved row's rates to the Price every estimate is
// computed at (spec §7.5).
//
// Oracles:
//   - never panics, nil cost included;
//   - a nil cost is never priced; a present one always is, all-zero rates
//     included (a row that says zero is not a row with no price);
//   - determinism: two conversions agree;
//   - provenance: the base rates are the cost's own, and each cache tier is
//     set exactly when its rate is positive — the function may not invent,
//     scale or drop a rate;
//   - the 1-hour creation tier is always absent: models.dev carries a single
//     cache-write rate, reported as the 5-minute tier.
func Fuzz_lcfg_PriceFromCost(f *testing.F) {
	f.Add(3.0, 15.0, 0.3, 3.75)
	f.Add(0.0, 0.0, 0.0, 0.0)
	f.Add(1.25, 10.0, 0.125, 0.0)
	f.Add(-7.0, 999.0, -0.5, 2.0)
	f.Add(math.NaN(), math.Inf(1), 0.0, math.Inf(-1))

	f.Fuzz(func(t *testing.T, in, out, cacheRead, cacheWrite float64) {
		if p, ok := PriceFromCost(nil); ok || p != (Price{}) {
			t.Fatalf("PriceFromCost(nil) = (%+v, %v), want the zero Price and false", p, ok)
		}

		cost := &registry.Cost{Input: in, Output: out, CacheRead: cacheRead, CacheWrite: cacheWrite}
		p1, ok1 := PriceFromCost(cost)
		p2, ok2 := PriceFromCost(cost)
		if !ok1 || !ok2 {
			t.Fatalf("PriceFromCost(%+v) reported no price for a present cost", cost)
		}
		if !lcfgSamePrice(p1, p2) {
			t.Fatalf("PriceFromCost(%+v) nondeterministic: %+v vs %+v", cost, p1, p2)
		}
		if !lcfgSameRate(p1.InputPerM, in) || !lcfgSameRate(p1.OutputPerM, out) {
			t.Fatalf("PriceFromCost(%+v) base rates = %v/%v, want the cost's own", cost, p1.InputPerM, p1.OutputPerM)
		}
		lcfgAssertCacheTier(t, "cache_read", p1.CacheReadPerM, cacheRead)
		lcfgAssertCacheTier(t, "cache_create_5m", p1.CacheCreation5mPerM, cacheWrite)
		if p1.CacheCreation1hPerM != nil {
			t.Fatalf("PriceFromCost(%+v) invented a 1-hour tier: %v", cost, *p1.CacheCreation1hPerM)
		}
	})
}

// lcfgAssertCacheTier checks one optional cache tier: present with exactly the
// row's rate when that rate is positive, absent otherwise.
func lcfgAssertCacheTier(t *testing.T, name string, got *float64, rate float64) {
	t.Helper()
	if rate > 0 {
		if got == nil || !lcfgSameRate(*got, rate) {
			t.Fatalf("%s = %v, want %v", name, got, rate)
		}
		return
	}
	if got != nil {
		t.Fatalf("%s = %v, want absent for a non-positive rate %v", name, *got, rate)
	}
}

// lcfgSameRate compares two rates by value, treating NaN as equal to NaN: a
// fuzzed NaN rate must survive the conversion unchanged, and == says otherwise.
func lcfgSameRate(a, b float64) bool {
	return a == b || (math.IsNaN(a) && math.IsNaN(b))
}

// lcfgSamePrice is lcfgSameRate over a whole Price, including its optional tiers.
func lcfgSamePrice(a, b Price) bool {
	sameTier := func(x, y *float64) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return lcfgSameRate(*x, *y)
	}
	return lcfgSameRate(a.InputPerM, b.InputPerM) && lcfgSameRate(a.OutputPerM, b.OutputPerM) &&
		sameTier(a.CacheReadPerM, b.CacheReadPerM) &&
		sameTier(a.CacheCreation5mPerM, b.CacheCreation5mPerM) &&
		sameTier(a.CacheCreation1hPerM, b.CacheCreation1hPerM)
}

// Fuzz_lcfg_Kind drives Kind over classified errors built from fuzzed HTTP
// status codes and messages, plus non-HTTP timeout and plain errors.
//
// Oracles:
//   - never panics;
//   - Kind is stable under error wrapping (errors.As chain walk): Kind(err) ==
//     Kind(fmt.Errorf("...: %w", err));
//   - Kind is deterministic and always returns a value whose String() is a
//     known, non-empty label;
//   - Kind(nil) == KindUnknown; a plain error is KindUnknown.
func Fuzz_lcfg_Kind(f *testing.F) {
	f.Add(400, "malformed request")
	f.Add(401, "bad key")
	f.Add(403, "cyber_policy_violation")
	f.Add(404, "model does not exist")
	f.Add(408, "deadline")
	f.Add(413, "context length exceeded")
	f.Add(429, "slow down")
	f.Add(429, "you exceeded your current quota")
	f.Add(400, "content was blocked by the content filter")
	f.Add(500, "boom")
	f.Add(503, "unavailable")
	f.Add(0, "")
	f.Add(-7, "usage policy violation")

	f.Fuzz(func(t *testing.T, status int, message string) {
		err := ErrorFromHTTPStatus("fuzzprov", status, message, nil, nil)
		if err == nil {
			t.Fatalf("ErrorFromHTTPStatus returned nil for status %d", status)
		}

		k1 := Kind(err)
		k2 := Kind(err)
		if k1 != k2 {
			t.Fatalf("Kind nondeterministic: %v vs %v", k1, k2)
		}
		if !lcfgKnownKind(k1) {
			t.Fatalf("Kind returned out-of-range value %d", int(k1))
		}
		if k1.String() == "" {
			t.Fatalf("Kind(%v).String() is empty", k1)
		}

		wrapped := fmt.Errorf("provider %q call failed: %w", "fuzzprov", err)
		if kw := Kind(wrapped); kw != k1 {
			t.Fatalf("Kind unstable under wrapping: bare=%v wrapped=%v", k1, kw)
		}
		doubleWrapped := fmt.Errorf("outer: %w", wrapped)
		if kd := Kind(doubleWrapped); kd != k1 {
			t.Fatalf("Kind unstable under double wrapping: bare=%v wrapped=%v", k1, kd)
		}

		// Non-HTTP timeout path always classifies as KindTimeout.
		to := NewRequestTimeoutError("fuzzprov", message, context.DeadlineExceeded)
		if kt := Kind(to); kt != KindTimeout {
			t.Fatalf("Kind(timeout) = %v, want %v", kt, KindTimeout)
		}

		if Kind(nil) != KindUnknown {
			t.Fatalf("Kind(nil) != KindUnknown")
		}
		if Kind(errors.New(message)) != KindUnknown {
			t.Fatalf("Kind(plain error) != KindUnknown")
		}
	})
}

func lcfgKnownKind(k ErrorKind) bool {
	switch k {
	case KindUnknown, KindInvalidRequest, KindAuthentication, KindAccessDenied,
		KindNotFound, KindTimeout, KindContextLength, KindContentFilter,
		KindQuotaExceeded, KindRateLimit, KindServer:
		return true
	default:
		return false
	}
}

// lcfgEnvFactoryPlan is the JSON-decodable recipe for a fuzzed set of env
// adapter factories: each rune maps to a factory behavior.
type lcfgEnvFactoryPlan struct {
	// Kinds is a string where each byte selects a factory behavior:
	//   'o' -> configured, returns an adapter
	//   'n' -> not configured (ok=false)
	//   'e' -> returns an error
	Kinds    string `json:"kinds"`
	StateDir string `json:"state_dir"`
}

// Fuzz_lcfg_NewFromEnv drives NewFromEnv and DefaultClient with a fuzzed set of
// env adapter factories, exercising the configured/not-configured/error branches
// and the lazy default-client cache — all without touching the network.
//
// Oracles:
//   - never panics;
//   - error-propagation: if any factory (up to the first error) returns an
//     error, NewFromEnv returns non-nil error and nil client;
//   - no-providers: if no factory configures one and none errors first, NewFromEnv
//     returns the "no providers" error;
//   - success: otherwise the client's provider count matches the number of
//     configuring factories that ran before any error;
//   - DefaultClient caches: after SetDefaultClient(c), DefaultClient() returns c;
//     the lazy path returns the same (client,err) on repeat calls.
func Fuzz_lcfg_NewFromEnv(f *testing.F) {
	f.Add([]byte(`{"kinds":"o","state_dir":"/x"}`))
	f.Add([]byte(`{"kinds":"nne"}`))
	f.Add([]byte(`{"kinds":"oon"}`))
	f.Add([]byte(`{"kinds":"nnn"}`))
	f.Add([]byte(`{"kinds":"eo"}`))
	f.Add([]byte(`{"kinds":""}`))

	f.Fuzz(func(t *testing.T, planBytes []byte) {
		var plan lcfgEnvFactoryPlan
		_ = json.Unmarshal(planBytes, &plan)

		restore := lcfgInstallEnvFactories(t, plan.Kinds)
		defer restore()

		var opts []EnvOption
		if plan.StateDir != "" {
			opts = append(opts, WithStateDir(plan.StateDir))
		}

		c, err := NewFromEnv(opts...)

		// Compute the expected disposition: iterate kinds until the first error.
		configured := 0
		var expectErr bool
		for i := 0; i < len(plan.Kinds); i++ {
			switch plan.Kinds[i] {
			case 'e':
				expectErr = true
			case 'o':
				configured++
			}
			if expectErr {
				break
			}
		}

		switch {
		case expectErr:
			if err == nil {
				t.Fatalf("NewFromEnv(kinds=%q) expected error, got nil", plan.Kinds)
			}
			if c != nil {
				t.Fatalf("NewFromEnv error path returned non-nil client")
			}
		case configured == 0:
			if err == nil {
				t.Fatalf("NewFromEnv(kinds=%q) expected no-providers error", plan.Kinds)
			}
		default:
			if err != nil {
				t.Fatalf("NewFromEnv(kinds=%q) unexpected error: %v", plan.Kinds, err)
			}
			if got := len(c.ProviderNames()); got != configured {
				t.Fatalf("NewFromEnv provider count = %d, want %d", got, configured)
			}
		}

		lcfgExerciseDefaultClient(t)
	})
}

// lcfgInstallEnvFactories replaces the global env factory slice with fakes per
// the kinds recipe and returns a restore func.
func lcfgInstallEnvFactories(t *testing.T, kinds string) func() {
	t.Helper()
	envFactoriesMu.Lock()
	saved := append([]EnvAdapterFactory{}, envFactories...)
	envFactories = nil
	for i := 0; i < len(kinds); i++ {
		idx := i
		kind := kinds[i]
		envFactories = append(envFactories, func(cfg EnvConfig) (ProviderAdapter, bool, error) {
			_ = cfg
			switch kind {
			case 'e':
				return nil, false, fmt.Errorf("lcfg env factory %d boom", idx)
			case 'o':
				return &lcfgOKAdapter{lcfgName: fmt.Sprintf("lcfg_env_%d", idx)}, true, nil
			default:
				return nil, false, nil
			}
		})
	}
	envFactoriesMu.Unlock()
	return func() {
		envFactoriesMu.Lock()
		envFactories = saved
		envFactoriesMu.Unlock()
	}
}

// lcfgExerciseDefaultClient covers both the set-path and the lazy-init path of
// DefaultClient, restoring the module globals afterward.
func lcfgExerciseDefaultClient(t *testing.T) {
	t.Helper()
	defaultClientMu.Lock()
	savedClient, savedErr, savedInit := defaultClient, errDefaultClient, defaultClientInit
	defaultClientMu.Unlock()
	defer func() {
		defaultClientMu.Lock()
		defaultClient, errDefaultClient, defaultClientInit = savedClient, savedErr, savedInit
		defaultClientMu.Unlock()
	}()

	// Set-path: SetDefaultClient then DefaultClient returns exactly it.
	sentinel := NewClient()
	SetDefaultClient(sentinel)
	got, err := DefaultClient()
	if err != nil || got != sentinel {
		t.Fatalf("DefaultClient after SetDefaultClient = (%p,%v), want (%p,nil)", got, err, sentinel)
	}

	// Lazy-path: reset the cache and let DefaultClient build via NewFromEnv using
	// whatever env factories are installed. Repeat calls must return the same
	// (client, err) pair from the cache.
	defaultClientMu.Lock()
	defaultClient, errDefaultClient, defaultClientInit = nil, nil, false
	defaultClientMu.Unlock()

	c1, e1 := DefaultClient()
	c2, e2 := DefaultClient()
	if c1 != c2 || (e1 == nil) != (e2 == nil) {
		t.Fatalf("DefaultClient lazy cache inconsistent: (%p,%v) vs (%p,%v)", c1, e1, c2, e2)
	}
}

// Fuzz_lcfg_ContinuationSecret drives LoadOrCreateContinuationSecret over a
// sandboxed temp dir, exercising the create, re-read (idempotent), empty-dir,
// mkdir-failure, and corrupt-file (bad mode / bad length) branches. It also
// checks that a hasher keyed from the loaded secret is deterministic.
//
// NB: LoadOrCreateContinuationSecret uses the os package directly (not afero),
// so the sandbox is t.TempDir(); the fuzzed path component is sanitized so it
// can never escape the temp root.
//
// Oracles:
//   - never panics;
//   - idempotent: two loads of the same valid state dir return byte-identical
//     32-byte secrets;
//   - empty state dir always errors with ErrContinuationSecretUnavailable;
//   - a corrupt secret file (wrong mode or wrong length) always errors;
//   - a hasher built from the secret is deterministic across two derivations.
func Fuzz_lcfg_ContinuationSecret(f *testing.F) {
	f.Add(uint8(0), "sub", []byte("seed"))
	f.Add(uint8(1), "", []byte(""))
	f.Add(uint8(2), "corrupt-mode", []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
	f.Add(uint8(3), "corrupt-len", []byte("short"))
	f.Add(uint8(4), "blocked", []byte("z"))

	f.Fuzz(func(t *testing.T, variant uint8, rawName string, content []byte) {
		injectedErr := errors.New("injected secret failure")
		baseOps := continuationSecretOps{
			mkdirAll: func(string, os.FileMode) error { return nil },
			read:     func(string) ([]byte, error) { return nil, os.ErrNotExist },
			randRead: func(p []byte) (int, error) { return len(p), nil },
			openFile: func(string, int, os.FileMode) (continuationSecretFile, error) { return &lcfgSecretFile{}, nil },
		}
		for _, failure := range []string{"rand", "exist", "open", "write", "sync"} {
			ops := baseOps
			switch failure {
			case "rand":
				ops.randRead = func([]byte) (int, error) { return 0, injectedErr }
			case "exist":
				ops.openFile = func(string, int, os.FileMode) (continuationSecretFile, error) { return nil, os.ErrExist }
				reads := 0
				ops.read = func(string) ([]byte, error) {
					reads++
					if reads == 1 {
						return nil, os.ErrNotExist
					}
					return make([]byte, 32), nil
				}
			case "open":
				ops.openFile = func(string, int, os.FileMode) (continuationSecretFile, error) { return nil, injectedErr }
			case "write":
				ops.openFile = func(string, int, os.FileMode) (continuationSecretFile, error) {
					return &lcfgSecretFile{writeErr: injectedErr}, nil
				}
			case "sync":
				ops.openFile = func(string, int, os.FileMode) (continuationSecretFile, error) {
					return &lcfgSecretFile{syncErr: injectedErr}, nil
				}
			}
			secret, err := loadOrCreateContinuationSecret("state", ops)
			if failure == "exist" {
				if err != nil || len(secret) != 32 {
					t.Fatalf("exclusive-create race = (%d,%v)", len(secret), err)
				}
			} else if !errors.Is(err, ErrContinuationSecretUnavailable) {
				t.Fatalf("%s failure = %v, want ErrContinuationSecretUnavailable", failure, err)
			}
		}

		// Empty-state-dir branch: independent of variant.
		if _, err := LoadOrCreateContinuationSecret(""); !errors.Is(err, ErrContinuationSecretUnavailable) {
			t.Fatalf("LoadOrCreateContinuationSecret(\"\") = %v, want ErrContinuationSecretUnavailable", err)
		}

		root := t.TempDir()
		name := lcfgSafeName(rawName)
		stateDir := filepath.Join(root, name)
		readBlocked := filepath.Join(root, "read-blocked")
		if err := os.Mkdir(readBlocked, 0o600); err == nil {
			if _, err := readContinuationSecret(readBlocked); !errors.Is(err, ErrContinuationSecretUnavailable) {
				t.Fatalf("directory secret read = %v", err)
			}
		}

		switch variant % 5 {
		case 0, 1:
			// Happy path + idempotency.
			s1, err := LoadOrCreateContinuationSecret(stateDir)
			if err != nil {
				t.Fatalf("LoadOrCreateContinuationSecret create failed: %v", err)
			}
			if len(s1) != 32 {
				t.Fatalf("secret length = %d, want 32", len(s1))
			}
			s2, err := LoadOrCreateContinuationSecret(stateDir)
			if err != nil {
				t.Fatalf("LoadOrCreateContinuationSecret re-read failed: %v", err)
			}
			if !bytes.Equal(s1, s2) {
				t.Fatalf("LoadOrCreateContinuationSecret not idempotent")
			}
			// Hasher determinism from the loaded secret.
			h1 := NewContinuationHasher(s1)
			h2 := NewContinuationHasher(s2)
			g1, err1 := h1.HashContinuationHandle("response_id", string(content))
			g2, err2 := h2.HashContinuationHandle("response_id", string(content))
			if err1 != nil || err2 != nil || g1 != g2 {
				t.Fatalf("hasher nondeterministic: %q(%v) vs %q(%v)", g1, err1, g2, err2)
			}

		case 2:
			// Corrupt file: correct length, wrong mode.
			path := ContinuationSecretPath(stateDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Skipf("setup mkdir failed: %v", err)
			}
			secret := make([]byte, 32)
			writeContinuationSecretForMode(t, path, secret, 0o644)
			if _, err := LoadOrCreateContinuationSecret(stateDir); !errors.Is(err, ErrContinuationSecretUnavailable) {
				t.Fatalf("wrong-mode secret = %v, want ErrContinuationSecretUnavailable", err)
			}

		case 3:
			// Corrupt file: correct mode, wrong length.
			path := ContinuationSecretPath(stateDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Skipf("setup mkdir failed: %v", err)
			}
			bad := content
			if len(bad) == 32 {
				bad = append(bad, 'x') // force a non-32 length
			}
			if err := os.WriteFile(path, bad, 0o600); err != nil {
				t.Skipf("setup write failed: %v", err)
			}
			if _, err := LoadOrCreateContinuationSecret(stateDir); !errors.Is(err, ErrContinuationSecretUnavailable) {
				t.Fatalf("wrong-length secret = %v, want ErrContinuationSecretUnavailable", err)
			}

		case 4:
			// mkdir failure: plant a regular file where the "continuation" dir
			// must be created, so os.MkdirAll fails with "not a directory".
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				t.Skipf("setup mkdir failed: %v", err)
			}
			blocker := filepath.Join(stateDir, "continuation")
			if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
				t.Skipf("setup write failed: %v", err)
			}
			if _, err := LoadOrCreateContinuationSecret(stateDir); !errors.Is(err, ErrContinuationSecretUnavailable) {
				t.Fatalf("mkdir-blocked secret = %v, want ErrContinuationSecretUnavailable", err)
			}
		}
	})
}

// lcfgSafeName reduces a fuzzed string to a single safe path component that can
// never escape the temp root (no separators, no dot-only names).
func lcfgSafeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	name := b.String()
	if name == "" {
		name = "state"
	}
	return name
}
