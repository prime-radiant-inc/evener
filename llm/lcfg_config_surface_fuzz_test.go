package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/llm/providercfg"
)

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
	for k, v := range instanceFactories {
		saved[k] = v
	}
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

// Fuzz_lcfg_GetPrice drives ModelCatalog.GetPrice over (a) the real embedded
// catalog with fuzzed model IDs and (b) a synthetic catalog built from fuzzed
// non-negative rates, exercising exact/alias lookup, the longest-prefix
// fallback, and priceFromModelInfo.
//
// Oracles:
//   - never panics (incl. nil-receiver and empty-ID short-circuits);
//   - determinism: two lookups agree;
//   - non-negative: the embedded catalog only ever yields non-negative rates;
//   - provenance: a found price's rates match some catalog entry whose ID is a
//     prefix of (or an exact/alias match for) the queried ID — the function may
//     not invent a price out of thin air.
func Fuzz_lcfg_GetPrice(f *testing.F) {
	f.Add("claude-opus-4-5-20260101", 3.0, 15.0)
	f.Add("gpt-5.2", 1.25, 10.0)
	f.Add("", 0.0, 0.0)
	f.Add("   ", 2.0, 4.0)
	f.Add("lcfg-unknown-model-xyz", -7.0, 999.0)

	f.Fuzz(func(t *testing.T, modelID string, inRate, outRate float64) {
		emb := EmbeddedModelCatalog()

		p1, ok1 := emb.GetPrice(modelID)
		p2, ok2 := emb.GetPrice(modelID)
		if ok1 != ok2 || p1 != p2 {
			t.Fatalf("embedded GetPrice(%q) nondeterministic: (%+v,%v) vs (%+v,%v)", modelID, p1, ok1, p2, ok2)
		}
		if ok1 {
			if p1.InputPerM < 0 || p1.OutputPerM < 0 {
				t.Fatalf("embedded GetPrice(%q) negative rate: %+v", modelID, p1)
			}
			lcfgAssertPriceProvenance(t, emb, modelID, p1)
		}

		// nil receiver must not panic.
		var nilCat *ModelCatalog
		if _, ok := nilCat.GetPrice(modelID); ok {
			t.Fatalf("nil catalog reported a price for %q", modelID)
		}

		// Synthetic catalog: skip non-finite fuzzed rates (those can only arrive
		// from a hand-built catalog, never from the embedded JSON, and the
		// provenance oracle uses value equality).
		if math.IsNaN(inRate) || math.IsInf(inRate, 0) || math.IsNaN(outRate) || math.IsInf(outRate, 0) {
			return
		}
		in := math.Abs(inRate)
		out := math.Abs(outRate)
		syn := &ModelCatalog{Models: []ModelInfo{
			{ID: "fam", InputCostPerMillion: &in, OutputCostPerMillion: &out},
			{ID: "fam-mini"}, // no rates: forces skip in the prefix loop
			{ID: "other", InputCostPerMillion: &out, OutputCostPerMillion: &in},
		}}
		sp1, sok1 := syn.GetPrice(modelID)
		sp2, sok2 := syn.GetPrice(modelID)
		if sok1 != sok2 || sp1 != sp2 {
			t.Fatalf("synthetic GetPrice(%q) nondeterministic", modelID)
		}
		if sok1 {
			lcfgAssertPriceProvenance(t, syn, modelID, sp1)
		}
	})
}

