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
		return "", fmt.Errorf("command exited with status 1: boom")
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := evaluate("get-token")
			results[i] = res.Value
			errs[i] = err
		}()
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

// The value contract is the evaluator's: trimmed verbatim output, and no
// output refused. These drive the real executor through evaluate.
func TestRealExecutorThroughEvaluate(t *testing.T) {
	t.Run("trims stdout", func(t *testing.T) {
		ResetForTest()
		res, err := evaluate("printf '  real-token \\n'")
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if res.Value != "real-token" {
			t.Fatalf("got %q", res.Value)
		}
	})

	t.Run("keeps multi-line output verbatim once trimmed", func(t *testing.T) {
		ResetForTest()
		res, err := evaluate("printf 'line-one\\nline-two\\n'")
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if res.Value != "line-one\nline-two" {
			t.Fatalf("got %q", res.Value)
		}
	})

	t.Run("no output is a failure", func(t *testing.T) {
		ResetForTest()
		_, err := evaluate("true")
		if err == nil || !strings.Contains(err.Error(), "no output") {
			t.Fatalf("got %v", err)
		}
	})
}

// These drive the real executor directly for the failure taxonomy it owns.
func TestRealRunCommand(t *testing.T) {
	t.Run("non-zero exit is a failure with stderr's first line", func(t *testing.T) {
		_, err := realRunCommand("printf out; printf 'boom\\nnoise' >&2; exit 3")
		if err == nil {
			t.Fatal("non-zero exit reported success")
		}
		var cerr *CommandError
		if !errors.As(err, &cerr) || cerr.Status != 3 || !strings.Contains(cerr.Error(), "boom") || strings.Contains(cerr.Error(), "noise") {
			t.Fatalf("got %v (%T)", err, err)
		}
		if strings.Contains(cerr.Error(), "out") {
			t.Fatalf("stdout leaked into the error: %v", cerr)
		}
	})

	t.Run("oversized output is a failure", func(t *testing.T) {
		_, err := realRunCommand("head -c 1048577 /dev/zero")
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("timeout kills the command", func(t *testing.T) {
		ResetForTest()
		old := commandTimeout
		commandTimeout = 100 * time.Millisecond
		t.Cleanup(func() { commandTimeout = old; ResetForTest() })
		_, err := realRunCommand("sleep 5")
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("got %v", err)
		}
	})
}

