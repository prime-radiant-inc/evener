package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

// Uses the production executable and HTTP guard, not a handler with a substituted token.
func TestTutorialHubTokenRotationRunbook(t *testing.T) {
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
		logPath := filepath.Join(home, fmt.Sprintf("hub-%d.log", generation))
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "hub", "--config", config, "--addr", "127.0.0.1:0", "--evener", binary)
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_STATE_HOME="+filepath.Join(home, "state"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"))
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
		return hubStack{addr: awaitHubListening(t, logPath)}, stop
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
		req, err := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/api/debug/subscriptions", nil)
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
