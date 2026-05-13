package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/rendezvous"
)

const serfLaunchCheckTimeout = 30 * time.Second

// SpawnRequest carries the per-spawn knobs passed directly from the caller.
type SpawnRequest struct {
	Model           string
	Agent           string
	WorkingDir      string
	StateDir        string
	RunDir          string
	ReasoningEffort string
	SSERingSize     int
	Env             []string
}

// ResumeRequest carries the resolved state needed to resume a saved session.
type ResumeRequest struct {
	SessionID   string
	WorkingDir  string
	StateDir    string
	Model       string
	RunDir      string
	SSERingSize int
	Env         []string
}

// HubSpawner fulfills the Spawner interface using SpawnDaemon.
type HubSpawner struct {
	Cfg        Config
	SerfBinary string // path to the serf binary; "" → "serf" on PATH
	RunDir     string
	HubToken   string
}

func (h *HubSpawner) Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		req.StateDir = resolveSerfStateDir(req.WorkingDir, h.Cfg.SerfLaunch.Env["SERF_STATE_DIR"])
	}
	req.RunDir = h.RunDir
	req.SSERingSize = h.Cfg.SerfLaunch.SSERingSize
	req.Env = buildSerfChildEnv(h.Cfg, h.RunDir, req.StateDir, h.HubToken)
	if err := validateProviderCredentials(req.Model, req.Env, req.StateDir); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

func (h *HubSpawner) Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		req.StateDir = resolveSerfStateDir(req.WorkingDir, h.Cfg.SerfLaunch.Env["SERF_STATE_DIR"])
	}
	req.RunDir = h.RunDir
	req.SSERingSize = h.Cfg.SerfLaunch.SSERingSize
	req.Env = buildSerfChildEnv(h.Cfg, h.RunDir, req.StateDir, h.HubToken)
	if req.Model != "" {
		if err := validateProviderCredentials(req.Model, req.Env, req.StateDir); err != nil {
			return rendezvous.Entry{}, err
		}
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return ResumeDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

// buildSpawnArgs assembles the arg slice for `serf serve` from a SpawnRequest.
//
// Always passes --addr 127.0.0.1:0 so the daemon binds an ephemeral port,
// which it reports via its rendezvous file.
func buildSpawnArgs(req SpawnRequest) []string {
	args := []string{"--addr", "127.0.0.1:0"}
	if req.WorkingDir != "" {
		args = append(args, "--dir", req.WorkingDir)
	}
	if req.StateDir != "" {
		args = append(args, "--state-dir", req.StateDir)
	}
	if req.RunDir != "" {
		args = append(args, "--run-dir", req.RunDir)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Agent != "" {
		args = append(args, "--agent", req.Agent)
	}
	if req.ReasoningEffort != "" {
		args = append(args, "--reasoning-effort", req.ReasoningEffort)
	}
	if req.SSERingSize > 0 {
		args = append(args, "--sse-ring-size", fmt.Sprintf("%d", req.SSERingSize))
	}
	return args
}

// SpawnDaemon launches a `serf serve` subprocess from the given SpawnRequest,
// then waits up to timeout for its rendezvous file to appear.
//
// Returns the rendezvous Entry on success, or error on timeout / spawn failure.
// Caller does NOT manage the subprocess lifecycle — the spawned daemon
// runs independently and lives until killed or sent /shutdown.
func SpawnDaemon(ctx context.Context, serfBinary string, runDir string, req SpawnRequest, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := append([]string{"serve"}, buildSpawnArgs(req)...)

	cmd := exec.Command(serfBinary, args...)
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	} else {
		cmd.Env = buildDefaultSerfChildEnv(runDir, req.StateDir)
	}
	cmd.Stdout = os.Stderr // forward to hub stderr for now
	cmd.Stderr = os.Stderr

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		return rendezvous.Entry{}, fmt.Errorf("daemon spawn timed out: %w", err)
	}
	return entry, nil
}

// WaitOption configures WaitForRendezvous.
type WaitOption func(*waitConfig)

type waitConfig struct {
	startedAfter time.Time
}

// WithStartedAfter rejects rendezvous entries whose StartedAt is on or
// before t. Use this to defend against a recycled PID matching a stale
// entry from a previously-crashed daemon.
func WithStartedAfter(t time.Time) WaitOption {
	return func(c *waitConfig) { c.startedAfter = t }
}

// WaitForRendezvous polls runDir for a rendezvous Entry whose PID matches.
// Returns when found, or when ctx is canceled.
func WaitForRendezvous(ctx context.Context, runDir string, pid int, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID != pid {
				continue
			}
			if !cfg.startedAfter.IsZero() && !e.StartedAt.After(cfg.startedAfter) {
				continue
			}
			return e, nil
		}
		select {
		case <-ctx.Done():
			return rendezvous.Entry{}, errors.New("timeout waiting for rendezvous")
		case <-ticker.C:
		}
	}
}

// ResumeDaemon launches `serf serve --resume <sessionID>` and waits for
// rendezvous. Returns the new daemon's rendezvous Entry.
//
// Note: resume always creates a NEW session_id (the daemon mints a fresh
// one). Caller resolves it via roster lookup after rendezvous appears.
func ResumeDaemon(ctx context.Context, serfBinary, runDir string, req ResumeRequest, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := []string{"serve", "--addr", "127.0.0.1:0", "--resume", req.SessionID}
	if req.WorkingDir != "" {
		args = append(args, "--dir", req.WorkingDir)
	}
	if req.StateDir != "" {
		args = append(args, "--state-dir", req.StateDir)
	}
	if req.RunDir != "" {
		args = append(args, "--run-dir", req.RunDir)
	}
	if req.SSERingSize > 0 {
		args = append(args, "--sse-ring-size", fmt.Sprintf("%d", req.SSERingSize))
	}
	cmd := exec.Command(serfBinary, args...)
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	} else {
		cmd.Env = buildDefaultSerfChildEnv(runDir, req.StateDir)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		return rendezvous.Entry{}, fmt.Errorf("resume timed out: %w", err)
	}
	return entry, nil
}

