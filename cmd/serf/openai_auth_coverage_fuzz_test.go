//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/auth/openai"
)

func FuzzOpenAIAuthCommandCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("dispatch", fuzzOpenAIDispatch)
		t.Run("default factories", fuzzOpenAIDefaultFactories)
		t.Run("browser login", fuzzOpenAIBrowserLogin)
		t.Run("device login", fuzzOpenAIDeviceLogin)
		t.Run("login validation", fuzzOpenAILoginValidation)
		t.Run("browser opener", fuzzOpenAIBrowserOpener)
		t.Run("mode selection", fuzzOpenAILoginMode)
		t.Run("redirect reader", fuzzOpenAIRedirectReader)
		t.Run("logout", fuzzOpenAILogout)
		t.Run("status", fuzzOpenAIStatus)
		t.Run("status formatting", fuzzOpenAIStatusFormatting)
	})
}

type fakeLoginService struct{ status authopenai.AuthStatus }

func (s fakeLoginService) Login(context.Context, string, string) (authopenai.AuthStatus, error) {
	return s.status, nil
}

type fakeDeviceLoginService struct{ status authopenai.AuthStatus }

func (s fakeDeviceLoginService) LoginWithDevice(context.Context, string, string, func(authopenai.DeviceCode)) (authopenai.AuthStatus, error) {
	return s.status, nil
}

type fakeStoredAuthService struct{}

func (fakeStoredAuthService) Logout(string, string) (bool, error) { return true, nil }
func (fakeStoredAuthService) Status(string, string) (authopenai.AuthStatus, error) {
	return authopenai.AuthStatus{}, nil
}

func fuzzOpenAIDefaultFactories(t *testing.T) {
	originalLoginFactory, originalDeviceFactory, originalStoredFactory := openAILoginServiceFactory, openAIDeviceLoginServiceFactory, openAIStoredAuthServiceFactory
	originalLoginAction, originalDeviceAction, originalLogoutAction, originalStatusAction := openAILoginAction, openAIDeviceLoginAction, openAILogoutAction, openAIStatusAction
	t.Cleanup(func() {
		openAILoginServiceFactory, openAIDeviceLoginServiceFactory, openAIStoredAuthServiceFactory = originalLoginFactory, originalDeviceFactory, originalStoredFactory
		openAILoginAction, openAIDeviceLoginAction, openAILogoutAction, openAIStatusAction = originalLoginAction, originalDeviceAction, originalLogoutAction, originalStatusAction
	})
	// Construct the real services to cover the default-preserving factories;
	// construction performs no IO or network calls.
	_ = originalLoginFactory(func(string) error { return nil }, func(context.Context) (string, error) { return "", nil })
	_ = originalDeviceFactory(func() {})
	_ = originalStoredFactory()
	openAILoginServiceFactory = func(func(string) error, func(context.Context) (string, error)) openAILoginService {
		return fakeLoginService{}
	}
	openAIDeviceLoginServiceFactory = func(func()) openAIDeviceLoginService { return fakeDeviceLoginService{} }
	openAIStoredAuthServiceFactory = func() openAIStoredAuthService { return fakeStoredAuthService{} }
	if _, err := originalLoginAction(context.Background(), "state", "instance", func(string) error { return nil }, func(context.Context) (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := originalDeviceAction(context.Background(), "state", "instance", func(authopenai.DeviceCode) {}, func() {}); err != nil {
		t.Fatal(err)
	}
	if _, err := originalLogoutAction("state", "instance"); err != nil {
		t.Fatal(err)
	}
	if _, err := originalStatusAction("state", "instance"); err != nil {
		t.Fatal(err)
	}
}

func fuzzOpenAIDispatch(t *testing.T) {
	oldLogin, oldLogout, oldStatus := openAILoginAction, openAILogoutAction, openAIStatusAction
	t.Cleanup(func() { openAILoginAction, openAILogoutAction, openAIStatusAction = oldLogin, oldLogout, oldStatus })
	status := authopenai.AuthStatus{Source: authopenai.AuthSourceOAuth}
	openAILoginAction = func(context.Context, string, string, func(string) error, func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
		return status, nil
	}
	openAILogoutAction = func(string, string) (bool, error) { return true, nil }
	openAIStatusAction = func(string, string) (authopenai.AuthStatus, error) { return status, nil }

	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}, {"login", "--no-device", "--state-dir", t.TempDir()}, {"logout", "--state-dir", t.TempDir()}, {"status", "--state-dir", t.TempDir()}} {
		var stdout, stderr bytes.Buffer
		if err := runOpenAI(args, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("runOpenAI(%v): %v", args, err)
		}
	}
	if err := runOpenAI([]string{"unknown"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("unknown command succeeded")
	}
}

func fuzzOpenAIBrowserLogin(t *testing.T) {
	oldAction, oldOpen := openAILoginAction, openAIBrowserOpener
	t.Cleanup(func() { openAILoginAction, openAIBrowserOpener = oldAction, oldOpen })
	openAIBrowserOpener = func(string) error { return errors.New("no browser") }
	openAILoginAction = func(ctx context.Context, stateDir, instance string, open func(string) error, read func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
		if instance != "openai" || stateDir == "" {
			t.Fatalf("bad resolved arguments: %q %q", stateDir, instance)
		}
		if err := open("https://example.test/auth"); err != nil {
			t.Fatal(err)
		}
		got, err := read(ctx)
		if err != nil || got != "https://localhost/callback" {
			t.Fatalf("redirect = %q, %v", got, err)
		}
		return authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth}, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runOpenAILogin([]string{"--no-device", "--instance", " ", "--dir", t.TempDir()}, strings.NewReader(" https://localhost/callback \n"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "browser_open_error=") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	openAILoginAction = func(context.Context, string, string, func(string) error, func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
		return authopenai.AuthStatus{}, errors.New("login failed")
	}
	if err := runOpenAILogin([]string{"--no-device", "--state-dir", t.TempDir()}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("login error suppressed")
	}
}

func fuzzOpenAIDeviceLogin(t *testing.T) {
	old := openAIDeviceLoginAction
	t.Cleanup(func() { openAIDeviceLoginAction = old })
	openAIDeviceLoginAction = func(_ context.Context, _, instance string, prompt func(authopenai.DeviceCode), concurrent func()) (authopenai.AuthStatus, error) {
		if instance != "named" {
			t.Fatalf("instance = %q", instance)
		}
		prompt(authopenai.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "ABCD"})
		concurrent()
		return authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth}, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runOpenAILogin([]string{"--device", "--instance", " named ", "--state-dir", t.TempDir()}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"device_code_url=", "device_code=ABCD", "concurrent_login=detected", "state=signed-in"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q: %q", want, stdout.String())
		}
	}
	openAIDeviceLoginAction = func(context.Context, string, string, func(authopenai.DeviceCode), func()) (authopenai.AuthStatus, error) {
		return authopenai.AuthStatus{}, errors.New("device failed")
	}
	if err := runOpenAIDeviceLogin(context.Background(), "state", "openai", io.Discard, io.Discard); err == nil {
		t.Fatal("device error suppressed")
	}
}

