package main

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
	"runtime"
	"strings"
	"time"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/hubapi"
)

const defaultHubAddr = "127.0.0.1:9180"

type hubAddress struct {
	BaseURL  string
	BindAddr string
	IsLocal  bool
}

type hubStartConfig struct {
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
	DialHub             func(context.Context, hubAddress, *http.Client) (*appwire.Client, error)
	StartLocalHub       func(hubStartRequest) error
	CheckHubEnvironment func(context.Context, hubAddress, *http.Client, string) error
}

type hubRuntime struct {
	Address hubAddress
	Client  *appwire.Client
}

type tuiStartupOptions struct {
	HubAddr      string
	HubBin       string
	StateDir     string
	LogFile      string
	AuthToken    string
	AutoStartHub bool
	Debug        bool
}

func parseTUIStartupOptions(args []string, getenv func(string) string) (tuiStartupOptions, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	opts := tuiStartupOptions{
		HubAddr:      envDefault(getenv, "SERF_HUB_ADDR", defaultHubAddr),
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
		fmt.Fprintf(w, "  --hub-addr <addr>        serf hub address (default: %s)\n", defaultHubAddr)
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
		return tuiStartupOptions{}, err
	}
	if noAutoStartHub {
		opts.AutoStartHub = false
	}
	return opts, nil
}

// resolveAuthToken determines the hub auth token using the resolution order:
// explicit value (from flag or env) → token file → empty (with warning).
// The stateDir is used to locate the token file; if empty, $HOME/.serf is used.
func resolveAuthToken(explicit, stateDir string) string {
	if explicit != "" {
		return explicit
	}
	tokenFile := authTokenFilePath(stateDir)
	data, err := os.ReadFile(tokenFile)
	if err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok
		}
	}
	fmt.Fprintf(os.Stderr, "serf-tui: warning: no hub auth token found (checked %s); proceeding without auth\n", tokenFile)
	return ""
}

