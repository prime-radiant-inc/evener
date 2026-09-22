package valueexpr

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeJWT builds a three-segment token whose payload carries exp, the shape
// every OIDC id token has, so the cache can refresh ahead of expiry.
func makeJWT(exp int64) string {
	enc := base64.RawURLEncoding
	return strings.Join([]string{
		enc.EncodeToString([]byte(`{"alg":"none"}`)),
		enc.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp))),
		enc.EncodeToString([]byte("sig")),
	}, ".")
}

func TestEvaluateCachesShortLivedValue(t *testing.T) {
	ResetForTest()
	base := time.Unix(1_800_000_000, 0)
	now := base
	Now = func() time.Time { return now }
	t.Cleanup(func() { ResetForTest() })

	runs := 0
	RunCommand = func(string) (string, error) {
		runs++
		return makeJWT(base.Unix() + 3600), nil // one hour of life
	}

	if _, err := evaluate("get-token"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if _, err := evaluate("get-token"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if runs != 1 {
		t.Fatalf("executor ran %d times inside the margin; want 1", runs)
	}

	// Crossing exp minus the refresh margin re-mints.
	now = base.Add(3600*time.Second - refreshMargin + time.Second)
	if _, err := evaluate("get-token"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if runs != 2 {
		t.Fatalf("executor ran %d times after the margin passed; want 2", runs)
	}
}

func TestEvaluateCachesLongLivedValueWithTTL(t *testing.T) {
	ResetForTest()
	base := time.Unix(1_800_000_000, 0)
	now := base
	Now = func() time.Time { return now }
	t.Cleanup(func() { ResetForTest() })

	runs := 0
	RunCommand = func(string) (string, error) {
		runs++
		return "a-key-with-no-expiry-claim", nil
	}
	for range 3 {
		if _, err := evaluate("get-key"); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	}
	if runs != 1 {
		t.Fatalf("executor ran %d times inside the TTL; want 1", runs)
	}
	now = base.Add(defaultTTL + time.Second)
	if _, err := evaluate("get-key"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if runs != 2 {
		t.Fatalf("executor ran %d times after the TTL; want 2", runs)
	}
}

func TestEvaluateExpiredJWTDoesNotCache(t *testing.T) {
	ResetForTest()
	base := time.Unix(1_800_000_000, 0)
	Now = func() time.Time { return base }
	t.Cleanup(func() { ResetForTest() })

	runs := 0
	RunCommand = func(string) (string, error) {
		runs++
		return makeJWT(base.Unix() - 10), nil // already expired
	}
	for range 2 {
		if _, err := evaluate("get-token"); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	}
	if runs != 2 {
		t.Fatalf("an expired token cached: executor ran %d times; want 2", runs)
	}
}

func TestEvaluateFailureIsNotCached(t *testing.T) {
	ResetForTest()
	t.Cleanup(func() { ResetForTest() })
	runs := 0
	RunCommand = func(string) (string, error) {
		runs++
		return "", errors.New("command exited with status 1: boom")
	}
	for range 2 {
		if _, err := evaluate("get-token"); err == nil {
			t.Fatal("evaluate succeeded on a failing command")
		}
	}
	if runs != 2 {
		t.Fatalf("a failed command was cached: executor ran %d times; want 2", runs)
	}
}

func TestEvaluateSharesOneMintAcrossConcurrentCallers(t *testing.T) {
	ResetForTest()
	t.Cleanup(func() { ResetForTest() })
	entered := make(chan struct{})
	release := make(chan struct{})
	RunCommand = func(string) (string, error) {
		close(entered)
		<-release
		return "shared", nil
	}
	var wg sync.WaitGroup
	results := make([]string, 4)
	errs := make([]error, 4)
	for i := range 4 {
		wg.Go(func() {
			res, err := evaluate("get-token")
			results[i] = res.Value
			errs[i] = err
		})
	}
	<-entered
	close(release)
	wg.Wait()
	for i := range 4 {
		if errs[i] != nil || results[i] != "shared" {
			t.Fatalf("caller %d: %q, %v", i, results[i], errs[i])
		}
	}
}

// mint anchors a plain value's TTL at the moment the command finished, not
// when the resolve began: a slow command must not eat into the value's own
// freshness window.
func TestMintAnchorsTTLAtCompletion(t *testing.T) {
	ResetForTest()
	t.Cleanup(func() { ResetForTest() })
	base := time.Unix(1_800_000_000, 0)
	calls := 0
	Now = func() time.Time {
		calls++
		if calls == 1 { // the resolve start
			return base
		}
		return base.Add(20 * time.Second) // the completion instant
	}
	RunCommand = func(string) (string, error) { return "plain-value", nil }

	res, err := evaluate("get-key")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if want := base.Add(20 * time.Second).Add(defaultTTL); !res.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v; want %v (the TTL must start at completion)", res.ExpiresAt, want)
	}
}

// An exp claim beyond int64 is no expiry at all: Go's float→int conversion
// out of range is implementation-defined, and on amd64 it yields MinInt64 —
// a negative epoch the fuzz invariant would rightly refuse. The claim is
// rejected before any conversion, so a "successful parse" always means a
// usable, positive epoch.
func TestTokenExpiryRejectsOutOfRangeClaims(t *testing.T) {
	enc := base64.RawURLEncoding
	for _, exp := range []string{"1e300", "9223372036854775808", "0.5", "1e-300"} {
		token := strings.Join([]string{
			enc.EncodeToString([]byte(`{"alg":"none"}`)),
			enc.EncodeToString([]byte(fmt.Sprintf(`{"exp":%s}`, exp))),
			enc.EncodeToString([]byte("sig")),
		}, ".")
		if _, ok := tokenExpiry(token); ok {
			t.Fatalf("tokenExpiry accepted exp %s; an out-of-range claim is no expiry at all", exp)
		}
	}
}

// Pruning rides the insertion: a dead entry holds nothing worth keeping,
// so an expired secret leaves memory with the next command that caches.
func TestEvaluatePrunesExpiredEntries(t *testing.T) {
	ResetForTest()
	t.Cleanup(func() { ResetForTest() })
	base := time.Unix(1_800_000_000, 0)
	now := base
	Now = func() time.Time { return now }
	RunCommand = func(cmd string) (string, error) {
		if cmd == "short-lived" {
			return makeJWT(base.Unix() + 120), nil // fresh for a minute, no longer
		}
		return "plain", nil
	}

	if _, err := evaluate("short-lived"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	evaluateMu.Lock()
	if len(cache) != 1 {
		evaluateMu.Unlock()
		t.Fatalf("cache holds %d entries; want the fresh mint alone", len(cache))
	}
	evaluateMu.Unlock()

	now = base.Add(10 * time.Minute)
	if _, err := evaluate("other"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	evaluateMu.Lock()
	defer evaluateMu.Unlock()
	if len(cache) != 1 {
		t.Fatalf("cache holds %d entries after the prune; want only the fresh one", len(cache))
	}
	if _, ok := cache["short-lived"]; ok {
		t.Fatal("the expired entry survived the prune")
	}
}