func fuzzOpenAILoginValidation(t *testing.T) {
	for _, args := range [][]string{{"--bad"}, {"extra"}, {"--device", "--no-device"}} {
		if err := runOpenAILogin(args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatalf("runOpenAILogin(%v) succeeded", args)
		}
	}
	var stderr bytes.Buffer
	if err := runOpenAILogin([]string{"-h"}, strings.NewReader(""), io.Discard, &stderr); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	oldResolve := resolveOpenAIStateDirAction
	t.Cleanup(func() { resolveOpenAIStateDirAction = oldResolve })
	resolveOpenAIStateDirAction = func(string, string) (string, error) { return "", errors.New("resolve failed") }
	if err := runOpenAILogin([]string{"--no-device"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("resolve error suppressed")
	}
}

func fuzzOpenAIBrowserOpener(t *testing.T) {
	oldOS, oldExec := openAIRuntimeGOOS, openAIExecCommand
	t.Cleanup(func() { openAIRuntimeGOOS, openAIExecCommand = oldOS, oldExec })
	var calls [][]string
	openAIExecCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	for _, goos := range []string{"darwin", "windows", "linux"} {
		openAIRuntimeGOOS = goos
		if err := openAIBrowserOpener("https://example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v", calls)
	}
	openAIExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("definitely-not-a-real-serf-command") }
	if err := openAIBrowserOpener("x"); err == nil {
		t.Fatal("start error suppressed")
	}
}

