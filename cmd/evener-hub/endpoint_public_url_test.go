package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/hubendpoint"
)

const publicationToken = "disposable-publication-token"

// A wrong public-origin branch must never turn an HTTPS advertisement into a TLS listener.
func TestPublicURLDoesNotChangeListener(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("public_url = \"https://hub.example.test:9443\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	report, stop := startPublicationHub(t, path, state, io.Discard)
	defer stop()
	if report.ConfiguredListenAddr != "127.0.0.1:0" || report.Running.RequestedListenAddr != "127.0.0.1:0" || report.Running.PublicURL != "https://hub.example.test:9443" || report.PublicURL != "https://hub.example.test:9443" {
		t.Fatalf("wrong configured/runtime publication: %+v", report)
	}
	var endpointJSON, endpointErrors bytes.Buffer
	if code := Run([]string{"endpoint", "--json", "--config", path}, nil, &endpointJSON, &endpointErrors); code != 0 {
		t.Fatalf("endpoint JSON exited %d", code)
	}
	if bytes.Contains(endpointJSON.Bytes(), []byte(publicationToken)) || bytes.Contains(endpointErrors.Bytes(), []byte(publicationToken)) {
		t.Fatal("token leaked in endpoint JSON or diagnostics")
	}
	host, port, err := net.SplitHostPort(report.Running.BoundAddr)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("wrong actual listener: %q, %v", report.Running.BoundAddr, err)
	}
	response, err := http.Get(report.Running.LocalURL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("local HTTP status %d", response.StatusCode)
	}
}

// Removing publication from either output or the invitation must break this assertion.
func TestPublicURLSharedByStartupAndPairing(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("public_url = \"https://hub.example.test:9443\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "hub.log")
	logs, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var web *WebServer
	report, stop := startPublicationHubWithWeb(t, path, state, logs, &web)
	invite, err := pairingRPC[appwire.DevicePairingInvitation](t, web, appwire.MethodEvenerDevicePairingCreate, appwire.DevicePairingCreateParams{})
	if err != nil || invite.Origin != "https://hub.example.test:9443" {
		stop()
		t.Fatalf("default invitation: %+v, %v", invite, err)
	}
	stop()
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(publicationToken)) || !bytes.Contains(data, []byte(report.Running.PublicURL)) {
		t.Fatal("background output either disclosed credential or missed advertised origin")
	}
}

func TestInvitationOverrideWinsPublicURL(t *testing.T) {
	p := newDevicePairings(hubcore.WebConfig{AuthToken: publicationToken, PublicURL: "https://hub.example.test:9443"})
	invite, err := p.create(appwire.DevicePairingCreateParams{Origin: "http://192.0.2.20:9180"})
	if err != nil || invite.Origin != "http://192.0.2.20:9180" {
		t.Fatalf("explicit invitation: %+v, %v", invite, err)
	}
}

func TestStartupLoginOutput(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	report, stop := startPublicationHub(t, path, state, writer)
	stop()
	writer.Close()
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(publicationToken)) || !bytes.Contains(data, []byte(report.Running.LocalURL)) {
		t.Fatal("child capture leaked token or omitted plain connection URL")
	}
	if isTerminalWriter(writer) || isTerminalWriter(&bytes.Buffer{}) || isTerminalWriter(io.Discard) {
		t.Fatal("noninteractive writer classified as terminal")
	}
	file, err := os.Create(filepath.Join(root, "redirected.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if isTerminalWriter(file) {
		t.Fatal("regular file classified as terminal")
	}
}

