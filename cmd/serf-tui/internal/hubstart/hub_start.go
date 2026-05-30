package hubstart

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/binresolve"
	"primeradiant.com/serf/internal/hubapi"
)

const DefaultHubAddr = "127.0.0.1:9180"

type HubAddress struct {
	BaseURL  string
	BindAddr string
	IsLocal  bool
}

type HubStartConfig struct {
	RawAddr             string
	HubBin              string
	StateDir            string
	LogFile             string
	AuthToken           string
	CurrentExecutable   string
	AutoStart           bool
	HealthTimeout       time.Duration
	HTTPClient          *http.Client
	LookPath            func(string) (string, error)
	DialHub             func(context.Context, HubAddress, *http.Client) (*appwire.Client, error)
	StartLocalHub       func(HubStartRequest) error
	CheckHubEnvironment func(context.Context, HubAddress, *http.Client, string) error
}

type HubRuntime struct {
	Address HubAddress
	Client  *appwire.Client
}

type TUIStartupOptions struct {
	HubAddr      string
	HubBin       string
	StateDir     string
	LogFile      string
	AuthToken    string
	AutoStartHub bool
	Debug        bool
}

func ParseTUIStartupOptions(args []string, getenv func(string) string) (TUIStartupOptions, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	opts := TUIStartupOptions{
		HubAddr:      EnvDefault(getenv, "SERF_HUB_ADDR", DefaultHubAddr),
		HubBin:       getenv("SERF_HUB_BIN"),
		StateDir:     getenv("SERF_STATE_DIR"),
		LogFile:      getenv("SERF_TUI_LOG_FILE"),
		AuthToken:    getenv("SERF_HUB_AUTH_TOKEN"),
		AutoStartHub: true,
	}
	fs := flag.NewFlagSet("serf-tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.HubAddr, "hub-addr", opts.HubAddr, "serf hub address")
	fs.StringVar(&opts.HubBin, "hub-bin", opts.HubBin, "path to serf-hub binary")
	noAutoStartHub := false
	fs.BoolVar(&noAutoStartHub, "no-auto-start-hub", false, "do not start a local hub when unreachable")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "override Serf state directory")
	fs.StringVar(&opts.LogFile, "log-file", opts.LogFile, "write startup diagnostics to this file")
	fs.StringVar(&opts.AuthToken, "auth-token", opts.AuthToken, "hub capability token (overrides SERF_HUB_AUTH_TOKEN and token file)")
	fs.BoolVar(&opts.Debug, "debug", opts.Debug, "disable alternate screen")
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintf(w, "Usage: serf-tui [flags]\n\n")
		fmt.Fprintf(w, "Serf TUI — interactive terminal UI for serf-hub.\n\n")
		fmt.Fprintf(w, "Flags:\n")
		fmt.Fprintf(w, "  --hub-addr <addr>        serf hub address (default: %s)\n", opts.HubAddr)
		fmt.Fprintf(w, "  --hub-bin <path>         path to serf-hub binary\n")
		fmt.Fprintf(w, "  --no-auto-start-hub      do not start a local hub when unreachable\n")
		fmt.Fprintf(w, "  --state-dir <path>       override Serf state directory\n")
		fmt.Fprintf(w, "  --log-file <path>        write startup diagnostics to this file\n")
		fmt.Fprintf(w, "  --auth-token <token>     hub capability token (overrides SERF_HUB_AUTH_TOKEN and token file)\n")
		fmt.Fprintf(w, "  --debug                  disable alternate screen\n\n")
		fmt.Fprintf(w, "Environment variables:\n")
		fmt.Fprintf(w, "  SERF_HUB_ADDR            default value for --hub-addr\n")
		fmt.Fprintf(w, "  SERF_HUB_BIN             default value for --hub-bin\n")
		fmt.Fprintf(w, "  SERF_STATE_DIR           default value for --state-dir\n")
		fmt.Fprintf(w, "  SERF_TUI_LOG_FILE        default value for --log-file\n")
		fmt.Fprintf(w, "  SERF_HUB_AUTH_TOKEN      default value for --auth-token\n")
	}
	if err := fs.Parse(args); err != nil {
		return TUIStartupOptions{}, err
	}
	if noAutoStartHub {
		opts.AutoStartHub = false
	}
	return opts, nil
}

