package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
)

const serfLaunchCheckTimeout = 30 * time.Second
const daemonLaunchStderrLimit = 64 * 1024

var (
	spawnMkdirAll                    = os.MkdirAll
	spawnMkdirTemp                   = os.MkdirTemp
	spawnWriteFile                   = os.WriteFile
	spawnRemoveAll                   = os.RemoveAll
	listSerfLaunchModelContractFn    = listSerfLaunchModelContract
	openAIStoredOAuthUsableForLaunch = openAIStoredOAuthUsable
	listRendezvousForWait            = rendezvous.List
)

// HubSpawner fulfills the hubcore.Spawner interface using SpawnDaemon.
type HubSpawner struct {
	Cfg                 Config
	SerfBinary          string // path to the serf binary; "" → "serf" on PATH
	RunDir              string
	HubToken            string
	Creds               *credentials.Store // credentials store for provider key injection
	StateRoot           string             // hub-level state root; used for resolving
	ProvidersConfigPath string             // path of the providers.toml the hub loaded
}

type SerfLaunchModelLister interface {
	ListLaunchModels(context.Context) ([]appwire.ModelDescriptor, error)
}

type SerfLaunchModelContractLister interface {
	ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error)
}

type SerfLaunchModelContractWorkingDirLister interface {
	ListLaunchModelContractForWorkingDir(context.Context, string) (appwire.ModelListResponse, error)
}