func TestLoginURLCommand(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	tokenPath := filepath.Join(state, hubedge.TokenFileName)
	if err := os.WriteFile(tokenPath, []byte(publicationToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, stop := startPublicationHub(t, path, state, io.Discard)
	defer stop()
	// The currently edited origin cannot replace the one this process published.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("public_url = \"https://other.example.test\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"login-url", "--config", path}, nil, &out, &stderr); code != 0 {
		t.Fatalf("CLI exit %d", code)
	}
	want := report.Running.LocalURL + "/auth/" + publicationToken + "\n"
	if out.String() != want || bytes.Contains(stderr.Bytes(), []byte(publicationToken)) {
		t.Fatal("command did not return actual running instance's URL safely")
	}
	// Follow the deliberate link against the actual HTTP listener with a fresh cookie jar.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login, err := url.Parse(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(login.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound || len(jar.Cookies(login)) == 0 {
		t.Fatalf("browser login status %d", response.StatusCode)
	}
	api, err := client.Get(report.Running.LocalURL + "/api/debug/subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	api.Body.Close()
	if api.StatusCode != http.StatusOK {
		t.Fatalf("cookie did not authenticate subsequent API access: status %d", api.StatusCode)
	}
	withoutCookie, err := http.Get(report.Running.LocalURL + "/api/debug/subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	withoutCookie.Body.Close()
	if withoutCookie.StatusCode != http.StatusUnauthorized {
		t.Fatalf("protected API without cookie: status %d", withoutCookie.StatusCode)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil || string(token) != publicationToken+"\n" {
		t.Fatalf("token changed: %v", err)
	}
	config, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(config, beforeConfig) {
		t.Fatalf("config changed by command: %v", err)
	}
	if report.Running.BoundAddr == "127.0.0.1:0" {
		t.Fatal("command used unbound port")
	}
}

func TestLoginURLCommandStopped(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	var out, stderr bytes.Buffer
	err := runLoginURL([]string{"--config", path}, &out, &stderr, defaultMainDeps())
	if !errors.Is(err, ErrLoginURLUnavailable) || out.Len() != 0 {
		t.Fatalf("stopped: err=%v stdout=%q", err, out.String())
	}
	for _, name := range []string{"hub.lock", "auth-token", "hub-endpoint.json"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("created %s: %v", name, err)
		}
	}
}

func startPublicationHub(t *testing.T, path, state string, stderr io.Writer) (hubendpoint.Report, func()) {
	return startPublicationHubWithWeb(t, path, state, stderr, nil)
}

func startPublicationHubWithWeb(t *testing.T, path, state string, stderr io.Writer, webOut **WebServer) (hubendpoint.Report, func()) {
	t.Helper()
	deps := defaultMainDeps()
	deps.ensureDirs = func() error { return nil }
	deps.loadRegistry = hermeticRegistryLoader
	deps.newToken = func() (string, error) { return "spawn-only-sentinel", nil }
	deps.loadAuthToken = func(root string) (string, error) {
		if root != state {
			return "", errors.New("wrong state root")
		}
		return publicationToken, nil
	}
	deps.loadCredentials = func(string) (*credentials.Store, error) { return &credentials.Store{}, nil }
	deps.startLivePrefetch = func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func()) {}
	deps.startLaunchPrefetch = func(context.Context, *WebServer, func(func())) {}
	if webOut != nil {
		deps.afterWeb = func(web *WebServer) { *webOut = web }
	}
	return startPublicationHubWithDeps(t, path, stderr, deps)
}

func startPublicationHubWithDeps(t *testing.T, path string, stderr io.Writer, deps mainDeps) (hubendpoint.Report, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} }
	served := make(chan struct{})
	deps.serve = func(ctx context.Context, srv hubHTTPServer) error { close(served); return serveHub(ctx, srv) }
	done := make(chan error, 1)
	go func() { done <- runMain([]string{"--config", path}, stderr, deps) }()
	select {
	case <-served:
	case err := <-done:
		t.Fatalf("hub failed startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("hub startup timed out")
	}
	report := endpointCommand(t, path)
	if report.State != "running" || report.Running == nil {
		t.Fatalf("not a verified live instance: %+v", report)
	}
	return report, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("hub shutdown timed out")
		}
	}
}