// ResolveAuthToken determines the hub auth token using the resolution order:
// explicit value (from flag or env) → token file → empty (with warning).
// The stateDir is used to locate the token file; if empty, $HOME/.serf is used.
func ResolveAuthToken(explicit, stateDir string) string {
	if explicit != "" {
		return explicit
	}
	tokenFile := AuthTokenFilePath(stateDir)
	data, err := os.ReadFile(tokenFile)
	if err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok
		}
	}
	fmt.Fprintf(os.Stderr, "serf-tui: warning: no hub auth token found (checked %s); proceeding without auth\n", tokenFile)
	return ""
}

// AuthTokenFilePath returns the path to the hub auth-token file.
func AuthTokenFilePath(stateDir string) string {
	if stateDir != "" {
		return filepath.Join(filepath.Clean(stateDir), "auth-token")
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "serf", "auth-token")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".serf", "auth-token")
	}
	return filepath.Join(home, ".serf", "auth-token")
}

// bearerTransport is an http.RoundTripper that injects an Authorization header.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// HTTPClientWithBearer returns an *http.Client that attaches an Authorization
// bearer token to every request. If token is empty the original client is
// returned unchanged.
func HTTPClientWithBearer(base *http.Client, token string) *http.Client {
	if token == "" {
		return base
	}
	inner := base.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	clone := *base
	clone.Transport = &bearerTransport{base: inner, token: token}
	return &clone
}