func (h *HubSpawner) ListLaunchModels(ctx context.Context) ([]appwire.ModelDescriptor, error) {
	resp, err := h.ListLaunchModelContract(ctx)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (h *HubSpawner) ListLaunchModelContract(ctx context.Context) (appwire.ModelListResponse, error) {
	stateDir, err := resolveSerfLaunchStateDir("", nil)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	return listSerfLaunchModelContractFn(ctx, h.SerfBinary, env)
}

func (h *HubSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	stateDir, err := resolveSerfLaunchStateDir(workingDir, nil)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	return listSerfLaunchModelContractFn(ctx, h.SerfBinary, env)
}

func (h *HubSpawner) Spawn(ctx context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		var err error
		if req.Project.ID != "" {
			req.StateDir, err = resolveStateDirForProject(req.Project, req.WorkingDir, req.Resolved.Effective.Env)
		} else {
			req.Project, req.StateDir, err = resolveSerfLaunchProjectStateDir(req.WorkingDir, req.Resolved.Effective.Env)
		}
		if err != nil {
			return rendezvous.Entry{}, err
		}
	}
	resolved, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	defer cleanup()
	req.Resolved = resolved
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            req.Resolved,
		Provider:            req.Provider,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	if err := validateProviderCredentials(req.Provider, h.Creds, req.Env, h.ProvidersConfigPath); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

func (h *HubSpawner) Resume(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		var err error
		if req.Project.ID != "" {
			req.StateDir, err = resolveStateDirForProject(req.Project, req.WorkingDir, req.Resolved.Effective.Env)
		} else {
			req.Project, req.StateDir, err = resolveSerfLaunchProjectStateDir(req.WorkingDir, req.Resolved.Effective.Env)
		}
		if err != nil {
			return rendezvous.Entry{}, err
		}
	}
	resolved, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	defer cleanup()
	req.Resolved = resolved
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            req.Resolved,
		Provider:            req.Provider,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	if req.Provider != "" {
		if err := validateProviderCredentials(req.Provider, h.Creds, req.Env, h.ProvidersConfigPath); err != nil {
			return rendezvous.Entry{}, err
		}
	}
	// Resume must validate only the daemon/app-wire contract. The resumed
	// session's persisted metadata, not ambient launch config, selects the model;
	// passing req.Resolved.Effective.Model here can reject an otherwise-valid
	// resume because of a stale launch-config model.
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, "", req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return ResumeDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

func prepareResolvedForSpawn(stateDir string, resolved launchconfig.Resolved) (launchconfig.Resolved, func(), error) {
	effective := &resolved.Effective
	if effective.SystemPromptMode != "inline" && effective.SystemPromptAppendMode != "inline" {
		return resolved, func() {}, nil
	}
	if stateDir == "" {
		return launchconfig.Resolved{}, nil, errors.New("state dir is required for inline system prompts")
	}
	if err := spawnMkdirAll(stateDir, 0o700); err != nil {
		return launchconfig.Resolved{}, nil, fmt.Errorf("create state dir for inline prompts: %w", err)
	}
	tempDir, err := spawnMkdirTemp(stateDir, "inline-prompts-")
	if err != nil {
		return launchconfig.Resolved{}, nil, fmt.Errorf("create inline prompt dir: %w", err)
	}
	cleanupPartial := func() { _ = spawnRemoveAll(tempDir) }
	writePrompt := func(name, text string) (string, error) {
		path := filepath.Join(tempDir, name)
		if err := spawnWriteFile(path, []byte(text), 0o600); err != nil {
			return "", err
		}
		return path, nil
	}

	if effective.SystemPromptMode == "inline" {
		path, err := writePrompt("system-prompt.md", effective.SystemPromptText)
		if err != nil {
			cleanupPartial()
			return launchconfig.Resolved{}, nil, fmt.Errorf("write inline system prompt: %w", err)
		}
		effective.SystemPromptMode = "file"
		effective.SystemPromptFile = path
		effective.SystemPromptText = ""
	}
	if effective.SystemPromptAppendMode == "inline" {
		path, err := writePrompt("system-prompt-append.md", effective.SystemPromptAppendText)
		if err != nil {
			cleanupPartial()
			return launchconfig.Resolved{}, nil, fmt.Errorf("write inline system prompt append: %w", err)
		}
		effective.SystemPromptAppendMode = "file"
		effective.SystemPromptAppendFile = path
		effective.SystemPromptAppendText = ""
	}
	// Once preparation succeeds, the daemon/session state directory owns these
	// files. They must remain available after the hub RPC returns because the
	// daemon can reuse the resolved session config later.
	return resolved, func() {}, nil
}

// buildSpawnArgs assembles the arg slice for `serf serve` from a hubcore.SpawnRequest.
//
// Always passes --addr 127.0.0.1:0 so the daemon binds an ephemeral port,
// which it reports via its rendezvous file.
func buildSpawnArgs(req hubcore.SpawnRequest) []string {
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
	if req.AppReplaySize > 0 {
		args = append(args, "--app-replay-size", strconv.Itoa(req.AppReplaySize))
	}
	args = append(args, launchconfig.ToArgs(req.Resolved)...)
	return args
}

func buildResumeArgs(req hubcore.ResumeRequest) []string {
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
	if req.AppReplaySize > 0 {
		args = append(args, "--app-replay-size", strconv.Itoa(req.AppReplaySize))
	}
	resumeResolved := req.Resolved
	resumeResolved.Effective.Model = ""
	resumeResolved.Effective.FastCheapModel = ""
	resumeResolved.Effective.ModelFallbacks = nil
	args = append(args, launchconfig.ToArgs(resumeResolved)...)
	return args
}

// SpawnDaemon launches a `serf serve` subprocess from the given hubcore.SpawnRequest,
// then waits up to timeout for its rendezvous file to appear.
//
// Returns the rendezvous Entry on success, or error on timeout / spawn failure.
// Caller does NOT manage the subprocess lifecycle — the spawned daemon
// runs independently and lives until killed or sent /shutdown.
func SpawnDaemon(ctx context.Context, serfBinary string, runDir string, req hubcore.SpawnRequest, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := append([]string{"serve"}, buildSpawnArgs(req)...)

	// NOT CommandContext: the spawned daemon must outlive this call's ctx (it
	// runs independently until killed or sent /shutdown). ctx scopes only the
	// rendezvous wait below; on timeout we kill the process explicitly.
	cmd := exec.Command(serfBinary, args...) //nolint:noctx // detached daemon must outlive ctx (see comment)
	cmd.Env = req.Env
	var stderr tailBuffer
	stderr.limit = daemonLaunchStderrLimit
	cmd.Stdout = os.Stderr // forward to hub stderr for now
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

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
		return rendezvous.Entry{}, launchFailureError(launchFailurePrefix("daemon spawn", err), err, stderr.String())
	}
	return entry, nil
}

// WaitOption configures waitForRendezvousOrExit.
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

