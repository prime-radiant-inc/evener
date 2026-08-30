package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

const evenerLaunchCheckTimeout = 30 * time.Second

// daemonLaunchOutputLimit bounds how much of a failed launch's daemon log is
// quoted back to the operator as the reason it would not start.
const daemonLaunchOutputLimit = 64 * 1024

var (
	spawnMkdirAll                   = os.MkdirAll
	spawnMkdirTemp                  = os.MkdirTemp
	spawnWriteFile                  = os.WriteFile
	spawnRemoveAll                  = os.RemoveAll
	listEvenerLaunchModelContractFn = listEvenerLaunchModelContract
	listRendezvousForWait           = rendezvous.List
)

// HubSpawner fulfills the hubcore.Spawner interface using SpawnDaemon.
type HubSpawner struct {
	Cfg                 Config
	EvenerBinary        string // path to the evener binary; "" → "evener" on PATH
	RunDir              string
	HubToken            string
	Registry            *hubcore.ProviderRegistry // live registry the credential gate reads
	StateRoot           string                    // hub-level state root; used for resolving
	ProvidersConfigPath string                    // providers.toml the child reads as its user layer
	CredentialsPath     string                    // credentials.toml the child resolves keys from
	NoUserLayer         bool                      // hand the child EVENER_PROVIDERS_CONFIG= (present, empty): no user layer (spec §10)
}

type EvenerLaunchModelLister interface {
	ListLaunchModels(context.Context) ([]appwire.ModelDescriptor, error)
}

type EvenerLaunchModelContractLister interface {
	ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error)
}

type EvenerLaunchModelContractWorkingDirLister interface {
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
	stateDir, err := resolveEvenerLaunchStateDir("", nil)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         h.NoUserLayer,
		CredentialsPath:     h.CredentialsPath,
	})
	return listEvenerLaunchModelContractFn(ctx, h.EvenerBinary, env)
}

