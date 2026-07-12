//go:build serffuzz

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

type authfz_tempFile struct{ fail byte }

type authfz_errContext struct{ context.Context }

func (authfz_errContext) Done() <-chan struct{} { return nil }
func (authfz_errContext) Err() error            { return context.Canceled }

func (f *authfz_tempFile) Name() string { return "authfz-temp" }
func (f *authfz_tempFile) Chmod(os.FileMode) error {
	if f.fail == 0 {
		return errors.New("chmod")
	}
	return nil
}
func (f *authfz_tempFile) Write(p []byte) (int, error) {
	if f.fail == 1 {
		return 0, errors.New("write")
	}
	return len(p), nil
}
func (f *authfz_tempFile) Sync() error {
	if f.fail == 2 {
		return errors.New("sync")
	}
	return nil
}
func (f *authfz_tempFile) Close() error {
	if f.fail == 3 {
		return errors.New("close")
	}
	return nil
}

func FuzzAuthOpenAIFaultSeams(f *testing.F) {
	for i := byte(0); i < 18; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		switch selector % 18 {
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
			if selector%18 == 2 {
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
			old := authCreateTemp
			authCreateTemp = func(string, string) (authTempFile, error) { return nil, errors.New("create") }
			t.Cleanup(func() { authCreateTemp = old })
			_ = SaveAuth(t.TempDir(), "openai", sampleAuthRecord())
		case 12, 13, 14, 15:
			oldCreate, oldRename := authCreateTemp, authRename
			fail := (selector % 18) - 12
			authCreateTemp = func(string, string) (authTempFile, error) { return &authfz_tempFile{fail: fail}, nil }
			if fail == 3 {
				authRename = func(string, string) error { return errors.New("rename") }
			}
			t.Cleanup(func() { authCreateTemp, authRename = oldCreate, oldRename })
			_ = SaveAuth(t.TempDir(), "openai", sampleAuthRecord())
		case 16:
			old := authMkdirAll
			authMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
			t.Cleanup(func() { authMkdirAll = old })
			_ = SaveAuth(t.TempDir(), "openai", sampleAuthRecord())
		case 17:
			oldCreate, oldRename := authCreateTemp, authRename
			authCreateTemp = func(string, string) (authTempFile, error) { return &authfz_tempFile{fail: 99}, nil }
			authRename = func(string, string) error { return errors.New("rename") }
			t.Cleanup(func() { authCreateTemp, authRename = oldCreate, oldRename })
			_ = SaveAuth(t.TempDir(), "openai", sampleAuthRecord())
		}
	})
}