// ResumeDaemon launches `serf serve --resume <sessionID>` and waits for
// rendezvous. Returns the resumed daemon's rendezvous Entry.
//
// Note: resume PRESERVES the existing session_id. The daemon restores via
// RestoreSessionFromMetaWithConfig, which keeps the persisted meta.ID
// (immutable across restart), so the returned Entry.SessionID is the same
// id the session had before it exited. (A fresh session_id is minted only
// by /clear, which is a distinct operation.)
func ResumeDaemon(ctx context.Context, serfBinary, runDir string, req hubcore.ResumeRequest, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := buildResumeArgs(req)
	// NOT CommandContext: the resumed daemon must outlive this call's ctx (it
	// runs independently until killed or sent /shutdown). ctx scopes only the
	// rendezvous wait below; on timeout we kill the process explicitly.
	cmd := exec.Command(serfBinary, args...) //nolint:noctx // detached daemon must outlive ctx (see comment)
	cmd.Env = req.Env
	var stderr tailBuffer
	stderr.limit = daemonLaunchStderrLimit
	cmd.Stdout = os.Stderr
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
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
		return rendezvous.Entry{}, launchFailureError(launchFailurePrefix("resume", err), err, stderr.String())
	}
	return entry, nil
}

type tailBuffer struct {
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if extra := len(b.buf) + len(p) - b.limit; extra > 0 {
		copy(b.buf, b.buf[extra:])
		b.buf = b.buf[:len(b.buf)-extra]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}

// errRendezvousTimeout is the rendezvous wait running out of time, as opposed
// to the child dying first or the caller walking away. Those are the only
// three ways the wait fails, the operator's next move differs for each, and a
// caller that wants to say which one happened must not have to re-read the
// message to find out.
var errRendezvousTimeout = errors.New("timeout waiting for rendezvous")

// errRendezvousCanceled is the caller abandoning the launch before the daemon
// registered. The wait runs under the caller's context on both hub paths — the
// REST resume passes r.Context(), the RPC one the websocket connection's ctx —
// so a browser that navigates away, a dropped connection, or a keepalive that
// gives up ends the wait without the spawn timeout having elapsed (kata 0c3g).
//
// The text keeps "rendezvous" so a canceled launch stays inside
// diagnostic.HubFailureKeywords: it is still a hub failure whose honest
// recovery is to reconnect and re-issue, not a Serf fault with a session log
// to go read.
var errRendezvousCanceled = errors.New("request canceled before rendezvous")

// rendezvousWaitError says which way a done rendezvous-wait context ended.
// ctx.Err() separates the two outright, so nothing has to be inferred from
// timing: Canceled is the caller walking away, DeadlineExceeded is time
// genuinely running out — the spawn timeout a launch layers on, or a deadline
// the caller brought with it.
func rendezvousWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return errRendezvousCanceled
	}
	return errRendezvousTimeout
}

// launchFailurePrefix labels a launch failure by what actually stopped it. A
// daemon that fails validation and exits in milliseconds is not a timeout, and
// neither is a caller who stopped waiting; calling either one a timeout sends
// an operator triaging it after a slow machine, a hung provider, or a
// too-short SpawnTimeout — none of which are involved (katas 42ck, 0c3g).
// Only the wait genuinely running out of time keeps the timeout label.
func launchFailurePrefix(action string, err error) string {
	switch {
	case errors.Is(err, errRendezvousTimeout):
		return action + " timed out"
	case errors.Is(err, errRendezvousCanceled):
		return action + " canceled"
	default:
		return action + " failed"
	}
}

func launchFailureError(prefix string, err error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" || strings.Contains(err.Error(), detail) {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, detail)
}

func resolveSerfStateDir(workDir, override string) (string, error) {
	return resolveSerfStateDirWithStateHome(workDir, override, "")
}

func resolveSerfLaunchStateDir(workDir string, env map[string]string) (string, error) {
	_, stateDir, err := resolveSerfLaunchProjectStateDir(workDir, env)
	return stateDir, err
}