func resolveSerfStateDir(workDir, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	wd := strings.TrimSpace(workDir)
	if wd == "" {
		if got, err := os.Getwd(); err == nil {
			wd = got
		}
	}
	return agent.RuntimeDir(cmdutil.GitOriginURLFromDir(wd), wd, "")
}

func buildSerfChildEnv(cfg Config, runDir, stateDir, hubToken string) []string {
	env := buildDefaultSerfChildEnv(runDir, stateDir)
	keys := make([]string, 0, len(cfg.SerfLaunch.Env))
	for key := range cfg.SerfLaunch.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = setEnvValue(env, key, cfg.SerfLaunch.Env[key])
	}
	if strings.TrimSpace(hubToken) != "" {
		env = setEnvValue(env, "SERF_HUB_TOKEN", hubToken)
	}
	return env
}

func buildDefaultSerfChildEnv(runDir, stateDir string) []string {
	env := append([]string{}, os.Environ()...)
	env = setEnvValue(env, "SERF_HUB_SPAWNED", "1")
	if runDir != "" {
		env = setEnvValue(env, "SERF_RUN_DIR", runDir)
	}
	if stateDir != "" {
		env = setEnvValue(env, "SERF_STATE_DIR", stateDir)
	}
	return env
}

func setEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func validateProviderCredentials(model string, env []string, stateDir string) error {
	ref, err := cmdutil.ParseModelRef(model)
	if err != nil {
		return appwire.InvalidParams(err.Error())
	}
	envMap := envToMap(env)
	switch ref.Provider {
	case "openai":
		if strings.TrimSpace(envMap["OPENAI_API_KEY"]) != "" {
			return nil
		}
		if stateDir != "" {
			if _, err := authopenai.LoadAuth(stateDir); err == nil {
				return nil
			} else if !errors.Is(err, authopenai.ErrAuthNotFound) {
				return appwire.HubLaunchError("load OpenAI credentials: " + err.Error())
			}
		}
		return missingProviderCredential(ref.Provider, "OPENAI_API_KEY or Serf OpenAI login")
	case "anthropic":
		return requireAnyEnv(ref.Provider, envMap, "ANTHROPIC_API_KEY")
	case "google", "gemini":
		return requireAnyEnv(ref.Provider, envMap, "GEMINI_API_KEY", "GOOGLE_API_KEY")
	case "minimax":
		return requireAnyEnv(ref.Provider, envMap, "MINIMAX_API_KEY")
	case "openrouter", "openrouter-anthropic":
		return requireAnyEnv(ref.Provider, envMap, "OPENROUTER_API_KEY")
	case "kimi":
		return requireAnyEnv(ref.Provider, envMap, "KIMI_API_KEY")
	case "glm":
		return requireAnyEnv(ref.Provider, envMap, "GLM_API_KEY")
	case "openai-compatible":
		return requireAnyEnv(ref.Provider, envMap, "OPENAI_COMPATIBLE_BASE_URL")
	case "ollama":
		return nil
	default:
		return nil
	}
}

func requireAnyEnv(provider string, env map[string]string, keys ...string) error {
	for _, key := range keys {
		if strings.TrimSpace(env[key]) != "" {
			return nil
		}
	}
	return missingProviderCredential(provider, strings.Join(keys, " or "))
}

func missingProviderCredential(provider, keys string) error {
	return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: configure %s in hub serf_launch env or inherited environment", provider, keys))
}

func envToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

func validateSerfLaunchContract(ctx context.Context, serfBinary, model string, env []string) error {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := []string{"launch-check", "--protocol", appwire.ProtocolVersion, "--json"}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	checkCtx, cancel := context.WithTimeout(ctx, serfLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, serfBinary, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if checkCtx.Err() != nil {
		return appwire.HubLaunchError("serf launch-check timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(redactEnvSecrets(string(out), env))
		if msg == "" {
			msg = err.Error()
		}
		return appwire.HubLaunchError("serf launch-check failed: " + msg)
	}
	var resp struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.HubLaunchError("serf launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.HubLaunchError(fmt.Sprintf("serf launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	return nil
}

func redactEnvSecrets(text string, env []string) string {
	for key, value := range envToMap(env) {
		if !isSensitiveEnvKey(key) || len(value) < 8 {
			continue
		}
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

func isSensitiveEnvKey(key string) bool {
	key = strings.ToUpper(key)
	return strings.Contains(key, "KEY") ||
		strings.Contains(key, "TOKEN") ||
		strings.Contains(key, "SECRET") ||
		strings.Contains(key, "PASSWORD") ||
		strings.Contains(key, "CREDENTIAL")
}

func waitForRendezvousOrExit(ctx context.Context, runDir string, pid int, exited <-chan error, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID != pid {
				continue
			}
			if !cfg.startedAfter.IsZero() && !e.StartedAt.After(cfg.startedAfter) {
				continue
			}
			return e, nil
		}
		select {
		case <-ctx.Done():
			return rendezvous.Entry{}, errors.New("timeout waiting for rendezvous")
		case err := <-exited:
			if err != nil {
				return rendezvous.Entry{}, fmt.Errorf("process exited before rendezvous: %w", err)
			}
			return rendezvous.Entry{}, errors.New("process exited before rendezvous")
		case <-ticker.C:
		}
	}
}
