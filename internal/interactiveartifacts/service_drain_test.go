package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServiceOwnerLockHeldThroughDrainAndStoreClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	root := filepath.Join(t.TempDir(), "private")
	s, err := startService(root, StoreOptions{Clock: fixedClock, hooks: storeHooks{beforeCommit: func() { close(entered); <-release }}})
	requireNoError(t, err)
	var closeOnce sync.Once
	var closeErr error
	closeService := func() error {
		closeOnce.Do(func() { closeErr = s.close(context.Background()) })
		return closeErr
	}
	t.Cleanup(func() { resume(); requireNoError(t, closeService()) })
	requireNoError(t, s.store.EnsureNamespace(t.Context(), "namespace", "realm", "owner"))
	token := "drain-test"
	requireNoError(t, s.store.InstallGrant(t.Context(), sha256.Sum256([]byte(token)), testScope()))
	client := sdkClient(t, s.ready.Endpoint, token)
	called := make(chan error, 1)
	go func() {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("drain"))})
		if err == nil && result.IsError {
			err = errors.New("draining mutation was rejected")
		}
		called <- err
	}()
	<-entered
	draining := make(chan struct{})
	s.server.RegisterOnShutdown(func() { close(draining) })
	closed := make(chan error, 1)
	go func() { closed <- closeService() }()
	<-draining
	lock, err := acquireServiceLock(filepath.Join(root, "owner.lock"))
	if err == nil {
		_ = lock.Close()
		t.Error("owner lock released while an admitted mutation was draining")
	}
	resume()
	requireNoError(t, <-called)
	requireNoError(t, <-closed)
	if err := s.store.db.PingContext(t.Context()); err == nil {
		t.Fatal("service shutdown returned with SQLite still open")
	}
	lock, err = acquireServiceLock(filepath.Join(root, "owner.lock"))
	requireNoError(t, err)
	requireNoError(t, lock.Close())
	replacement, err := startService(root, StoreOptions{Clock: fixedClock})
	requireNoError(t, err)
	requireNoError(t, replacement.close(context.Background()))
}