func resolveSerfLaunchProjectStateDir(workDir string, env map[string]string) (identifier.Project, string, error) {
	if env == nil {
		return resolveSerfStateDirWithProject(workDir, "", "")
	}
	return resolveSerfStateDirWithProject(workDir, env[envvars.SERFStateDir.Name], env[envvars.XDGStateHome.Name])
}

// resolveStateDirForProject derives state storage from an identity that was
// already resolved by the launch entry point. An explicit SERF_STATE_DIR
// remains authoritative; the active working directory is intentionally unused
// in that case and is retained only for the direct-call fallback contract.
func resolveStateDirForProject(project identifier.Project, workDir string, env map[string]string) (string, error) {
	override := ""
	stateHome := ""
	if env != nil {
		override = env[envvars.SERFStateDir.Name]
		stateHome = env[envvars.XDGStateHome.Name]
	}
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	if project.ID == "" {
		_, stateDir, err := resolveSerfStateDirWithProject(workDir, override, stateHome)
		return stateDir, err
	}
	return agent.RuntimeDirForProjectWithStateHome(project, strings.TrimSpace(stateHome)), nil
}

func resolveSerfStateDirWithStateHome(workDir, override, stateHome string) (string, error) {
	_, stateDir, err := resolveSerfStateDirWithProject(workDir, override, stateHome)
	return stateDir, err
}

func resolveSerfStateDirWithProject(workDir, override, stateHome string) (identifier.Project, string, error) {
	if strings.TrimSpace(override) != "" {
		return identifier.Project{}, override, nil
	}
	wd := strings.TrimSpace(workDir)
	if wd == "" {
		if got, err := os.Getwd(); err == nil {
			wd = got
		}
	}
	// Key off the resolved main repo root, not the raw wd: for an origin-less
	// repo, spawning from a linked worktree must compute the same session
	// state dir as spawning from the main checkout. See
	// cmdutil.ResolveStateKeyDir and
	// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1.
	project, stateDir, err := agent.RuntimeDirWithStateHome(wd, "", strings.TrimSpace(stateHome))
	if err != nil {
		return identifier.Project{}, "", fmt.Errorf("resolve project state: %w", err)
	}
	return project, stateDir, nil
}

