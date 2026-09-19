package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceGrantInstallationSweepsExpiredAuthority(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixedClock().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	s, err := startService(filepath.Join(t.TempDir(), "private"), StoreOptions{Clock: now})
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, s.close(context.Background())) })
	requireNoError(t, s.store.EnsureNamespace(t.Context(), "namespace", "realm", "owner"))
	live := testScope()
	live.ExpiresAt = now().Add(time.Hour)
	requireNoError(t, s.store.InstallGrant(t.Context(), sha256.Sum256([]byte("live")), live))
	client := sdkClient(t, s.ready.Endpoint, "live")
	var expired string
	for batch := range 10 {
		short := live
		short.ExpiresAt = now().Add(time.Second)
		for i := range 10 {
			expired = fmt.Sprintf("short-%d-%d", batch, i)
			requireNoError(t, s.store.InstallGrant(t.Context(), sha256.Sum256([]byte(expired)), short))
		}
		clock.Add(int64(2 * time.Second))
		fresh := live
		fresh.ExpiresAt = now().Add(time.Second)
		requireNoError(t, s.store.InstallGrant(t.Context(), sha256.Sum256(fmt.Appendf(nil, "fresh-%d", batch)), fresh))
		s.store.mu.RLock()
		retained := len(s.store.grants)
		s.store.mu.RUnlock()
		if retained != 2 {
			t.Fatalf("batch=%d retained=%d want live+fresh", batch, retained)
		}
	}
	status, _ := postProtocol(t, s, expired, []string{"2025-11-25"}, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if status != http.StatusUnauthorized {
		t.Fatalf("expired authority HTTP=%d", status)
	}
	_, err = client.ListTools(t.Context(), nil)
	requireNoError(t, err)
}
