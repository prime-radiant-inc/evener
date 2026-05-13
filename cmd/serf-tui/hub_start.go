package main

import (
	"context"
	"fmt"
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
)

const defaultHubAddr = "127.0.0.1:9180"

type hubAddress struct {
	BaseURL  string
	BindAddr string
	IsLocal  bool
}

type hubStartConfig struct {
	RawAddr           string
	HubBin            string
	LogFile           string
	CurrentExecutable string
	AutoStart         bool
	HealthTimeout     time.Duration
	HTTPClient        *http.Client
	LookPath          func(string) (string, error)
}

type hubRuntime struct {
	Address hubAddress
	Client  *appwire.Client
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
	addr, err := normalizeHubAddress(cfg.RawAddr)
	if err != nil {
		return hubRuntime{}, err
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.HealthTimeout == 0 {
		cfg.HealthTimeout = 5 * time.Second
	}
	client, err := waitForHubHealth(ctx, addr, cfg.HTTPClient, 500*time.Millisecond)
	if err == nil {
		return hubRuntime{Address: addr, Client: client}, nil
	}
	if !cfg.AutoStart {
		return hubRuntime{}, fmt.Errorf("hub is not reachable at %s", addr.BaseURL)
	}
	if !addr.IsLocal {
		return hubRuntime{}, fmt.Errorf("hub is not reachable at %s and auto-start is only supported for local hubs", addr.BaseURL)
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	bin, err := resolveHubBinary(cfg.HubBin, cfg.CurrentExecutable, cfg.LookPath)
	if err != nil {
		return hubRuntime{}, err
	}
	if err := startLocalHub(bin, addr.BindAddr, cfg.LogFile); err != nil {
		return hubRuntime{}, err
	}
	client, err = waitForHubHealth(ctx, addr, cfg.HTTPClient, cfg.HealthTimeout)
	if err != nil {
		return hubRuntime{}, fmt.Errorf("started hub but health check failed: %w", err)
	}
	return hubRuntime{Address: addr, Client: client}, nil
}

func waitForHubHealth(ctx context.Context, addr hubAddress, httpClient *http.Client, timeout time.Duration) (*appwire.Client, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, err := dialHubRPC(waitCtx, addr, httpClient)
		cancel()
		if err == nil {
			return client, nil
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
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serf-tui", Version: "tui"},
	}); err != nil {
		client.Close()
		return nil, err
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

func startLocalHub(binary, bindAddr, logFile string) error {
	cmd := exec.Command(binary, "--addr", bindAddr)
	var out *os.File
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
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
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start serf-hub: %w", err)
	}
	return cmd.Process.Release()
}