// validateProviderCredentials checks that the credentials store has a value
// for the given provider. Providers listed with auth mode "none" (e.g. ollama)
// are always accepted. A configured provider with no resolvable credential
// returns a structured launch error.
//
// If store is nil, credential validation is skipped (the spawned process
// inherits env credentials or will fail at the LLM provider level instead).
//
// When providersConfigPath is non-empty, the file is loaded fresh from disk
// and the check is instance-aware: inline api_key on the instance counts as a
// credential, and for openai-type instances the per-instance OAuth file
// (auth/<name>.json) is checked using the instance name, not "openai".
// When the file cannot be loaded or the path is empty, the original type-map
// behavior is used unchanged.
func validateProviderCredentials(provider string, store *credentials.Store, env []string, providersConfigPath string) error {
	if provider == "" || store == nil {
		return nil
	}

	// Config-path: instance-aware credential check.
	if providersConfigPath != "" {
		pcfg, exists, err := providercfg.LoadFile(providersConfigPath)
		if err != nil || !exists {
			// Fall through to the no-config path below.
			goto noConfig
		}
		cfg := &pcfg
		for _, inst := range cfg.Instances {
			if inst.Name != strings.ToLower(strings.TrimSpace(provider)) {
				continue
			}
			tag := providercfg.BehaviorTag(string(inst.Type), string(inst.APIStyle))
			// Which key authenticates this instance is a question about the
			// endpoint its adapter contacts, not about the dialect it speaks
			// there, so it keys on the credential tag while every behavior
			// question below keys on the behavior tag.
			credTag := providercfg.CredentialTag(string(inst.Type), string(inst.APIStyle), inst.BaseURL)
			// A provider that authenticates nothing (ollama) has no credential
			// to look for, so there is nothing to gate on. The auth mode is a
			// property of the instance's type, not of the name it was given.
			if envvars.RequiresNoCredential(tag) {
				return nil
			}
			// Inline api_key on the instance is always sufficient.
			if strings.TrimSpace(inst.APIKey) != "" {
				return nil
			}
			if hasFile, _ := store.InstanceLayers(inst.Name, credTag); hasFile {
				return nil
			}
			for _, value := range inst.CredentialHeaders {
				if strings.TrimSpace(value) != "" {
					return nil
				}
			}
			// Per-instance OAuth at auth/<name>.json, for instances that behave
			// as OpenAI proper. The behavior tag rather than the declared type:
			// an openai instance routed through chat-completions is served by
			// the openaicompat adapter, which sends a bearer API key and cannot
			// use an OAuth record at all, so accepting one here would only move
			// the failure to a 401 mid-session.
			if tag == "openai" {
				stateDir := openAIStateDirFromLaunchEnv(env)
				if openAIInstanceOAuthUsable(stateDir, inst.Name) {
					return nil
				}
			}
			// Fall through to env-var check using the instance's behavior tag.
			if tag == "openai-compatible" {
				// A base_url set in providers.toml is sufficient: the openaicompat
				// adapter reads it from config and does not require
				// OPENAI_COMPATIBLE_BASE_URL in the environment.
				if strings.TrimSpace(inst.BaseURL) != "" {
					return nil
				}
				if openAICompatibleBaseURLInEnv(env) {
					return nil
				}
			}
			// ResolveKey's layer order, asked of the launch environment rather
			// than the hub's: the instance name's variables first, then the
			// credential row's. Asking only the row would refuse an instance
			// whose client cmdutil builds from the name-scoped key.
			if providerCredentialInEnv(inst.Name, env) || providerCredentialInEnv(credTag, env) {
				return nil
			}
			return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set via serf/auth/apiKey/set or set the matching env var", provider))
		}
		// Instance name not found in config — don't block launch.
		return nil
	}

noConfig:
	// No-config path: original type-map behavior, unchanged.
	if strings.EqualFold(strings.TrimSpace(provider), "openai") && openAIStoredOAuthUsableForLaunch(env) {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(provider), "openai-compatible") && openAICompatibleBaseURLInEnv(env) {
		return nil
	}
	// Use List() so providers that need no credentials (e.g. ollama) are
	// correctly identified via their SourceNone status.
	for _, p := range store.List() {
		if !strings.EqualFold(p.Name, provider) {
			continue
		}
		if p.Source == credentials.SourceNone {
			return nil
		}
		if providerCredentialInEnv(provider, env) {
			return nil
		}
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set via serf/auth/apiKey/set or set the matching env var", provider))
	}
	// Unknown provider — don't block launch.
	return nil
}

// openAIInstanceOAuthUsable reports whether a usable OAuth record exists for
// the given instance name in stateDir (at auth/<instanceName>.json).
func openAIInstanceOAuthUsable(stateDir, instanceName string) bool {
	record, err := authopenai.LoadAuth(stateDir, instanceName)
	if err != nil {
		return false
	}
	if record.Source != authopenai.AuthSourceOAuth {
		return false
	}
	if record.Expiry.IsZero() || record.Expiry.After(time.Now()) {
		return true
	}
	return strings.TrimSpace(record.RefreshToken) != ""
}

