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
		AutoStartHub: true,
	}
	fs := flag.NewFlagSet("serf-tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.HubAddr, "hub-addr", opts.HubAddr, "serf hub address")
	fs.StringVar(&opts.HubBin, "hub-bin", opts.HubBin, "path to serf-hub binary")
	noAutoStartHub := false
	fs.BoolVar(&noAutoStartHub, "no-auto-start-hub", false, "do not start a local hub when unreachable")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "override Serf state directory")
	fs.StringVar(&opts.LogFile, "log-file", opts.LogFile, "write startup diagnostics to this file")
	fs.BoolVar(&opts.Debug, "debug", opts.Debug, "disable alternate screen")
	if err := fs.Parse(args); err != nil {
		return tuiStartupOptions{}, err
	}
	if noAutoStartHub {
		opts.AutoStartHub = false
	}
	return opts, nil
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
	if currentExecutable != "" {
		sibling := filepath.Join(filepath.Dir(currentExecutable), name)
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