func EnvDefault(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

type StartupErrorKind string

const (
	StartupErrorMissingHubBinary  StartupErrorKind = "missing-hub-binary"
	StartupErrorBindFailure       StartupErrorKind = "bind-failure"
	StartupErrorUnhealthyHub      StartupErrorKind = "unhealthy-hub"
	StartupErrorIncompatibleAPI   StartupErrorKind = "incompatible-api"
	StartupErrorStaleEnvironment  StartupErrorKind = "stale-environment"
	StartupErrorRemoteNoAutoStart StartupErrorKind = "remote-no-autostart"
	StartupErrorHubUnavailable    StartupErrorKind = "hub-unavailable"
)

var LocalHubImmediateExitWindow = 750 * time.Millisecond

type StartupError struct {
	Kind   StartupErrorKind
	Addr   string
	Detail string
	Err    error
}

func (e StartupError) Error() string {
	detail := e.Detail
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	switch e.Kind {
	case StartupErrorMissingHubBinary:
		return "cannot find serf-hub binary: " + detail
	case StartupErrorBindFailure:
		return "hub failed to bind: " + detail
	case StartupErrorUnhealthyHub:
		return "hub is unhealthy: " + detail
	case StartupErrorIncompatibleAPI:
		return "hub API is incompatible: " + detail
	case StartupErrorStaleEnvironment:
		return "hub state/auth environment is stale: " + detail
	case StartupErrorRemoteNoAutoStart:
		return "remote hub is not reachable and cannot be auto-started: " + detail
	default:
		return "hub is not reachable: " + detail
	}
}

func (e StartupError) Unwrap() error {
	return e.Err
}

type HubStartRequest struct {
	Binary   string
	BindAddr string
	StateDir string
	LogFile  string
}

func NormalizeHubAddress(raw string) (HubAddress, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultHubAddr
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return HubAddress{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return HubAddress{}, fmt.Errorf("unsupported hub URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return HubAddress{}, fmt.Errorf("hub address must include a host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return HubAddress{
		BaseURL:  strings.TrimRight(u.String(), "/"),
		BindAddr: u.Host,
		IsLocal:  isLocalHubHost(u.Hostname()),
	}, nil
}

func isLocalHubHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func StartHubClient(ctx context.Context, cfg HubStartConfig) (HubRuntime, error) {
	fail := func(err error) (HubRuntime, error) {
		WriteStartupDiagnostic(cfg.LogFile, err)
		return HubRuntime{}, err
	}
	addr, err := NormalizeHubAddress(cfg.RawAddr)
	if err != nil {
		return fail(err)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	// Wrap the HTTP client to inject the bearer token on every request.
	// This covers both the WebSocket upgrade (nhooyr/websocket uses HTTPClient)
	// and any plain HTTP calls made via hubapi.Client.
	if cfg.AuthToken != "" {
		cfg.HTTPClient = HTTPClientWithBearer(cfg.HTTPClient, cfg.AuthToken)
	}
	if cfg.HealthTimeout == 0 {
		cfg.HealthTimeout = 5 * time.Second
	}
	if cfg.DialHub == nil {
		cfg.DialHub = dialHubRPC
	}
	if cfg.CheckHubEnvironment == nil {
		cfg.CheckHubEnvironment = checkHubEnvironment
	}
	client, err := waitForHubHealth(ctx, addr, cfg.HTTPClient, 500*time.Millisecond, cfg.DialHub)
	if err == nil {
		if err := cfg.CheckHubEnvironment(ctx, addr, cfg.HTTPClient, cfg.StateDir); err != nil {
			return fail(err)
		}
		return HubRuntime{Address: addr, Client: client}, nil
	}
	if isTerminalStartupError(err) {
		return fail(err)
	}
	if !cfg.AutoStart {
		return fail(StartupError{Kind: StartupErrorHubUnavailable, Addr: addr.BaseURL, Err: err})
	}
	if !addr.IsLocal {
		return fail(StartupError{Kind: StartupErrorRemoteNoAutoStart, Addr: addr.BaseURL, Err: err})
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	bin, err := binresolve.Resolve("serf-hub", cfg.HubBin, cfg.CurrentExecutable, cfg.LookPath)
	if err != nil {
		return fail(StartupError{Kind: StartupErrorMissingHubBinary, Addr: addr.BaseURL, Err: err})
	}
	startLocalHubFn := cfg.StartLocalHub
	if startLocalHubFn == nil {
		startLocalHubFn = StartLocalHub
	}
	if err := startLocalHubFn(HubStartRequest{
		Binary:   bin,
		BindAddr: addr.BindAddr,
		StateDir: cfg.StateDir,
		LogFile:  cfg.LogFile,
	}); err != nil {
		return fail(classifyStartHubError(addr, err))
	}
	client, err = waitForHubHealth(ctx, addr, cfg.HTTPClient, cfg.HealthTimeout, cfg.DialHub)
	if err != nil {
		if isTerminalStartupError(err) {
			return fail(err)
		}
		return fail(StartupError{Kind: StartupErrorUnhealthyHub, Addr: addr.BaseURL, Err: err})
	}
	if err := cfg.CheckHubEnvironment(ctx, addr, cfg.HTTPClient, cfg.StateDir); err != nil {
		return fail(err)
	}
	return HubRuntime{Address: addr, Client: client}, nil
}

func waitForHubHealth(ctx context.Context, addr HubAddress, httpClient *http.Client, timeout time.Duration, dialHub func(context.Context, HubAddress, *http.Client) (*appwire.Client, error)) (*appwire.Client, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, err := dialHub(waitCtx, addr, httpClient)
		cancel()
		if err == nil {
			return client, nil
		}
		if isTerminalStartupError(err) {
			return nil, err
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func dialHubRPC(ctx context.Context, addr HubAddress, httpClient *http.Client) (*appwire.Client, error) {
	transport, err := appwire.DialWebSocket(ctx, hubRPCURL(addr), httpClient)
	if err != nil {
		return nil, err
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	init, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serf-tui", Version: "tui"},
	})
	if err != nil {
		client.Close()
		return nil, err
	}
	if init.ProtocolVersion != "" && init.ProtocolVersion != appwire.ProtocolVersion {
		client.Close()
		return nil, StartupError{
			Kind:   StartupErrorIncompatibleAPI,
			Addr:   addr.BaseURL,
			Detail: fmt.Sprintf("hub speaks %q, TUI requires %q", init.ProtocolVersion, appwire.ProtocolVersion),
		}
	}
	return client, nil
}

func hubRPCURL(addr HubAddress) string {
	base := addr.BaseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + "/rpc"
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + "/rpc"
	default:
		return base + "/rpc"
	}
}

func StartLocalHub(req HubStartRequest) error {
	cmd := exec.Command(req.Binary, "--addr", req.BindAddr)
	if req.StateDir != "" {
		cmd.Env = append(os.Environ(),
			"SERF_STATE_DIR="+req.StateDir,
			"XDG_STATE_HOME="+StateHomeForSerfStateDir(req.StateDir),
		)
	}
	var out *os.File
	if req.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(req.LogFile), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(req.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		out = f
	} else {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		out = f
	}
	defer out.Close()
	var startupOutput bytes.Buffer
	cmdOut := io.MultiWriter(out, &startupOutput)
	cmd.Stdout = cmdOut
	cmd.Stderr = cmdOut
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start serf-hub: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()
	select {
	case err := <-exited:
		output := strings.TrimSpace(startupOutput.String())
		if output != "" {
			return fmt.Errorf("serf-hub exited during startup: %w: %s", err, output)
		}
		return fmt.Errorf("serf-hub exited during startup: %w", err)
	case <-time.After(LocalHubImmediateExitWindow):
	}
	return cmd.Process.Release()
}

func StateHomeForSerfStateDir(stateDir string) string {
	clean := filepath.Clean(strings.TrimSpace(stateDir))
	return filepath.Dir(clean)
}

func checkHubEnvironment(ctx context.Context, addr HubAddress, httpClient *http.Client, stateDir string) error {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return nil
	}
	client, err := hubapi.NewClient(addr.BaseURL, httpClient)
	if err != nil {
		return err
	}
	health, err := client.Health(ctx)
	if err != nil || strings.TrimSpace(health.StateGlob) == "" {
		return nil
	}
	expected := filepath.Join(filepath.Clean(stateDir), "projects", "*")
	if filepath.Clean(health.StateGlob) != filepath.Clean(expected) {
		return StartupError{
			Kind:   StartupErrorStaleEnvironment,
			Addr:   addr.BaseURL,
			Detail: fmt.Sprintf("hub indexes %s; TUI requested %s", health.StateGlob, expected),
		}
	}
	return nil
}

func classifyStartHubError(addr HubAddress, err error) error {
	kind := StartupErrorUnhealthyHub
	if looksLikeBindFailure(err) {
		kind = StartupErrorBindFailure
	}
	return StartupError{Kind: kind, Addr: addr.BaseURL, Err: err}
}

func looksLikeBindFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "bind") || strings.Contains(msg, "address already in use")
}

func isTerminalStartupError(err error) bool {
	var startupErr StartupError
	if !errors.As(err, &startupErr) {
		return false
	}
	return startupErr.Kind == StartupErrorIncompatibleAPI || startupErr.Kind == StartupErrorStaleEnvironment
}

func StartupErrorScreen(err error) string {
	var startupErr StartupError
	if !errors.As(err, &startupErr) {
		return fmt.Sprintf("Serf TUI startup failed\n\n%s\n", err)
	}
	detail := startupErr.Detail
	if detail == "" && startupErr.Err != nil {
		detail = startupErr.Err.Error()
	}
	switch startupErr.Kind {
	case StartupErrorMissingHubBinary:
		return fmt.Sprintf("Serf TUI startup failed\n\nCannot find serf-hub binary.\n%s\n", detail)
	case StartupErrorBindFailure:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub failed to bind %s.\n%s\n", startupErr.Addr, detail)
	case StartupErrorUnhealthyHub:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub started but did not become healthy at %s.\n%s\n", startupErr.Addr, detail)
	case StartupErrorIncompatibleAPI:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub API is incompatible at %s.\n%s\n", startupErr.Addr, detail)
	case StartupErrorStaleEnvironment:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub is running with a different state/auth environment at %s.\n%s\n", startupErr.Addr, detail)
	case StartupErrorRemoteNoAutoStart:
		return fmt.Sprintf("Serf TUI startup failed\n\nRemote Hub is not reachable at %s, and serf-tui only auto-starts local Hubs.\n%s\n", startupErr.Addr, detail)
	default:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub is not reachable at %s.\n%s\n", startupErr.Addr, detail)
	}
}

func WriteStartupDiagnostic(logFile string, err error) {
	logFile = strings.TrimSpace(logFile)
	if logFile == "" || err == nil {
		return
	}
	if mkErr := os.MkdirAll(filepath.Dir(logFile), 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	kind := StartupErrorKind("unknown")
	var startupErr StartupError
	if errors.As(err, &startupErr) {
		kind = startupErr.Kind
	}
	fmt.Fprintf(f, "serf-tui startup failed kind=%s error=%s\n", kind, err)
}