func fuzzOpenAILoginMode(t *testing.T) {
	for _, tc := range []struct{ device, browser bool }{{true, false}, {false, true}} {
		chooseLoginMode(tc.device, tc.browser)
	}
	env := func(values map[string]string) func(string) string { return func(k string) string { return values[k] } }
	for _, tc := range []struct {
		goos   string
		values map[string]string
	}{
		{"linux", map[string]string{"SERF_LOGIN_HEADLESS": "yes"}}, {"linux", map[string]string{"SERF_LOGIN_HEADLESS": "off", "SSH_TTY": "x"}},
		{"linux", map[string]string{"SSH_CONNECTION": "x"}}, {"darwin", nil}, {"windows", nil}, {"linux", nil},
		{"linux", map[string]string{"DISPLAY": ":0"}}, {"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}},
	} {
		_ = isHeadlessLoginFor(tc.goos, env(tc.values))
	}
	_ = isHeadlessLogin()
	old := isHeadlessLoginAction
	t.Cleanup(func() { isHeadlessLoginAction = old })
	isHeadlessLoginAction = func() bool { return true }
	chooseLoginMode(false, false)
	isHeadlessLoginAction = func() bool { return false }
	chooseLoginMode(false, false)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func fuzzOpenAIRedirectReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := makeRedirectURLReader(strings.NewReader("x\n"), io.Discard)(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if _, err := makeRedirectURLReader(errReader{}, io.Discard)(context.Background()); err == nil {
		t.Fatal("read error suppressed")
	}
	if _, err := makeRedirectURLReader(strings.NewReader(" \n"), io.Discard)(context.Background()); err == nil {
		t.Fatal("empty redirect accepted")
	}
	if got, err := makeRedirectURLReader(strings.NewReader(" url "), io.Discard)(context.Background()); err != nil || got != "url" {
		t.Fatalf("redirect = %q, %v", got, err)
	}
	if got, err := resolveOpenAIStateDir("ignored", " override "); err != nil || got != " override " {
		t.Fatalf("state = %q, %v", got, err)
	}
	if got, err := resolveOpenAIStateDir("ignored", ""); err != nil || got == "" {
		t.Fatalf("default state = %q, %v", got, err)
	}
}

func fuzzOpenAILogout(t *testing.T) {
	oldLogout, oldStatus := openAILogoutAction, openAIStatusAction
	t.Cleanup(func() { openAILogoutAction, openAIStatusAction = oldLogout, oldStatus })
	for _, args := range [][]string{{"--bad"}, {"extra"}, {"-h"}} {
		if err := runOpenAILogout(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("logout %v succeeded", args)
		}
	}
	openAILogoutAction = func(string, string) (bool, error) { return false, errors.New("delete failed") }
	if err := runOpenAILogout([]string{"--state-dir", t.TempDir()}, io.Discard, io.Discard); err == nil {
		t.Fatal("delete error suppressed")
	}
	openAILogoutAction = func(string, string) (bool, error) { return true, nil }
	openAIStatusAction = func(string, string) (authopenai.AuthStatus, error) {
		return authopenai.AuthStatus{}, errors.New("status failed")
	}
	if err := runOpenAILogout([]string{"--state-dir", t.TempDir()}, io.Discard, io.Discard); err == nil {
		t.Fatal("status error suppressed")
	}
	openAIStatusAction = func(_ string, instance string) (authopenai.AuthStatus, error) {
		if instance != "openai" {
			t.Fatalf("instance=%q", instance)
		}
		return authopenai.AuthStatus{}, nil
	}
	if err := runOpenAILogout([]string{"--instance", " "}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	oldResolve := resolveOpenAIStateDirAction
	t.Cleanup(func() { resolveOpenAIStateDirAction = oldResolve })
	resolveOpenAIStateDirAction = func(string, string) (string, error) { return "", errors.New("resolve failed") }
	if err := runOpenAILogout(nil, io.Discard, io.Discard); err == nil {
		t.Fatal("resolve error suppressed")
	}
}

func fuzzOpenAIStatus(t *testing.T) {
	old := openAIStatusAction
	t.Cleanup(func() { openAIStatusAction = old })
	for _, args := range [][]string{{"--bad"}, {"extra"}, {"-h"}} {
		if err := runOpenAIStatus(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("status %v succeeded", args)
		}
	}
	openAIStatusAction = func(string, string) (authopenai.AuthStatus, error) {
		return authopenai.AuthStatus{}, errors.New("status failed")
	}
	if err := runOpenAIStatus([]string{"--state-dir", t.TempDir()}, io.Discard, io.Discard); err == nil {
		t.Fatal("status error suppressed")
	}
	openAIStatusAction = func(_ string, instance string) (authopenai.AuthStatus, error) {
		if instance != "openai" {
			t.Fatalf("instance=%q", instance)
		}
		return authopenai.AuthStatus{}, nil
	}
	if err := runOpenAIStatus([]string{"--instance", " "}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	oldResolve := resolveOpenAIStateDirAction
	t.Cleanup(func() { resolveOpenAIStateDirAction = oldResolve })
	resolveOpenAIStateDirAction = func(string, string) (string, error) { return "", errors.New("resolve failed") }
	if err := runOpenAIStatus(nil, io.Discard, io.Discard); err == nil {
		t.Fatal("resolve error suppressed")
	}
}

func fuzzOpenAIStatusFormatting(t *testing.T) {
	got := formatOpenAIStatus(authopenai.AuthStatus{})
	if got != "state=signed-out source=signed-out" {
		t.Fatalf("empty status = %q", got)
	}
	got = formatOpenAIStatus(authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth, Email: "e", AccountID: "a", WorkspaceID: "w", Expiry: time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600)), NeedsRefresh: true, NeedsLogin: true})
	for _, want := range []string{"state=signed-in", "email=e", "account_id=a", "workspace_id=w", "expiry=2030-01-02T02:04:05Z", "needs_refresh=true", "needs_login=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q: %q", want, got)
		}
	}
}