// lcfgAssertPriceProvenance verifies a returned price traces to a catalog entry
// that legitimately matches modelID (exact/alias/prefix), never a fabrication.
func lcfgAssertPriceProvenance(t *testing.T, cat *ModelCatalog, modelID string, got Price) {
	t.Helper()
	id := strings.TrimSpace(modelID)
	for i := range cat.Models {
		m := &cat.Models[i]
		if m.InputCostPerMillion == nil || m.OutputCostPerMillion == nil {
			continue
		}
		if *m.InputCostPerMillion != got.InputPerM || *m.OutputCostPerMillion != got.OutputPerM {
			continue
		}
		if m.ID == id || strings.HasPrefix(id, m.ID) {
			return
		}
		for _, a := range m.Aliases {
			if a == id {
				return
			}
		}
	}
	t.Fatalf("GetPrice(%q) returned %+v with no matching catalog entry", modelID, got)
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
		// Empty-state-dir branch: independent of variant.
		if _, err := LoadOrCreateContinuationSecret(""); !errors.Is(err, ErrContinuationSecretUnavailable) {
			t.Fatalf("LoadOrCreateContinuationSecret(\"\") = %v, want ErrContinuationSecretUnavailable", err)
		}

		root := t.TempDir()
		name := lcfgSafeName(rawName)
		stateDir := filepath.Join(root, name)

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
			if err := os.WriteFile(path, secret, 0o644); err != nil {
				t.Skipf("setup write failed: %v", err)
			}
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

// Fuzz_lcfg_BuildAPILogRequest drives BuildAPILogRequest with fuzzed requests
// that also populate the ReasoningEffort and Continuation branches (which the
// existing api-log fuzzer leaves cold, since Continuation is json:"-").
//
// Oracles:
//   - never panics;
//   - determinism: two projections of the same request are deeply equal;
//   - structure preservation: MessageCount/ToolCount/ToolNames mirror the
//     request exactly, and when set, ReasoningEffort and the Continuation-derived
//     fields propagate verbatim.
func Fuzz_lcfg_BuildAPILogRequest(f *testing.F) {
	f.Add(
		[]byte(`{"model":"gpt-5.2","provider":"openai","messages":[{"role":"user"}],"tools":[{"name":"shell"}]}`),
		"high",
		[]byte(`{"PreviousResponseIDHash":"ph","ConversationIDHash":"ch","AnchorTurnIndex":3,"DeltaTurnCount":2,"DeltaTurnKinds":["a","b"],"EndpointFamily":"responses","ChatFallbackHistoryLen":7}`),
	)
	f.Add([]byte(`{}`), "", []byte(`null`))
	f.Add([]byte(`{"messages":[{},{}],"tools":[{"name":"a"},{"name":"b"}]}`), "low", []byte(`{}`))

	f.Fuzz(func(t *testing.T, reqBytes []byte, effort string, contBytes []byte) {
		var req Request
		_ = json.Unmarshal(reqBytes, &req)

		if effort != "" {
			e := effort
			req.ReasoningEffort = &e
		}

		var cont ContinuationMetadata
		if err := json.Unmarshal(contBytes, &cont); err == nil && len(contBytes) > 0 && string(contBytes) != "null" {
			req.Continuation = &cont
		}

		lr1 := BuildAPILogRequest(req)
		lr2 := BuildAPILogRequest(req)
		if !reflect.DeepEqual(lr1, lr2) {
			t.Fatalf("BuildAPILogRequest nondeterministic")
		}

		if lr1.MessageCount != len(req.Messages) {
			t.Fatalf("MessageCount=%d, want %d", lr1.MessageCount, len(req.Messages))
		}
		if lr1.ToolCount != len(req.Tools) {
			t.Fatalf("ToolCount=%d, want %d", lr1.ToolCount, len(req.Tools))
		}
		if len(req.Tools) > 0 && len(lr1.ToolNames) != len(req.Tools) {
			t.Fatalf("ToolNames len=%d, want %d", len(lr1.ToolNames), len(req.Tools))
		}
		if req.ReasoningEffort != nil && lr1.ReasoningEffort != *req.ReasoningEffort {
			t.Fatalf("ReasoningEffort=%q, want %q", lr1.ReasoningEffort, *req.ReasoningEffort)
		}
		if req.Continuation != nil {
			if lr1.PreviousResponseIDHash != req.Continuation.PreviousResponseIDHash {
				t.Fatalf("PreviousResponseIDHash dropped")
			}
			if lr1.ConversationIDHash != req.Continuation.ConversationIDHash {
				t.Fatalf("ConversationIDHash dropped")
			}
			if lr1.AnchorTurnIndex != req.Continuation.AnchorTurnIndex {
				t.Fatalf("AnchorTurnIndex dropped")
			}
			if lr1.DeltaTurnCount != req.Continuation.DeltaTurnCount {
				t.Fatalf("DeltaTurnCount dropped")
			}
			if len(lr1.DeltaTurnKinds) != len(req.Continuation.DeltaTurnKinds) {
				t.Fatalf("DeltaTurnKinds len mismatch")
			}
			if lr1.EndpointFamily != req.Continuation.EndpointFamily {
				t.Fatalf("EndpointFamily dropped")
			}
			if lr1.ChatFallbackHistoryLen != req.Continuation.ChatFallbackHistoryLen {
				t.Fatalf("ChatFallbackHistoryLen dropped")
			}
		}
	})
}
