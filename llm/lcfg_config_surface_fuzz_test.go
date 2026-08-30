package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
