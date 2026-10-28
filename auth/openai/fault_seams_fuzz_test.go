//go:build evenerfuzz

package openai

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type authfz_badListener struct{ addr net.Addr }

func (l authfz_badListener) Accept() (net.Conn, error) { return nil, errors.New("accept") }
func (l authfz_badListener) Close() error              { return nil }
func (l authfz_badListener) Addr() net.Addr            { return l.addr }

type authfz_errContext struct{ context.Context }

func (authfz_errContext) Done() <-chan struct{} { return nil }
func (authfz_errContext) Err() error            { return context.Canceled }

func FuzzAuthOpenAIFaultSeams(f *testing.F) {
	for i := byte(0); i < 17; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		switch selector % 17 {
		case 0:
			old := randomRead
			randomRead = func([]byte) (int, error) { return 0, errors.New("random") }
			t.Cleanup(func() { randomRead = old })
			_, _, _ = GeneratePKCE()
			_, _ = GenerateState()
			_, _ = NewService(Config{}, nil).Login(context.Background(), t.TempDir(), "openai")
		case 1:
			old := randomRead
			calls := 0
			randomRead = func(p []byte) (int, error) {
				calls++
				if calls == 2 {
					return 0, errors.New("random")
				}
				for i := range p {
					p[i] = byte(i)
				}
				return len(p), nil
			}
			t.Cleanup(func() { randomRead = old })
			_, _ = NewService(Config{}, nil).Login(context.Background(), t.TempDir(), "openai")
		case 2, 3:
			old := marshalDeviceRequest
			marshalDeviceRequest = func(any) ([]byte, error) { return nil, errors.New("marshal") }
			t.Cleanup(func() { marshalDeviceRequest = old })
				if selector%17 == 2 {
				_, _ = RequestDeviceCode(context.Background(), nil, Config{})
			} else {
				_, _, _ = PollDeviceAuthOnce(context.Background(), nil, Config{}, DeviceCode{})
			}
		case 4:
			old := callbackListen
			callbackListen = func(context.Context, int) (net.Listener, error) {
				return authfz_badListener{addr: cov_nonTCPAddr{}}, nil
			}
			t.Cleanup(func() { callbackListen = old })
			_, _ = StartCallbackServer(context.Background(), Config{}, 0, "state")
		case 5:
			old := callbackListen
			callbackListen = func(context.Context, int) (net.Listener, error) { return authfz_badListener{addr: &net.TCPAddr{}}, nil }
			t.Cleanup(func() { callbackListen = old })
			s, err := StartCallbackServer(context.Background(), Config{}, 0, "state")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = s.Wait(context.Background())
		case 6:
			s := &CallbackServer{server: &http.Server{}}
			_ = s.Close()
			_ = normalizeServerCloseError(http.ErrServerClosed)
		case 7:
			for _, goos := range []string{"darwin", "windows", "linux"} {
				_ = browserCommand(goos, "https://example.invalid")
			}
			t.Setenv("PATH", t.TempDir())
			_ = defaultBrowserOpener("https://example.invalid")
		case 8:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, _ = waitForLoginCompletion(ctx, nil, make(chan callbackResult))
			ch := make(chan callbackResult, 1)
			ch <- callbackResult{err: context.Canceled}
			_, _ = waitForLoginCompletion(context.Background(), nil, ch)
			callback, manual := make(chan callbackResult, 1), make(chan callbackResult, 1)
			callback <- callbackResult{err: context.Canceled}
			manual <- callbackResult{err: context.Canceled}
			_, _ = waitForLoginCompletion(authfz_errContext{Context: context.Background()}, callback, manual)
		case 9:
			cfg := DefaultConfig()
			cfg.IssuerBaseURL = ":"
			_, _ = ExchangeCode(context.Background(), nil, cfg, TokenExchangeRequest{})
			state := t.TempDir()
			rec := sampleAuthRecord()
			rec.Expiry = time.Unix(0, 0)
			if err := SaveAuth(state, "openai", rec); err != nil {
				t.Fatal(err)
			}
			s := newTestService(time.Now())
			s.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
				if err := os.RemoveAll(filepath.Dir(AuthFilePath(state, "openai"))); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Dir(AuthFilePath(state, "openai")), []byte("block"), 0o600); err != nil {
					t.Fatal(err)
				}
				return TokenSet{AccessToken: "new"}, nil
			}
			_, _ = s.ResolveRuntimeCredentials(context.Background(), state, "openai")
		case 10:
			old := authMarshal
			authMarshal = func(any, string, string) ([]byte, error) { return nil, errors.New("marshal") }
			t.Cleanup(func() { authMarshal = old })
			_ = SaveAuth(t.TempDir(), "openai", sampleAuthRecord())
		case 11:
			// A rejection whose timestamps cannot marshal to RFC 3339 (a
			// year outside [0,9999]) fails to become a note.
			at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := RecordRefreshRejection(t.TempDir(), "openai", sampleAuthRecord(), at); err == nil {
				t.Fatal("RecordRefreshRejection with an out-of-range rejected-at unexpectedly succeeded")
			}
		case 12:
			old := authMkdirAll
			authMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
			t.Cleanup(func() { authMkdirAll = old })
			if err := RecordRefreshRejection(t.TempDir(), "openai", sampleAuthRecord(), time.Now()); err == nil {
				t.Fatal("RecordRefreshRejection with a failing auth directory creation unexpectedly succeeded")
			}
		case 13:
			// An unreadable note is not a refusal: RefreshRejected reports
			// false rather than trusting a note it cannot parse.
			state := t.TempDir()
			path := RefreshRejectionPath(state, "openai")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			if RefreshRejected(state, "openai", sampleAuthRecord()) {
				t.Fatal("RefreshRejected reported a refusal from an unreadable note")
			}
		case 14:
			// clearRefreshRejection surfaces a removal failure that is not
			// the note's absence; its callers only treat it as best effort.
			state := t.TempDir()
			if err := os.MkdirAll(filepath.Join(RefreshRejectionPath(state, "openai"), "occupied"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := clearRefreshRejection(state, "openai"); err == nil {
				t.Fatal("clearRefreshRejection on a non-empty directory at the note path unexpectedly succeeded")
			}
		case 15:
			// A record corrupted between the pre-lock load and the
			// under-lock re-read fails the re-read. s.now is the last
			// collaborator invoked before the refresh lock is taken, so
			// corrupting there stands in single-goroutine for a concurrent
			// writer that breaks the record inside the refresh window.
			state := t.TempDir()
			rec := sampleAuthRecord()
			rec.Expiry = time.Unix(0, 0)
			if err := SaveAuth(state, "openai", rec); err != nil {
				t.Fatal(err)
			}
			stale := time.Now().Add(-time.Hour)
			s := newTestService(stale)
			corrupted := false
			s.now = func() time.Time {
				if !corrupted {
					corrupted = true
					if err := os.WriteFile(AuthFilePath(state, "openai"), []byte("corrupt"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return stale
			}
			_, err := s.ResolveRuntimeCredentials(context.Background(), state, "openai")
			if !errors.Is(err, ErrAuthCorrupt) {
				t.Fatalf("ResolveRuntimeCredentials with a record corrupted under the refresh lock = %v, want ErrAuthCorrupt", err)
			}
		case 16:
			old := authMkdirAll
			authMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
			t.Cleanup(func() { authMkdirAll = old })
			_ = SaveAuth(t.TempDir(), "openai", sampleAuthRecord())
		}
	})
}