// authTokenFilePath returns the path to the hub auth-token file.
func authTokenFilePath(stateDir string) string {
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

// httpClientWithBearer returns an *http.Client that attaches an Authorization
// bearer token to every request. If token is empty the original client is
// returned unchanged.
func httpClientWithBearer(base *http.Client, token string) *http.Client {
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

func envDefault(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

type startupErrorKind string

const (
	startupErrorMissingHubBinary  startupErrorKind = "missing-hub-binary"
	startupErrorBindFailure       startupErrorKind = "bind-failure"
	startupErrorUnhealthyHub      startupErrorKind = "unhealthy-hub"
	startupErrorIncompatibleAPI   startupErrorKind = "incompatible-api"
	startupErrorStaleEnvironment  startupErrorKind = "stale-environment"
	startupErrorRemoteNoAutoStart startupErrorKind = "remote-no-autostart"
	startupErrorHubUnavailable    startupErrorKind = "hub-unavailable"
)

var localHubImmediateExitWindow = 750 * time.Millisecond

type startupError struct {
	Kind   startupErrorKind
	Addr   string
	Detail string
	Err    error
}

func (e startupError) Error() string {
	detail := e.Detail
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	switch e.Kind {
	case startupErrorMissingHubBinary:
		return "cannot find serf-hub binary: " + detail
	case startupErrorBindFailure:
		return "hub failed to bind: " + detail
	case startupErrorUnhealthyHub:
		return "hub is unhealthy: " + detail
	case startupErrorIncompatibleAPI:
		return "hub API is incompatible: " + detail
	case startupErrorStaleEnvironment:
		return "hub state/auth environment is stale: " + detail
	case startupErrorRemoteNoAutoStart:
		return "remote hub is not reachable and cannot be auto-started: " + detail
	default:
		return "hub is not reachable: " + detail
	}
}

func (e startupError) Unwrap() error {
	return e.Err
}

type hubStartRequest struct {
	Binary   string
	BindAddr string
	StateDir string
	LogFile  string
}

func normalizeHubAddress(raw string) (hubAddress, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultHubAddr
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return hubAddress{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return hubAddress{}, fmt.Errorf("unsupported hub URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return hubAddress{}, fmt.Errorf("hub address must include a host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return hubAddress{
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

func startHubClient(ctx context.Context, cfg hubStartConfig) (hubRuntime, error) {
	fail := func(err error) (hubRuntime, error) {
		writeStartupDiagnostic(cfg.LogFile, err)
		return hubRuntime{}, err
	}
	addr, err := normalizeHubAddress(cfg.RawAddr)
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
		cfg.HTTPClient = httpClientWithBearer(cfg.HTTPClient, cfg.AuthToken)
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
		return hubRuntime{Address: addr, Client: client}, nil
	}
	if isTerminalStartupError(err) {
		return fail(err)
	}
	if !cfg.AutoStart {
		return fail(startupError{Kind: startupErrorHubUnavailable, Addr: addr.BaseURL, Err: err})
	}
	if !addr.IsLocal {
		return fail(startupError{Kind: startupErrorRemoteNoAutoStart, Addr: addr.BaseURL, Err: err})
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	bin, err := resolveHubBinary(cfg.HubBin, cfg.CurrentExecutable, cfg.LookPath)
	if err != nil {
		return fail(startupError{Kind: startupErrorMissingHubBinary, Addr: addr.BaseURL, Err: err})
	}
	startLocalHubFn := cfg.StartLocalHub
	if startLocalHubFn == nil {
		startLocalHubFn = startLocalHub
	}
	if err := startLocalHubFn(hubStartRequest{
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
		return fail(startupError{Kind: startupErrorUnhealthyHub, Addr: addr.BaseURL, Err: err})
	}
	if err := cfg.CheckHubEnvironment(ctx, addr, cfg.HTTPClient, cfg.StateDir); err != nil {
		return fail(err)
	}
	return hubRuntime{Address: addr, Client: client}, nil
}

func waitForHubHealth(ctx context.Context, addr hubAddress, httpClient *http.Client, timeout time.Duration, dialHub func(context.Context, hubAddress, *http.Client) (*appwire.Client, error)) (*appwire.Client, error) {
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

func dialHubRPC(ctx context.Context, addr hubAddress, httpClient *http.Client) (*appwire.Client, error) {
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
		return nil, startupError{
			Kind:   startupErrorIncompatibleAPI,
			Addr:   addr.BaseURL,
			Detail: fmt.Sprintf("hub speaks %q, TUI requires %q", init.ProtocolVersion, appwire.ProtocolVersion),
		}
	}
	return client, nil
}

func hubRPCURL(addr hubAddress) string {
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

// resolveHubBinary returns the path to serf-hub. Resolution order:
//  1. explicit path (from --hub-bin or SERF_HUB_BIN)
//  2. sibling next to the currently running serf-tui binary
//  3. PATH lookup via lookPath
//
// The sibling-resolution step canonicalises currentExecutable via
// filepath.Abs + filepath.EvalSymlinks so that an invocation like
// "./serf-tui" (or a symlink such as /usr/local/bin/serf-tui ->
// /opt/serf/serf-tui) still finds the binary that sits next to the real
// file. Returning an absolute path also avoids exec.ErrDot when the
// caller hands the path off to exec.Command.
func resolveHubBinary(explicitPath, currentExecutable string, lookPath func(string) (string, error)) (string, error) {
	if explicitPath != "" {
		if !isExecutable(explicitPath) {
			return "", fmt.Errorf("hub binary is not executable: %s", explicitPath)
		}
		return explicitPath, nil
	}
	name := "serf-hub"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if dir, ok := siblingDir(currentExecutable); ok {
		sibling := filepath.Join(dir, name)
		if isExecutable(sibling) {
			return sibling, nil
		}
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(name)
	if err != nil {
		return "", fmt.Errorf("find serf-hub: %w", err)
	}
	return path, nil
}

// siblingDir returns the directory of the running serf-tui binary as an
// absolute, symlink-resolved path. The currentExecutable argument is
// typically obtained from os.Executable() in main(); callers may pass a
// fixture path in tests. The path is canonicalised via filepath.Abs and
// filepath.EvalSymlinks so that a relative invocation like "./serf-tui"
// (which would trip exec.ErrDot) or a symlink such as
// /usr/local/bin/serf-tui -> /opt/serf/serf-tui still resolves to the
// directory that actually holds the binary. Returns ok=false when no
// usable path can be derived.
func siblingDir(currentExecutable string) (string, bool) {
	candidate := strings.TrimSpace(currentExecutable)
	if candidate == "" {
		return "", false
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Dir(abs), true
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func startLocalHub(req hubStartRequest) error {
	cmd := exec.Command(req.Binary, "--addr", req.BindAddr)
	if req.StateDir != "" {
		cmd.Env = append(os.Environ(),
			"SERF_STATE_DIR="+req.StateDir,
			"XDG_STATE_HOME="+stateHomeForSerfStateDir(req.StateDir),
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
	case <-time.After(localHubImmediateExitWindow):
	}
	return cmd.Process.Release()
}

func stateHomeForSerfStateDir(stateDir string) string {
	clean := filepath.Clean(strings.TrimSpace(stateDir))
	return filepath.Dir(clean)
}

func checkHubEnvironment(ctx context.Context, addr hubAddress, httpClient *http.Client, stateDir string) error {
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
		return startupError{
			Kind:   startupErrorStaleEnvironment,
			Addr:   addr.BaseURL,
			Detail: fmt.Sprintf("hub indexes %s; TUI requested %s", health.StateGlob, expected),
		}
	}
	return nil
}

func classifyStartHubError(addr hubAddress, err error) error {
	kind := startupErrorUnhealthyHub
	if looksLikeBindFailure(err) {
		kind = startupErrorBindFailure
	}
	return startupError{Kind: kind, Addr: addr.BaseURL, Err: err}
}

func looksLikeBindFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "bind") || strings.Contains(msg, "address already in use")
}

func isTerminalStartupError(err error) bool {
	var startupErr startupError
	if !errors.As(err, &startupErr) {
		return false
	}
	return startupErr.Kind == startupErrorIncompatibleAPI || startupErr.Kind == startupErrorStaleEnvironment
}

func startupErrorScreen(err error) string {
	var startupErr startupError
	if !errors.As(err, &startupErr) {
		return fmt.Sprintf("Serf TUI startup failed\n\n%s\n", err)
	}
	detail := startupErr.Detail
	if detail == "" && startupErr.Err != nil {
		detail = startupErr.Err.Error()
	}
	switch startupErr.Kind {
	case startupErrorMissingHubBinary:
		return fmt.Sprintf("Serf TUI startup failed\n\nCannot find serf-hub binary.\n%s\n", detail)
	case startupErrorBindFailure:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub failed to bind %s.\n%s\n", startupErr.Addr, detail)
	case startupErrorUnhealthyHub:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub started but did not become healthy at %s.\n%s\n", startupErr.Addr, detail)
	case startupErrorIncompatibleAPI:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub API is incompatible at %s.\n%s\n", startupErr.Addr, detail)
	case startupErrorStaleEnvironment:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub is running with a different state/auth environment at %s.\n%s\n", startupErr.Addr, detail)
	case startupErrorRemoteNoAutoStart:
		return fmt.Sprintf("Serf TUI startup failed\n\nRemote Hub is not reachable at %s, and serf-tui only auto-starts local Hubs.\n%s\n", startupErr.Addr, detail)
	default:
		return fmt.Sprintf("Serf TUI startup failed\n\nHub is not reachable at %s.\n%s\n", startupErr.Addr, detail)
	}
}

func writeStartupDiagnostic(logFile string, err error) {
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
	kind := startupErrorKind("unknown")
	var startupErr startupError
	if errors.As(err, &startupErr) {
		kind = startupErr.Kind
	}
	fmt.Fprintf(f, "serf-tui startup failed kind=%s error=%s\n", kind, err)
}
