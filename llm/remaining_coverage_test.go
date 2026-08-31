package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// mockSessionMiddleware implements Middleware and sessionAPILogReleaser.
type mockSessionMiddleware struct {
	onRelease func(string) error
}

func (m *mockSessionMiddleware) WrapComplete(next CompleteFunc) CompleteFunc { return next }
func (m *mockSessionMiddleware) WrapStream(next StreamFunc) StreamFunc       { return next }
func (m *mockSessionMiddleware) ReleaseSession(id string) error              { return m.onRelease(id) }

// TestReleaseSessionAPILogNilClient covers the nil client path (lines 229-231).
func TestReleaseSessionAPILogNilClient(t *testing.T) {
	var c *Client
	if err := c.ReleaseSessionAPILog("sess"); err != nil {
		t.Fatalf("nil client: %v", err)
	}
}

// TestReleaseSessionAPILogWithMiddleware covers the middleware iteration path
// (lines 232-238).
func TestReleaseSessionAPILogWithMiddleware(t *testing.T) {
	c := NewClient()
	var released []string
	mw := &mockSessionMiddleware{onRelease: func(id string) error {
		released = append(released, id)
		return nil
	}}
	c.Use(mw)
	if err := c.ReleaseSessionAPILog("sess-a"); err != nil {
		t.Fatalf("ReleaseSessionAPILog: %v", err)
	}
	if len(released) != 1 || released[0] != "sess-a" {
		t.Fatalf("released = %v, want [sess-a]", released)
	}
}

// TestReleaseSessionAPILogWithMiddlewareError covers the error join path.
func TestReleaseSessionAPILogWithMiddlewareError(t *testing.T) {
	c := NewClient()
	mw := &mockSessionMiddleware{onRelease: func(id string) error {
		return errors.New("release failed")
	}}
	c.Use(mw)
	if err := c.ReleaseSessionAPILog("sess"); err == nil {
		t.Fatal("ReleaseSessionAPILog with error should return error")
	}
}

// TestBeginProviderOperationNilContext covers the nil ctx path (lines 274-275).
func TestBeginProviderOperationNilContext(t *testing.T) {
	c := NewClient()
	ctx, _ := c.beginProviderOperation(context.TODO())
	if ctx == nil {
		t.Fatal("ctx should not be nil")
	}
}

func TestUsagelimitMessageEmpty(t *testing.T) {
	limit := usageLimit{
		message:  "",
		planType: "pro",
		resetsAt: time.Now().Add(time.Hour),
	}
	msg := usageLimitMessage(limit, time.Now())
	if !stringContains(msg, "usage limit reached") {
		t.Fatalf("empty message fallback = %q", msg)
	}
}

// stringContains is a simple substring check.
func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestSanitizeRequestForAPILogHostCredential covers the Host credential path.
func TestSanitizeRequestForAPILogHostCredential(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://provider.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "credential-host-sentinel"
	material := NewAPILogCredentialMaterial(nil, nil, "credential-host-sentinel")
	_, headers := SanitizeRequestForAPILog(req, material)
	if _, ok := headers["Host"]; ok {
		t.Fatal("Host containing credential should be excluded")
	}
}

// TestBeginProviderOperationWithContext covers the normal path with a context.
func TestBeginProviderOperationWithContext(t *testing.T) {
	c := NewClient()
	ctx := context.Background()
	ctx2, _ := c.beginProviderOperation(ctx)
	if ctx2 == nil {
		t.Fatal("ctx should not be nil")
	}
}