func (h *HubSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	stateDir, err := resolveEvenerLaunchStateDir(workingDir, nil)
	if err != nil {
		// The spawn form lets a user type a working directory that does not
		// exist yet (preflight offers to create it on submit), and the model
		// picker loads its launchable set scoped by that cwd. A not-yet-created
		// directory fails project resolution at EvalSymlinks with fs.ErrNotExist;
		// failing the whole model list here empties the picker before the
		// directory is ever created. Fall back to the unscoped state dir — the
		// same resolveEvenerLaunchStateDir("", nil) ListLaunchModelContract
		// uses — so the launchable set loads against the default project
		// identity. A non-NotExist error (permissions, etc.) still propagates.
		if !errors.Is(err, fs.ErrNotExist) {
			return appwire.ModelListResponse{}, err
		}
		stateDir, err = resolveEvenerLaunchStateDir("", nil)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         h.NoUserLayer,
		CredentialsPath:     h.CredentialsPath,
	})
	return listEvenerLaunchModelContractFn(ctx, h.EvenerBinary, env)
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
			req.Project, req.StateDir, err = resolveEvenerLaunchProjectStateDir(req.WorkingDir, req.Resolved.Effective.Env)
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
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         h.NoUserLayer,
		CredentialsPath:     h.CredentialsPath,
	})
	if err := validateProviderCredentials(req.Provider, h.Registry); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateEvenerLaunchContract(ctx, h.EvenerBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.EvenerBinary, h.RunDir, req, timeout)
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
			req.Project, req.StateDir, err = resolveEvenerLaunchProjectStateDir(req.WorkingDir, req.Resolved.Effective.Env)
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
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         h.NoUserLayer,
		CredentialsPath:     h.CredentialsPath,
	})
	if req.Provider != "" {
		if err := validateProviderCredentials(req.Provider, h.Registry); err != nil {
			return rendezvous.Entry{}, err
		}
	}
	// Resume must validate only the daemon/app-wire contract. The resumed
	// session's persisted metadata, not ambient launch config, selects the model;
	// passing req.Resolved.Effective.Model here can reject an otherwise-valid
	// resume because of a stale launch-config model.
	if err := validateEvenerLaunchContract(ctx, h.EvenerBinary, "", req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return ResumeDaemon(ctx, h.EvenerBinary, h.RunDir, req, timeout)
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

// buildSpawnArgs assembles the arg slice for `evener serve` from a hubcore.SpawnRequest.
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
	if req.PluginRoot != "" {
		args = append(args, "--plugin-root", req.PluginRoot)
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

// SpawnDaemon launches a `evener serve` subprocess from the given hubcore.SpawnRequest,
// then waits up to timeout for its rendezvous file to appear.
//
// Returns the rendezvous Entry on success, or error on timeout / spawn failure.
// Caller does NOT manage the subprocess lifecycle — the spawned daemon
// runs independently and lives until killed or sent /shutdown.
func SpawnDaemon(ctx context.Context, evenerBinary string, runDir string, req hubcore.SpawnRequest, timeout time.Duration) (rendezvous.Entry, error) {
	return spawnDaemon(ctx, evenerBinary, runDir, req, timeout, os.Stderr)
}

// spawnDaemon is SpawnDaemon against a caller-supplied hub log, which is the
// hub's own stderr in production.
func spawnDaemon(ctx context.Context, evenerBinary string, runDir string, req hubcore.SpawnRequest, timeout time.Duration, hubLog io.Writer) (rendezvous.Entry, error) {
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	args := append([]string{"serve"}, buildSpawnArgs(req)...)

	// NOT CommandContext: the spawned daemon must outlive this call's ctx (it
	// runs independently until killed or sent /shutdown). ctx scopes only the
	// rendezvous wait below; on timeout we kill the process explicitly.
	cmd := exec.Command(evenerBinary, args...) //nolint:noctx // detached daemon must outlive ctx (see comment)
	cmd.SysProcAttr = daemonSysProcAttr()
	cmd.Env = req.Env
	// A fresh spawn cannot name the log after its session yet: the daemon mints
	// the id and reports it through rendezvous, so the file is adopted below.
	dlog, err := openDaemonLog(runDir, "")
	if err != nil {
		return rendezvous.Entry{}, err
	}
	dlog.attach(cmd)

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		dlog.close()
		// Nothing was ever written to it and no session will ever claim it.
		dlog.removeIfPending()
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	// The child holds its own descriptor from here on.
	dlog.close()
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	waitCtx, cancel := withRendezvousTimeout(ctx, timeout)
	defer cancel()
	entry, err := waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		// Take the tail FIRST: it is the only account of this failure anyone
		// gets. Then drop the file, because the session id that would have
		// named it only ever arrives with the rendezvous entry this launch did
		// not get, so nothing will ever read it again (kata dd8d).
		failure := launchFailureError(launchFailurePrefix("daemon spawn", err), err, dlog.tail(daemonLaunchOutputLimit))
		dlog.removeIfPending()
		return rendezvous.Entry{}, failure
	}
	dlog.adopt(entry.SessionID)
	_, _ = io.WriteString(hubLog, daemonSpawnBanner(entry.SessionID, entry.PID, dlog.path))
	return entry, nil
}

// WaitOption configures waitForRendezvousOrExit.
type WaitOption func(*waitConfig)

type waitConfig struct {
	startedAfter time.Time
}

// withRendezvousTimeout applies the production startup budget when it is
// positive. A non-positive timeout leaves the caller's context as the only
// bound, which lets deterministic host-process tests await the rendezvous edge
// itself instead of turning an arbitrary duration into their behavior oracle.
func withRendezvousTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// WithStartedAfter rejects rendezvous entries whose StartedAt is on or
// before t. Use this to defend against a recycled PID matching a stale
// entry from a previously-crashed daemon.
func WithStartedAfter(t time.Time) WaitOption {
	return func(c *waitConfig) { c.startedAfter = t }
}

// ResumeDaemon launches `evener serve --resume <sessionID>` and waits for
// rendezvous. Returns the resumed daemon's rendezvous Entry.
//
// Note: resume PRESERVES the existing session_id. The daemon restores via
// RestoreSessionFromMetaWithConfig, which keeps the persisted meta.ID
// (immutable across restart), so the returned Entry.SessionID is the same
// id the session had before it exited. (A fresh session_id is minted only
// by /clear, which is a distinct operation.)
func ResumeDaemon(ctx context.Context, evenerBinary, runDir string, req hubcore.ResumeRequest, timeout time.Duration) (rendezvous.Entry, error) {
	return resumeDaemon(ctx, evenerBinary, runDir, req, timeout, os.Stderr)
}

// resumeDaemon is ResumeDaemon against a caller-supplied hub log, which is the
// hub's own stderr in production.
func resumeDaemon(ctx context.Context, evenerBinary, runDir string, req hubcore.ResumeRequest, timeout time.Duration, hubLog io.Writer) (rendezvous.Entry, error) {
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	args := buildResumeArgs(req)
	// NOT CommandContext: the resumed daemon must outlive this call's ctx (it
	// runs independently until killed or sent /shutdown). ctx scopes only the
	// rendezvous wait below; on timeout we kill the process explicitly.
	cmd := exec.Command(evenerBinary, args...) //nolint:noctx // detached daemon must outlive ctx (see comment)
	cmd.SysProcAttr = daemonSysProcAttr()
	cmd.Env = req.Env
	// A resume keeps its session's id, so it keeps — and appends to — that
	// session's own log.
	dlog, err := openDaemonLog(runDir, req.SessionID)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	dlog.attach(cmd)
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		dlog.close()
		dlog.removeIfUncommitted()
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	// The child holds its own descriptor from here on.
	dlog.close()
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()
	waitCtx, cancel := withRendezvousTimeout(ctx, timeout)
	defer cancel()
	entry, err := waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		failure := launchFailureError(launchFailurePrefix("resume", err), err, dlog.tail(daemonLaunchOutputLimit))
		dlog.removeIfUncommitted()
		return rendezvous.Entry{}, failure
	}
	if err := dlog.promote(); err != nil {
		_ = cmd.Process.Kill()
		promotionErr := fmt.Errorf("promote daemon log: %w", err)
		failure := launchFailureError(launchFailurePrefix("resume", promotionErr), promotionErr, dlog.tail(daemonLaunchOutputLimit))
		dlog.removeIfUncommitted()
		return rendezvous.Entry{}, failure
	}
	_, _ = io.WriteString(hubLog, daemonSpawnBanner(entry.SessionID, entry.PID, dlog.path))
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
// recovery is to reconnect and re-issue, not a Evener fault with a session log
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

func resolveEvenerStateDir(workDir, override string) (string, error) {
	return resolveEvenerStateDirWithStateHome(workDir, override, "")
}

func resolveEvenerLaunchStateDir(workDir string, env map[string]string) (string, error) {
	_, stateDir, err := resolveEvenerLaunchProjectStateDir(workDir, env)
	return stateDir, err
}

func resolveEvenerLaunchProjectStateDir(workDir string, env map[string]string) (identifier.Project, string, error) {
	if env == nil {
		return resolveEvenerStateDirWithProject(workDir, "", "")
	}
	return resolveEvenerStateDirWithProject(workDir, env[envvars.EVENERStateDir.Name], env[envvars.XDGStateHome.Name])
}

// resolveStateDirForProject derives state storage from an identity that was
// already resolved by the launch entry point. An explicit EVENER_STATE_DIR
// remains authoritative; the active working directory is intentionally unused
// in that case and is retained only for the direct-call fallback contract.
func resolveStateDirForProject(project identifier.Project, workDir string, env map[string]string) (string, error) {
	override := ""
	stateHome := ""
	if env != nil {
		override = env[envvars.EVENERStateDir.Name]
		stateHome = env[envvars.XDGStateHome.Name]
	}
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	if project.ID == "" {
		_, stateDir, err := resolveEvenerStateDirWithProject(workDir, override, stateHome)
		return stateDir, err
	}
	return agent.RuntimeDirForProjectWithStateHome(project, strings.TrimSpace(stateHome)), nil
}

func resolveEvenerStateDirWithStateHome(workDir, override, stateHome string) (string, error) {
	_, stateDir, err := resolveEvenerStateDirWithProject(workDir, override, stateHome)
	return stateDir, err
}

func resolveEvenerStateDirWithProject(workDir, override, stateHome string) (identifier.Project, string, error) {
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

// validateProviderCredentials refuses a launch whose instance has no
// credential the child could resolve, so the failure is a launch error the
// user can read rather than a 401 mid-session. The registry answers every
// part of it (spec §11.3): auth = none and optional-bearer need nothing,
// oauth-openai-codex is satisfied by the instance's OAuth record, gcp-adc by
// the ADC variable or file, and everything else by a resolved key or
// credential header — with the endpoint stop of §10 already applied, so a
// gateway that inherits no vendor key is refused here.
//
// A nil registry or an empty provider name means there is nothing to check.
func validateProviderCredentials(provider string, reg *hubcore.ProviderRegistry) error {
	name := strings.ToLower(strings.TrimSpace(provider))
	if name == "" || reg == nil || reg.Get() == nil {
		return nil
	}
	r := reg.Get()
	if inst, ok := r.Instance(name); ok {
		switch inst.Auth {
		case registry.AuthNone, registry.AuthOptionalBearer:
			return nil
		}
		if inst.CredentialSource != "none" {
			return nil
		}
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: %s", name, strings.Join(inst.Warnings, "; ")))
	}
	// Not an instance: a curated implicit provider whose credential does not
	// resolve in this environment (spec §5.1), or a name nothing declares.
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set a key via evener/auth/apiKey/set or export one of %s", name, strings.Join(p.APIKeyEnv, ", ")))
	}
	return appwire.HubLaunchError(fmt.Sprintf("unknown instance %q: add a [providers.%s] entry to providers.toml", name, name))
}

func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range slices.Backward(env) {
		if rest, ok := strings.CutPrefix(entry, prefix); ok {
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
// evenerLaunchCheckTimeout budget elapsed, or the caller went away — and only the
// first is a timeout. Calling the second one sends an operator triaging it
// after a slow machine or a hung `evener launch-check`, when nothing was slow and
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
		return appwire.HubLaunchError("evener launch-check canceled")
	}
	return appwire.HubLaunchError("evener launch-check timed out")
}

func validateEvenerLaunchContract(ctx context.Context, evenerBinary, model string, env []string) error {
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	args := []string{"launch-check", "--protocol", appwire.ProtocolVersion, "--json"}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	checkCtx, cancel := context.WithTimeout(ctx, evenerLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, evenerBinary, args...)
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
		return appwire.HubLaunchError("evener launch-check failed: " + msg)
	}
	var resp struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.HubLaunchError("evener launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.HubLaunchError(fmt.Sprintf("evener launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	return nil
}

func listEvenerLaunchModelContract(ctx context.Context, evenerBinary string, env []string) (appwire.ModelListResponse, error) {
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	checkCtx, cancel := context.WithTimeout(ctx, evenerLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, evenerBinary, "launch-check", "--protocol", appwire.ProtocolVersion, "--json", "--models")
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
		return appwire.ModelListResponse{}, appwire.HubLaunchError("evener launch-check failed: " + msg)
	}
	var resp struct {
		Protocol    string                        `json:"protocol"`
		Models      []appwire.ModelDescriptor     `json:"models"`
		Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.ModelListResponse{}, appwire.HubLaunchError("evener launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.ModelListResponse{}, appwire.HubLaunchError(fmt.Sprintf("evener launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
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
