package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

func tutorialRecoveryProviders(t *testing.T) func() (string, <-chan struct{}) {
	t.Helper()
	return func() (string, <-chan struct{}) {
		// A late request to an older endpoint cannot release a newer generation.
		discovered := make(chan struct{})
		var once sync.Once
		var ownedRequests atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ownedRequests.Add(1)
			if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "" {
				t.Error("unexpected provider discovery request or credential")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
			once.Do(func() { close(discovered) })
		}))
		t.Cleanup(func() {
			provider.Close()
			t.Logf("owned provider requests=%d", ownedRequests.Load())
		})
		return provider.URL, discovered
	}
}

// Uses the production executable and HTTP guard, not a handler with a substituted token.
func TestTutorialHubTokenRotationRunbook(t *testing.T) {
	if testing.Short() {
		t.Skip("token rotation runbook builds and runs real hubs")
	}
	var callerRequests atomic.Int32
	caller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callerRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(caller.Close)
	t.Setenv("OLLAMA_BASE_URL", caller.URL+"/v1")
	t.Setenv("OPENAI_BASE_URL", caller.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "synthetic-token-rotation-canary")
	credentialLeaks := 0
	t.Cleanup(func() {
		if got := callerRequests.Load(); got != 0 {
			t.Errorf("caller provider requests = %d, want 0", got)
		}
		if credentialLeaks != 0 {
			t.Errorf("hub generations inheriting caller credential = %d, want 0", credentialLeaks)
		}
		t.Logf("caller provider requests=%d, credential inheritance=%d", callerRequests.Load(), credentialLeaks)
	})
	// The allowlist removes caller providers and credentials. Ollama is still
	// discovered without a key, so bind its implicit fallback to our HTTP boundary.
	providerForGeneration := tutorialRecoveryProviders(t)
	home := t.TempDir()
	root := filepath.Join(home, "custom-hub-state")
	configDir := filepath.Join(home, "config", "evener")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "hub.toml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("hub_state_root = %q\nplugin_auto_upgrade = false\n", root)), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(liveStackBinaries(t, repo), "evener")
	oldToken, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	start := func(generation int) (hubStack, func()) {
		providerURL, discovered := providerForGeneration()
		logPath := filepath.Join(home, fmt.Sprintf("hub-%d.log", generation))
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "hub", "--config", config, "--addr", "127.0.0.1:0", "--evener", binary)
		cmd.Env = append(tutorialChildEnvironment(t, home), "OLLAMA_BASE_URL="+providerURL+"/v1")
		for _, entry := range cmd.Env {
			if strings.HasPrefix(entry, "OPENAI_API_KEY=") {
				credentialLeaks++
			}
		}
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			_ = log.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Error(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("hub exit: %v", err)
				}
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("hub did not stop")
			}
			_ = log.Close()
		}
		t.Cleanup(stop)
		stack := hubStack{addr: awaitHubListening(t, logPath)}
		select {
		case <-discovered:
			t.Logf("hub generation %d completed owned provider discovery", generation)
		case <-time.After(20 * time.Second):
			t.Fatal("hub did not reach owned provider discovery")
		}
		return stack, stop
	}
	first, stop := start(1)
	first.token = oldToken
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	oldClient := first.dialRPC(ctx, t)
	list := func(client *appwire.Client) {
		t.Helper()
		if _, err := clientRequest[appwire.ThreadListResponse](ctx, client, appwire.MethodThreadList, appwire.ThreadListParams{}); err != nil {
			t.Fatalf("authenticated thread/list: %v", err)
		}
	}
	list(oldClient)
	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bootstrap := func(addr, token string) []*http.Cookie {
		t.Helper()
		resp, err := httpClient.Get("http://" + addr + "/auth/" + token)
		// A URL error can carry the credential. Never print it.
		if err != nil {
			t.Fatal("private authorization request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound || len(resp.Cookies()) == 0 {
			t.Fatal("authorization did not set cookie")
		}
		return resp.Cookies()
	}
	check := func(addr, token string, cookies []*http.Cookie, want int) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/debug/subscriptions", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("protected endpoint status = %d, want %d", resp.StatusCode, want)
		}
	}
	oldCookies := bootstrap(first.addr, oldToken)
	check(first.addr, "", nil, http.StatusUnauthorized)
	check(first.addr, oldToken, nil, http.StatusOK)
	check(first.addr, "", oldCookies, http.StatusOK)
	// Deliberately replace only the disposable file while the guard is still running.
	if err := os.Remove(filepath.Join(root, hubedge.TokenFileName)); err != nil {
		t.Fatal(err)
	}
	replacement, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	if replacement == oldToken {
		t.Fatal("token did not change")
	}
	info, err := os.Stat(filepath.Join(root, hubedge.TokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("token permissions = %o", info.Mode().Perm())
	}
	check(first.addr, oldToken, nil, http.StatusOK)
	check(first.addr, "", oldCookies, http.StatusOK)
	check(first.addr, replacement, nil, http.StatusUnauthorized)
	list(oldClient)
	stop()
	// Notification closure is produced by the old socket's read loop, not by client.Close.
	for {
		select {
		case _, ok := <-oldClient.Notifications():
			if !ok {
				goto closed
			}
		case <-ctx.Done():
			t.Fatal("authenticated socket survived hub shutdown")
		}
	}
closed:
	// Follow the operator procedure too: no token at the active path when the
	// stopped hub starts. Production startup must create its own replacement.
	if err := os.Remove(filepath.Join(root, hubedge.TokenFileName)); err != nil {
		t.Fatal(err)
	}
	second, _ := start(2)
	generated, err := os.ReadFile(filepath.Join(root, hubedge.TokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	startupToken := strings.TrimSpace(string(generated))
	if startupToken == "" || startupToken == oldToken || startupToken == replacement {
		t.Fatal("restart did not generate a fresh credential")
	}
	check(second.addr, oldToken, nil, http.StatusUnauthorized)
	check(second.addr, "", oldCookies, http.StatusUnauthorized)
	check(second.addr, replacement, nil, http.StatusUnauthorized)
	check(second.addr, startupToken, nil, http.StatusOK)
	check(second.addr, "", bootstrap(second.addr, startupToken), http.StatusOK)
	second.token = startupToken
	list(second.dialRPC(ctx, t))
	if _, err := os.Stat(filepath.Join(home, "state", "evener", hubedge.TokenFileName)); !os.IsNotExist(err) {
		t.Fatal("token was not confined to configured hub_state_root")
	}
	t.Log("file-only replacement retained old access; restart closed socket; old bearer/cookie=401, replacement bearer/cookie=200, trusted thread/list succeeded")
}
