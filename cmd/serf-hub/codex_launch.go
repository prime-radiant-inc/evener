package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
)

type CodexLaunchConfig struct {
	ID              string            `toml:"id"`
	Binary          string            `toml:"binary"`
	WorkingDir      string            `toml:"working_dir"`
	Listen          string            `toml:"listen"`
	Args            []string          `toml:"args"`
	Env             map[string]string `toml:"env"`
	Timeout         time.Duration     `toml:"timeout"`
	BearerToken     string            `toml:"bearer_token"`
	BearerTokenFile string            `toml:"bearer_token_file"`
}

type CodexLauncher struct {
	mu      sync.Mutex
	configs map[string]CodexLaunchConfig
	running map[string]*launchedCodex
	sources map[string]appsource.Source
	client  *http.Client
}

type launchedCodex struct {
	cmd      *exec.Cmd
	endpoint string
	exited   <-chan error
}

func NewCodexLauncher(configs []CodexLaunchConfig) *CodexLauncher {
	byID := make(map[string]CodexLaunchConfig, len(configs))
	for _, cfg := range configs {
		id := strings.TrimSpace(cfg.ID)
		if id == "" {
			id = "codex"
		}
		cfg.ID = id
		byID[id] = cfg
	}
	return &CodexLauncher{
		configs: byID,
		running: map[string]*launchedCodex{},
		sources: map[string]appsource.Source{},
		client:  http.DefaultClient,
	}
}

func (l *CodexLauncher) EnsureSource(ctx context.Context, sourceID string, sources *appsource.Registry) (appsource.Source, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if source, ok := l.cachedSourceLocked(sourceID, sources); ok {
		if sources != nil {
			sources.Add(source)
		}
		return source, nil
	}
	cfg, ok := l.configs[sourceID]
	if !ok {
		return nil, appwire.HubLaunchError("codex launch not configured: " + sourceID)
	}
	launched, err := l.launchLocked(ctx, cfg)
	if err != nil {
		return nil, err
	}
	source := appsource.NewCodexSource(appsource.CodexSourceConfig{
		ID:              sourceID,
		Endpoint:        launched.endpoint,
		BearerToken:     cfg.BearerToken,
		BearerTokenFile: cfg.BearerTokenFile,
	}, l.client)
	l.running[sourceID] = launched
	l.sources[sourceID] = source
	if sources != nil {
		sources.Add(source)
	}
	return source, nil
}

func (l *CodexLauncher) Manages(sourceID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.configs[sourceID]
	return ok
}

func (l *CodexLauncher) cachedSourceLocked(sourceID string, sources *appsource.Registry) (appsource.Source, bool) {
	source, hasSource := l.sources[sourceID]
	launched := l.running[sourceID]
	if !hasSource {
		return nil, false
	}
	if launched != nil && !launchedCodexExited(launched) {
		return source, true
	}
	delete(l.running, sourceID)
	delete(l.sources, sourceID)
	if sources != nil {
		sources.Remove(sourceID)
	}
	return nil, false
}

func launchedCodexExited(launched *launchedCodex) bool {
	select {
	case <-launched.exited:
		return true
	default:
		return false
	}
}

func (l *CodexLauncher) Shutdown(ctx context.Context) error {
	l.mu.Lock()
	running := make([]*launchedCodex, 0, len(l.running))
	for _, launched := range l.running {
		running = append(running, launched)
	}
	l.running = map[string]*launchedCodex{}
	l.sources = map[string]appsource.Source{}
	l.mu.Unlock()

	for _, launched := range running {
		if launched.cmd.Process != nil {
			_ = launched.cmd.Process.Kill()
		}
		select {
		case <-launched.exited:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (l *CodexLauncher) launchLocked(ctx context.Context, cfg CodexLaunchConfig) (*launchedCodex, error) {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "codex"
	}
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = "ws://127.0.0.1:0"
	}
	if !strings.HasPrefix(listen, "ws://") {
		return nil, appwire.HubLaunchError("hub-launched codex app-server requires websocket listen URL")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	args := buildCodexLaunchArgs(binary, cfg.Args, listen)
	cmd := exec.Command(binary, args...)
	cmd.Dir = cfg.WorkingDir
	cmd.Env = codexLaunchEnv(cfg.Env)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, appwire.HubLaunchError("prepare codex app-server stdout: " + err.Error())
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, appwire.HubLaunchError("prepare codex app-server stderr: " + err.Error())
	}
	endpoints := make(chan string, 4)
	if err := cmd.Start(); err != nil {
		return nil, appwire.HubLaunchError("start codex app-server: " + err.Error())
	}
	go scanCodexEndpoint(stdout, endpoints)
	go scanCodexEndpoint(stderr, endpoints)
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := configuredCodexEndpoint(listen)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if endpoint != "" && codexReady(waitCtx, l.client, endpoint) {
			return &launchedCodex{cmd: cmd, endpoint: endpoint, exited: exited}, nil
		}
		select {
		case next := <-endpoints:
			if next != "" {
				endpoint = next
			}
		case err := <-exited:
			if err != nil {
				return nil, appwire.HubLaunchError("codex app-server exited before ready: " + err.Error())
			}
			return nil, appwire.HubLaunchError("codex app-server exited before ready")
		case <-ticker.C:
		case <-waitCtx.Done():
			_ = cmd.Process.Kill()
			return nil, appwire.HubLaunchError("codex app-server timed out waiting for ready")
		}
	}
}

func buildCodexLaunchArgs(binary string, configured []string, listen string) []string {
	var args []string
	if configured != nil {
		args = append(args, configured...)
	} else if !strings.Contains(filepath.Base(binary), "codex-app-server") {
		args = append(args, "app-server")
	}
	if !argsContainFlag(args, "--listen") {
		args = append(args, "--listen", listen)
	}
	return args
}

func argsContainFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

func codexLaunchEnv(overrides map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, "SERF_HUB_SPAWNED_CODEX=1")
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func scanCodexEndpoint(r io.Reader, endpoints chan<- string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if endpoint, ok := parseCodexEndpoint(scanner.Text()); ok {
			endpoints <- endpoint
		}
	}
}

func parseCodexEndpoint(line string) (string, bool) {
	if endpoint, ok := parseCodexEndpointJSON(line); ok {
		return endpoint, true
	}
	idx := strings.Index(line, "ws://")
	if idx < 0 {
		return "", false
	}
	raw := line[idx:]
	if fields := strings.Fields(raw); len(fields) > 0 {
		raw = fields[0]
	}
	raw = strings.TrimRight(raw, ".,)")
	return validCodexEndpoint(raw)
}

func parseCodexEndpointJSON(line string) (string, bool) {
	var payload struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", false
	}
	return validCodexEndpoint(payload.Endpoint)
}

func validCodexEndpoint(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.Host == "" {
		return "", false
	}
	return raw, true
}

func configuredCodexEndpoint(listen string) string {
	u, err := url.Parse(listen)
	if err != nil || u.Scheme != "ws" || strings.HasSuffix(u.Host, ":0") {
		return ""
	}
	return listen
}

func codexReady(ctx context.Context, client *http.Client, endpoint string) bool {
	readyURL, err := codexReadyURL(endpoint)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func codexReadyURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme != "ws" {
		return "", fmt.Errorf("unsupported codex endpoint scheme: %s", u.Scheme)
	}
	u.Scheme = "http"
	u.Path = "/readyz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
