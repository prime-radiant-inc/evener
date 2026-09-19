package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
)

func TestServiceRealHTTPQueueFairnessAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	s, err := startService(filepath.Join(t.TempDir(), "private"), StoreOptions{Clock: fixedClock, hooks: storeHooks{beforeAdmission: func() { entered <- struct{}{}; <-release }}})
	requireNoError(t, err)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); requireNoError(t, s.close(context.Background())) })
	requireNoError(t, s.store.EnsureNamespace(context.Background(), "namespace", "realm", "owner"))
	for _, token := range []string{"noisy", "other"} {
		scope := testScope()
		scope.PrincipalID = token
		requireNoError(t, s.store.InstallGrant(context.Background(), sha256.Sum256([]byte(token)), scope))
	}
	changes := make(chan AdmissionStats, 1024)
	s.admission.onChange = func(stats AdmissionStats) { changes <- stats }
	waitQueued := func(want int) {
		t.Helper()
		for {
			stats := <-changes
			if stats.Queued == want {
				return
			}
		}
	}
	send := func(ctx context.Context, token string, i int) (int, error) {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact_publish","arguments":%s}}`, createJSON(fmt.Sprintf("overload-%d", i)))
		if token == "other" {
			body = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ready.Endpoint, bytes.NewBufferString(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() { _, _ = send(context.Background(), "noisy", i) })
	}
	for range 4 {
		<-entered
	}
	queuedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := range 128 {
		wg.Go(func() { _, _ = send(queuedCtx, "noisy", i+4) })
	}
	waitQueued(128)
	status, err := send(context.Background(), "noisy", 999)
	requireNoError(t, err)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("queue overflow=%d", status)
	}
	status, err = send(context.Background(), "other", 999)
	requireNoError(t, err)
	if status != http.StatusOK {
		t.Fatalf("unrelated principal starved: %d", status)
	}
	cancel()
	waitQueued(0)
	stats := s.admission.stats()
	if stats.Active != 4 {
		t.Fatalf("queued cancellation released another lease: %+v", stats)
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	stats = s.admission.stats()
	if stats.Active != 0 || stats.Queued != 0 || stats.PeakQueued != 128 || stats.PeakActive > 32 {
		t.Fatalf("unbounded accounting: %+v", stats)
	}
	t.Logf("actual HTTP service admission: %+v", stats)
}

func TestServiceIncompleteBodiesCannotOccupyEveryIngressSlot(t *testing.T) {
	s, token := serviceFixture(t)
	otherScope := testScope()
	otherScope.PrincipalID = "other-principal"
	requireNoError(t, s.store.InstallGrant(context.Background(), sha256.Sum256([]byte("other-body-reader")), otherScope))
	changes := make(chan AdmissionStats, 2048)
	s.admission.onChange = func(stats AdmissionStats) { changes <- stats }
	endpoint, err := url.Parse(s.ready.Endpoint)
	requireNoError(t, err)
	var sockets []net.Conn
	t.Cleanup(func() {
		for _, socket := range sockets {
			_ = socket.Close()
		}
	})
	waitInFlight := func(want int) {
		t.Helper()
		for {
			stats := <-changes
			if stats.InFlight == want {
				return
			}
		}
	}
	for i := range 132 {
		socket, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", endpoint.Host)
		requireNoError(t, err)
		sockets = append(sockets, socket)
		_, err = fmt.Fprintf(socket, "POST /mcp HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Length: 1\r\n\r\n", endpoint.Host, token)
		requireNoError(t, err)
		waitInFlight(i + 1)
	}
	client := NewHTTPClient(token)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.ready.Endpoint, nil)
	requireNoError(t, err)
	response, err := client.Do(request)
	requireNoError(t, err)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("noisy body-reader overflow: %d", response.StatusCode)
	}
	other := sdkClient(t, s.ready.Endpoint, "other-body-reader")
	catalog, err := other.ListTools(context.Background(), nil)
	requireNoError(t, err)
	if len(catalog.Tools) != 7 {
		t.Fatal("incomplete noisy bodies starved other principal")
	}
	for _, socket := range sockets {
		requireNoError(t, socket.Close())
	}
	sockets = nil
	waitInFlight(0)
}