func providerCredentialInEnv(provider string, env []string) bool {
	for _, key := range credentials.EnvVars(provider) {
		value, ok := envLookup(env, key)
		if env == nil {
			if v, found := envvars.Find(key); found {
				value, ok = v.LookupEnv()
			}
		}
		if ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func openAICompatibleBaseURLInEnv(env []string) bool {
	value, ok := envLookup(env, envvars.OpenAICompatibleBaseURL.Name)
	if env == nil {
		value, ok = envvars.OpenAICompatibleBaseURL.LookupEnv()
	}
	return ok && strings.TrimSpace(value) != ""
}

func openAIStoredOAuthUsable(env []string) bool {
	return openAIInstanceOAuthUsable(openAIStateDirFromLaunchEnv(env), "openai")
}

func openAIStateDirFromLaunchEnv(env []string) string {
	if env == nil {
		return authopenai.DefaultStateDirWithStateHome("")
	}
	return openAIStateDirFromEnvList(env)
}

func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(env[i], prefix); ok {
			return rest, true
		}
	}
	return "", false
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

// launchCheckWaitError says which way a launch-check that never produced a
// verdict was stopped. Its context is done for two unrelated reasons — the
// serfLaunchCheckTimeout budget elapsed, or the caller went away — and only the
// first is a timeout. Calling the second one sends an operator triaging it
// after a slow machine or a hung `serf launch-check`, when nothing was slow and
// nobody is waiting for the answer any more (kata zg02).
//
// The launch-check runs ahead of the rendezvous wait and carries its own
// budget, so this is the first place a mid-launch cancellation lands: the hub
// runs it under the caller's context on every path that reaches it, and both
// hub paths hand it a live request context — r.Context() on the REST resume,
// the websocket connection's ctx on the RPC one.
//
// ctx.Err() separates the two outright, the same way rendezvousWaitError does
// for the wait that follows: Canceled is the caller walking away,
// DeadlineExceeded is time genuinely running out — the hub's own budget, or a
// deadline the caller brought with it.
//
// Both stay an appwire.HubLaunchError, the discriminator the web client and the
// TUI notice panel read to headline the failure as a session that would not
// start. The label changes; the family of failure does not.
func launchCheckWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return appwire.HubLaunchError("serf launch-check canceled")
	}
	return appwire.HubLaunchError("serf launch-check timed out")
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
		return launchCheckWaitError(checkCtx)
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

func listSerfLaunchModelContract(ctx context.Context, serfBinary string, env []string) (appwire.ModelListResponse, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	checkCtx, cancel := context.WithTimeout(ctx, serfLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, serfBinary, "launch-check", "--protocol", appwire.ProtocolVersion, "--json", "--models")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if checkCtx.Err() != nil {
		return appwire.ModelListResponse{}, launchCheckWaitError(checkCtx)
	}
	if err != nil {
		msg := strings.TrimSpace(redactEnvSecrets(string(out), env))
		if msg == "" {
			msg = err.Error()
		}
		return appwire.ModelListResponse{}, appwire.HubLaunchError("serf launch-check failed: " + msg)
	}
	var resp struct {
		Protocol    string                        `json:"protocol"`
		Models      []appwire.ModelDescriptor     `json:"models"`
		Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.ModelListResponse{}, appwire.HubLaunchError("serf launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.ModelListResponse{}, appwire.HubLaunchError(fmt.Sprintf("serf launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	models := make([]appwire.ModelDescriptor, 0, len(resp.Models))
	for _, model := range resp.Models {
		provider := strings.TrimSpace(model.Provider)
		name := strings.TrimSpace(model.Model)
		if provider == "" || name == "" {
			continue
		}
		models = append(models, appwire.ModelDescriptor{Provider: provider, Model: name})
	}
	diagnostics := make([]appwire.ModelListDiagnostic, 0, len(resp.Diagnostics))
	for _, diag := range resp.Diagnostics {
		diag.Provider = strings.TrimSpace(diag.Provider)
		diag.Source = strings.TrimSpace(diag.Source)
		diag.Title = strings.TrimSpace(diag.Title)
		diag.Message = strings.TrimSpace(diag.Message)
		diag.Hint = strings.TrimSpace(diag.Hint)
		if diag.Message == "" {
			continue
		}
		diagnostics = append(diagnostics, diag)
	}
	return appwire.ModelListResponse{Data: models, Diagnostics: diagnostics}, nil
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

// waitForRendezvousOrExit polls runDir for a rendezvous Entry whose PID
// matches, returning when one appears, when the launched child exits first, or
// when ctx ends. It is the only rendezvous wait: a second, exported copy of
// this loop with no exited arm and no possible production caller was deleted,
// having twice drifted from this one (kata waf1).
//
// A nil exited channel never fires in the select below, which is how a caller
// with no child process to watch waits on the rendezvous file alone.
func waitForRendezvousOrExit(ctx context.Context, runDir string, pid int, exited <-chan error, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := listRendezvousForWait(runDir)
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
			return rendezvous.Entry{}, rendezvousWaitError(ctx)
		case err := <-exited:
			if err != nil {
				return rendezvous.Entry{}, fmt.Errorf("process exited before rendezvous: %w", err)
			}
			return rendezvous.Entry{}, errors.New("process exited before rendezvous")
		case <-ticker.C:
		}
	}
}
