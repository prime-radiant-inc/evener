package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/rendezvous"
)

// threadStartDetachedTimeout bounds thread/start's admitted sequence once it
// detaches from the connection context: it must comfortably cover the spawn's
// rendezvous wait (30s default) plus the ReadThread and initial StartTurn
// RPCs, while guaranteeing a wedged daemon cannot park the worker forever.
// Var, not const, so tests can shrink the bound.
var threadStartDetachedTimeout = 2 * time.Minute

var (
	hubCanonicalizeDir = fspaths.CanonicalizeDir
	hubResolveLaunch   = launchconfig.Resolve
	hubParseModelRef   = cmdutil.ParseModelRef
	hubRosterRefresh   = func(ctx context.Context, r *hubcore.Roster) error { return r.RefreshAndWait(ctx) }
	hubRosterList      = func(r *hubcore.Roster) []hubcore.LiveEntry { return r.List() }
	hubForkSession     = agent.ForkSession
	hubForkSessionAt   = agent.ForkSessionAtUserTurn
	hubAsideSession    = agent.AsideSession
	// hubResolvePlugins resolves a launch's plugin inventory. mgr is the
	// caller's cfg.PluginManager (hubcore.WebConfig): when set, resolution
	// reaches the hub's own already-wired *plugins.Manager — the same one
	// evener/plugin/* and evener/marketplace/* mutations use — instead of a
	// second, unwired Manager. nil falls back to a fresh
	// plugins.NewManager(pluginRoot); every caller that never builds a server
	// (most tests) passes nil and gets that fallback.
	hubResolvePlugins = func(ctx context.Context, pluginRoot string, dirs []string, enabled *[]string, mgr *plugins.Manager) (plugins.LaunchPluginResolution, error) {
		if mgr == nil {
			mgr = plugins.NewManager(pluginRoot)
		}
		return mgr.ResolveForLaunch(ctx, dirs, enabled)
	}
)

func hubThreadStart(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	sourceID := strings.TrimSpace(params.Source)
	// Forward the normalized routing value, not the verbatim field the lookup
	// trimmed: a source must not observe surrounding whitespace the hub already
	// stripped to resolve it. A harness-routed start leaves Source empty, as the
	// caller sent it, because the harness is the routing field in that case.
	forward := params
	forward.Source = sourceID
	if sourceID == "" {
		sourceID = launchSourceID(params)
	}
	if sourceID != "" && sourceID != "local" {
		source, ok := sources.Source(sourceID)
		if !ok || source == nil {
			return appwire.ThreadStartResponse{}, appwire.Unavailable("spawn source is not available: " + sourceID)
		}
		return source.StartThread(ctx, forward)
	}
	if cfg.Spawner == nil {
		return appwire.ThreadStartResponse{}, appwire.Unavailable("spawner not configured")
	}
	workingDir := params.CWD
	if workingDir != "" {
		resolved, err := hubCanonicalizeDir(workingDir)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams("cwd: " + err.Error())
		}
		workingDir = resolved
	}
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	// Legacy scalar fields win over launchOverrides (per spec §5.4).
	if params.Model != "" {
		model := params.Model
		if params.ModelProvider != "" && !strings.HasPrefix(params.Model, params.ModelProvider+"/") {
			model = params.ModelProvider + "/" + params.Model
		}
		modelRef, err := hubParseModelRef(model)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
		}
		overrides.Model = modelRef.Qualified()
	}
	if params.Profile != "" {
		overrides.Agent = params.Profile
	}
	if params.ReasoningEffort != "" {
		overrides.ReasoningEffort = params.ReasoningEffort
	}
	if params.NonInteractive != nil {
		v := *params.NonInteractive
		overrides.NonInteractive = &v
	}
	// launch.toml is user-editable configuration (Hub UI's Launch settings
	// tab, or hand-edited), so its root is the config root, not
	// cfg.HubStateRoot (machine-generated state: auth-token, index.db,
	// deletions/).
	spawnResolved, resolveErr := hubResolveLaunch(hubLaunchConfigRoot(cfg), workingDir, overrides)
	if resolveErr != nil {
		return appwire.ThreadStartResponse{}, resolveErr
	}
	// The env floor (EVENER_MODEL etc.) applies to the spawn decision too,
	// matching the agent's own flag > env fallback: a session started now
	// would run with the env model, so the required-model gate must accept
	// it and the spawned child must receive it. Layers and per-launch
	// overrides still win — the floor only fills what nothing else set.
	// Env only, deliberately NOT the builtin floor: the agent applies its
	// own builtins, and pinning them in the hub's argv would skew across
	// versions (ApplyEnvDefaults' doc comment).
	spawnResolved = launchconfig.ApplyEnvDefaults(spawnResolved, os.Getenv, launchconfig.LaunchOptionSchema())
	resolvedModel := strings.TrimSpace(spawnResolved.Effective.Model)
	if resolvedModel == "" {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams("model is required")
	}
	modelRef, err := hubParseModelRef(resolvedModel)
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := validateEvenerLaunchModel(ctx, cfg, modelRef, workingDir); err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	pluginResolution, pluginErr := hubResolvePlugins(ctx, cfg.PluginRoot, spawnResolved.Effective.PluginDirs, spawnResolved.Effective.EnabledPlugins, cfg.PluginManager)
	if pluginErr != nil {
		// A resolver failure is fatal when a selection has to be honoured, and
		// always when the failure IS the caller leaving: the next thing this
		// handler does is detach from the request context and spawn, so a
		// cancellation walked past here becomes a session started for a client
		// that has gone. Everything else falls through to a launch with
		// whatever the resolver could list.
		if spawnResolved.Effective.EnabledPlugins != nil ||
			errors.Is(pluginErr, context.Canceled) || errors.Is(pluginErr, context.DeadlineExceeded) {
			return appwire.ThreadStartResponse{}, appwire.HubLaunchError(pluginErr.Error())
		}
	} else if err := pluginResolution.ValidateSelection(); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	// One last look at the connection before the handler stops listening to
	// it. Everything above is validation, and a request the caller abandoned
	// while it ran must not become a session: past the detach below, nothing
	// asks about the caller again.
	if err := ctx.Err(); err != nil {
		return appwire.ThreadStartResponse{}, appwire.HubLaunchError(err.Error())
	}
	// The mutation is admitted here: every validation has passed and the spawn
	// is about to happen. From this point the outcome must not depend on the
	// connection's fate — a disconnecting client still gets a fully-formed
	// thread (spawn + read + optional initial turn) that reconnect resync
	// discovers via thread/list — so shed PEER-lifetime cancellation, but pair
	// it with an explicit deadline: a wedged sequence may not park the worker
	// with no cancel path (threadStartDetachedTimeout's doc covers sizing).
	// The spawned child is already detached (spawnDaemon uses exec.Command);
	// this shields only the handler's own awaits.
	ctx, cancelDetached := context.WithTimeout(context.WithoutCancel(ctx), threadStartDetachedTimeout)
	defer cancelDetached()
	entry, err := cfg.Spawner.Spawn(ctx, hubcore.SpawnRequest{
		Project:       spawnResolved.Project,
		Resolved:      spawnResolved,
		WorkingDir:    workingDir,
		PluginRoot:    cfg.PluginRoot,
		AgentsDocPath: hubAgentsDocPath(cfg),
		Provider:      modelRef.Provider,
	})
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.HubLaunchError(err.Error())
	}
	canUseSpawnEntry := entry.Protocol == appwire.ProtocolVersion && entry.Endpoint != "" && entry.ThreadID != ""
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			if !canUseSpawnEntry {
				return appwire.ThreadStartResponse{}, appwire.Unavailable(err.Error())
			}
			// Spawning already established this daemon's identity. An unrelated
			// discovery failure must not hide its identity or discard initial input.
			fmt.Fprintf(os.Stderr, "[hub] spawned session %s; roster refresh failed: %v\n", entry.ThreadID, err)
		}
		if entry.ThreadID == "" || entry.SessionID == "" {
			for _, live := range hubRosterList(cfg.Roster) {
				if live.PID == entry.PID {
					if entry.ThreadID == "" {
						entry.ThreadID = live.SessionID
					}
					if entry.SessionID == "" {
						entry.SessionID = live.SessionID
					}
					break
				}
			}
		}
	}
	ref := localSpawnWorkspaceRef(entry)
	var source appsource.Source
	if canUseSpawnEntry {
		// Exact-entry RPCs share the persistent source's recovery cancellation.
		source, err = spawnedLocalDaemonSource(sources)
	} else {
		source, err = sourceForThread(sources, ref, "")
	}
	if err != nil {
		if entry.ThreadID == "" {
			return appwire.ThreadStartResponse{}, err
		}
		thread := appwire.Thread{
			ID:            entry.ThreadID,
			SessionID:     entry.SessionID,
			Preview:       entry.SessionID,
			ModelProvider: modelRef.Provider,
			CWD:           workingDir,
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:        appwire.EvenerThread{Ref: ref, InstanceID: localSpawnInstanceID(entry, appwire.Thread{})},
		}
		thread = applyHubForkCapability(cfg, thread)
		annotateThreadProjects([]appwire.Thread{thread})
		return appwire.ThreadStartResponse{Thread: thread}, nil
	}
	startEpoch := sessionRecoveryState(cfg, ref, "").Epoch
	read := func(ctx context.Context) (appwire.ThreadReadResponse, error) {
		if canUseSpawnEntry {
			return readSpawnedLocalThread(ctx, sources, entry)
		}
		return source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	}
	var threadResp appwire.ThreadReadResponse
	if cfg.Roster != nil && canUseSpawnEntry {
		if !cfg.Roster.HasConfirmedEntry(entry) {
			threadResp, err = cfg.Roster.ReadSpawnedThread(ctx, entry, read)
			if err != nil && threadResp.Thread.ID != "" {
				return appwire.ThreadStartResponse{}, appwire.Unavailable(err.Error())
			}
		} else {
			threadResp, err = read(ctx)
		}
	} else {
		threadResp, err = read(ctx)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isSessionRecoveryAdmissionError(err) {
		return appwire.ThreadStartResponse{}, err
	}
	if err != nil {
		threadResp.Thread = appwire.Thread{
			ID: entry.ThreadID, SessionID: entry.SessionID, CWD: workingDir,
			Source: "local", Evener: appwire.EvenerThread{Ref: ref, InstanceID: localSpawnInstanceID(entry, appwire.Thread{})},
		}
	}
	expectedInstanceID := localSpawnInstanceID(entry, threadResp.Thread)
	threadResp.Thread = applyHubForkCapability(cfg, threadResp.Thread)
	annotateThreadProjects([]appwire.Thread{threadResp.Thread})
	turn := appwire.Turn{}
	if len(params.Input) > 0 {
		// Gate the initial input on the capability the spawned session's own read
		// reported — the same read that established this thread's identity, so a
		// selection is never delivered to a daemon that did not advertise skill
		// input support. A failed read synthesized the thread above without
		// capabilities, so the gate fails closed: support must be known, never
		// assumed for an unread target. The start rechecks on its own connection
		// (gateSkillInputOnClient), covering a replacement or resumed daemon.
		if err := appwire.ValidateSkillInputSupport(params.Input, threadResp.Thread.Evener.Capabilities.SkillInput); err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
		}
		clientMutationID, err := identifier.NewClientMutationID()
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InternalError("create initial turn mutation id: " + err.Error())
		}
		turnParams := appwire.TurnStartParams{
			Ref:                ref,
			ClientMutationID:   clientMutationID,
			ExpectedInstanceID: expectedInstanceID,
			Input:              params.Input,
		}
		turnResp, err := withDeletionTargetOwnership(ctx, cfg, ref, "", clientMutationID, func() (appwire.TurnStartResponse, error) {
			if err := sessionActionRecoveryError(ctx, cfg, ref, "", startEpoch); err != nil {
				return appwire.TurnStartResponse{}, err
			}
			if canUseSpawnEntry {
				local, err := spawnedLocalDaemonSource(sources)
				if err != nil {
					return appwire.TurnStartResponse{}, err
				}
				return local.StartTurnAtEntry(ctx, entry, turnParams)
			}
			return source.StartTurn(ctx, turnParams)
		})
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		turn = turnResp.Turn
	}
	return appwire.ThreadStartResponse{Thread: threadResp.Thread, Turn: turn}, nil
}

func localSpawnWorkspaceRef(entry rendezvous.Entry) string {
	if ref, err := appwire.ParseRef(strings.TrimSpace(entry.WorkspaceRef)); err == nil && ref.SourceID == "local" {
		return ref.String()
	}
	threadID := strings.TrimSpace(entry.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(entry.SessionID)
	}
	return appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
}

func localSpawnInstanceID(entry rendezvous.Entry, thread appwire.Thread) string {
	for _, candidate := range []string{
		entry.InstanceID,
		entry.SessionID,
		thread.Evener.InstanceID,
		entry.ThreadID,
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func launchSourceID(params appwire.ThreadStartParams) string {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" {
		if harness == "evener" {
			return "local"
		}
		return harness
	}
	return ""
}

func hubThreadResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return resumeThread(ctx, cfg, sources, params, false)
}

func hubThreadAutoResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return resumeThread(ctx, cfg, sources, params, true)
}

func resumeThread(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams, automatic bool) (response appwire.ThreadResumeResponse, resumeErr error) {
	logSessionID := strings.TrimSpace(params.Session)
	if logSessionID == "" {
		if ref, err := appwire.ParseRef(params.Ref); err == nil {
			logSessionID = ref.ThreadID
		}
	}
	ctx, trace := withThreadLifecycleLog(ctx, "resume", logSessionID, nil)
	// These deferred stages use the final local trace after alias resolution,
	// while retaining their original start times and immutable context snapshots.
	requestCtx := ctx
	requestStarted := time.Now()
	trace.record(requestCtx, "request", "begin", requestStarted, nil, 0, 0)
	defer func() { trace.record(requestCtx, "request", "complete", requestStarted, resumeErr, 0, 0) }()
	var cleanupErr error
	var activeResume *hubcore.ActiveResume
	requestedRefID := ""
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		requestedRefID = ref.ThreadID
		if ref.SourceID != "local" {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.ThreadResumeResponse{}, err
			}
			return source.ResumeThread(ctx, params)
		}
	}
	sessionID := strings.TrimSpace(params.Session)
	if sessionID == "" && params.Ref != "" {
		// A non-empty ref was parsed at function entry, so this cannot fail.
		ref, _ := appwire.ParseRef(params.Ref)
		sessionID = ref.ThreadID
	}
	if sessionID == "" {
		return appwire.ThreadResumeResponse{}, appwire.InvalidParams("sessionId or ref is required")
	}
	requestedID := sessionID
	var ownershipAliases []string
	if cfg.ResumeLocks != nil {
		epoch := sessionRequestRecoveryEpoch(ctx, cfg, "", requestedID)
		if err := sessionConnectionRecoveryError(ctx, cfg, "", requestedID); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		ownershipDone := trace.stage(ctx, "ownership")
		target, aliases, err := resumeOwnership(cfg, requestedID, requestedRefID)
		ownershipDone(err)
		if err != nil {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
		}
		ownershipAliases = aliases
		if err := cfg.ResumeLocks.ResumeCleanupError(aliases); err != nil {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
		}
		epochs := make(map[string]uint64, len(aliases))
		for _, id := range aliases {
			epochs[id] = sessionRequestRecoveryEpoch(ctx, cfg, "", id)
		}
		epochs[requestedID] = epoch
		// Registration now takes the same per-alias ownership tokens as the
		// confirmed-stopped no-op, so the wait for those tokens can start here.
		lockDone := trace.stage(ctx, "lock_wait")
		active, err := cfg.ResumeLocks.RegisterResume(ctx, target, aliases, epochs)
		if err != nil {
			lockDone(err)
			if errors.Is(err, hubcore.ErrResumeInvalidated) {
				return appwire.ThreadResumeResponse{}, sessionRecoveryAdmissionError{appwire.Unavailable(err.Error())}
			}
			return appwire.ThreadResumeResponse{}, err
		}
		activeResume = active
		ctx = active.Context()
		// Register before waiting for ownership; complete after every subsequent
		// defer has released ownership and the launcher has confirmed cleanup.
		defer func() { active.Complete(cleanupErr) }()
		var heldStarted time.Time
		defer func() {
			// AcquireOwnership and ReleaseOwnership bracket the same span the
			// reacquire loop used to, in the registration's sorted ownership
			// order, and hubcore records the hold so a concurrent force stop
			// can tell this launch's reservation from an unrelated action's.
			active.ReleaseOwnership()
			if !heldStarted.IsZero() {
				trace.record(ctx, "lock_held", "complete", heldStarted, nil, 0, 0)
			}
		}()
		if err := active.AcquireOwnership(ctx); err != nil {
			lockDone(err)
			return appwire.ThreadResumeResponse{}, err
		}
		lockDone(nil)
		heldStarted = time.Now()
		trace.record(ctx, "lock_held", "begin", heldStarted, nil, 0, 0)
		// A deletion record may name any alias in the resolved ownership
		// group, so the whole group is fenced here, under the locks that make
		// the check final, before live-owner reuse or launching below.
		if err := deletionFenceErrorForGroup(cfg, aliases); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		for _, id := range aliases {
			if err := sessionConnectionRecoveryError(ctx, cfg, "", id); err != nil {
				return appwire.ThreadResumeResponse{}, err
			}
			state := cfg.ResumeLocks.RecoveryState(id)
			if state.Epoch != epochs[id] || state.Stopping > 0 || (automatic && state.ResumeRequired) {
				return appwire.ThreadResumeResponse{}, sessionRecoveryAdmissionError{appwire.Unavailable("session recovery requires a fresh explicit thread/resume request")}
			}
		}
		recheckDone := trace.stage(ctx, "ownership_recheck")
		currentTarget, currentAliases, err := resumeOwnership(cfg, requestedID, requestedRefID)
		recheckDone(err)
		if err != nil {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
		}
		if currentTarget != target || !slices.Equal(currentAliases, aliases) {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable("session ownership changed; refresh before resuming")
		}
		sessionID = target
		ctx, trace = trace.resolved(ctx, sessionID)
		defer func() {
			if resumeErr != nil {
				return
			}
			if !automatic {
				if err := cfg.ResumeLocks.ExplicitResumeCompleted(requestedID, epoch); err != nil {
					response = appwire.ThreadResumeResponse{}
					resumeErr = appwire.Unavailable("persist completed session recovery: " + err.Error())
					return
				}
				// The response was projected while this resume still held the
				// recovery fence. Re-project so it reports the fork authority a
				// read issued after the clear reports.
				response.Thread = applyHubForkCapability(cfg, response.Thread)
			}
			cfg.ResumeLocks.RecordResolvedSession(requestedID, sessionID, epoch)
		}()
	}

	lockedParams := params
	lockedParams.Session = sessionID
	launch := resumeLaunch{active: activeResume, completionOwned: !automatic, aliases: ownershipAliases}
	launched, launchErr := resumeThreadLockedLaunch(ctx, cfg, sources, lockedParams, &launch)
	cleanupErr = launch.cleanupErr
	return launched, launchErr
}

// resumeThreadLocked runs the discovery-and-spawn half of resumeThread with
// the caller's per-session ownership serialization already held (the public
// wrapper's alias locks, or the retirement path's). The resolved ownership
// target arrives as params.Session; the session is re-derived from params the
// same way the wrapper resolves it, without re-walking ownership aliases.
//
// The retirement path holds no explicit Resume to own: it launches with no
// active resume lifetime and the configured startup budget, exactly as an
// automatic resume does.
func resumeThreadLocked(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return resumeThreadLockedLaunch(ctx, cfg, sources, params, &resumeLaunch{})
}

// resumeLaunch is the explicit Resume's registered launch lifetime, threaded
// from the wrapper that registered it to the locked half that launches the
// child. The launcher writes its cleanup classification back through cleanupErr
// so the registering wrapper can report it to ActiveResume.Complete, which must
// run only after every defer has released ownership and the single child waiter
// has confirmed cleanup.
type resumeLaunch struct {
	active          *hubcore.ActiveResume
	completionOwned bool
	cleanupErr      error
	// aliases is the caller's resolved ownership group when it holds one (the
	// explicit Resume wrapper); the retirement wrapper holds no group and
	// leaves it nil, and the launcher falls back to the requested/resolved
	// pair its caller always has.
	aliases []string
}

// resumeThreadLockedLaunch is resumeThreadLocked for a caller that holds a
// launch lifetime in hand.
func resumeThreadLockedLaunch(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams, launch *resumeLaunch) (appwire.ThreadResumeResponse, error) {
	// The request's trace travels in the context, already carrying the resolved
	// session identity the wrapper stamped. A caller that enters here without
	// one records nothing: every stage below is nil-safe.
	trace := threadLifecycleFromContext(ctx)
	sessionID := strings.TrimSpace(params.Session)
	if sessionID == "" && params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		sessionID = ref.ThreadID
	}
	if sessionID == "" {
		return appwire.ThreadResumeResponse{}, appwire.InvalidParams("sessionId or ref is required")
	}
	requestedID := sessionID

	if err := deletionFenceError(cfg, params.Ref, requestedID, ""); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	group := launch.aliases
	if len(group) == 0 {
		group = []string{requestedID, sessionID}
	}
	if err := deletionFenceErrorForGroup(cfg, group); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}

	var discoveryErr error
	if cfg.Roster != nil {
		discoveryDone := trace.stage(ctx, "discovery")
		discoveryErr = hubRosterRefresh(ctx, cfg.Roster)
		discoveryDone(discoveryErr)
	}
	protocolDone := trace.stage(ctx, "protocol_check")
	if err := daemonRestartRequiredError(ctx, cfg, "", sessionID, ""); err != nil {
		protocolDone(err)
		return appwire.ThreadResumeResponse{}, err
	}
	protocolDone(nil)
	if discoveryErr != nil {
		// Incomplete discovery cannot authorize a replacement, but a direct
		// probe can establish that a previously confirmed owner still serves
		// this session. Keep the global discovery failure for other owners.
		if owner, ok := liveDaemonForThread(cfg.Roster, sessionID); ok && owner.Protocol == appwire.ProtocolVersion {
			probeDone := trace.stage(ctx, "owner_probe")
			if err := cfg.Roster.RefreshEntry(ctx, owner.Entry); err != nil {
				probeDone(err)
				return appwire.ThreadResumeResponse{}, appwire.Unavailable(errors.Join(discoveryErr, err).Error())
			}
			probeDone(nil)
			return hubResumedThreadResponse(ctx, cfg, sources, owner.SessionID, owner.ThreadID)
		}
		return appwire.ThreadResumeResponse{}, appwire.Unavailable(discoveryErr.Error())
	}
	ownerDone := trace.stage(ctx, "owner_lookup")
	owner, _, err := lookupDaemonOwner(ctx, cfg, "", sessionID, true)
	ownerDone(err)
	if err != nil {
		return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
	}
	if owner.SessionID != "" {
		if _, directlyOwned := liveDaemonForThread(cfg.Roster, sessionID); !directlyOwned {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable("session is retained by " + localAppRef(owner.SessionID) + "; open the owning session or refresh after it stops")
		}
	}
	if cfg.Spawner == nil {
		return appwire.ThreadResumeResponse{}, appwire.Unavailable("spawner not configured")
	}
	prepareDone := trace.stage(ctx, "request_preparation")
	resumeReq, err := resumeRequestForConfig(cfg, sessionID)
	prepareDone(err)
	if err != nil {
		return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(err.Error())
	}
	// Serialize concurrent resumes of the same session behind a per-session
	// lock shared with the REST send path (kata sm1a). While one resume holds
	// the lock, another RPC mutation that also decided to resume waits here
	// rather than spawning a second daemon for the same exited session.
	if cfg.ResumeLocks != nil {
		// Double-check under the lock: a resume that completed while we waited
		// has already put the session in the roster, so reuse it instead of
		// spawning again. Only this Hub's exact flag-day protocol establishes
		// ownership; an older daemon can be healthy while remaining unroutable
		// through the current local source. A dead daemon may remain as a
		// crash marker and must fall through to spawning. Every serving
		// configuration builds its Roster unconditionally (the single
		// newWebServer call in main.go), so in production this re-check always
		// runs and a resume that queued behind a completed one reuses its
		// daemon instead of spawning a replacement; the nil guard is for tests
		// that construct a WebConfig without discovery.
		if cfg.Roster != nil {
			if le, ok := liveDaemonForThread(cfg.Roster, sessionID); ok &&
				le.Protocol == appwire.ProtocolVersion {
				return hubResumedThreadResponse(ctx, cfg, sources, le.SessionID, le.ThreadID)
			}
		}
		if state := cfg.ResumeLocks.RecoveryState(sessionID); state.ResumeRequired && !state.ExitConfirmed {
			// A hub death between PersistForceStop and ConfirmForceStop leaves
			// the requirement set with the exit unconfirmed, and Resume is the
			// only action that clears ResumeRequired — refusing unconditionally
			// strands the session. Discovery was refreshed above, before this
			// lock: a live or unverified claim on any recovery alias keeps the
			// refusal. Marker absence alone is still not exit proof — a denied
			// signal or a failed wait can leave the owner running markerless —
			// so the escape holds the authority to the Stop route's own proof
			// class: the process controller's verified ErrExited against the
			// identity the force stop persisted. Only then is the exit
			// confirmed durably and the launch allowed.
			if cfg.Roster == nil || recoveryGroupClaimExists(cfg.Roster, cfg.ResumeLocks.RecoveryAliases(sessionID)) {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable("resume owner exit is unconfirmed; verify the existing process before launching a replacement")
			}
			owner, ok := cfg.ResumeLocks.RecoveryOwner(sessionID)
			if !ok {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable("resume owner exit is unconfirmed; verify the existing process before launching a replacement")
			}
			controller := cfg.DaemonProcesses
			if controller == nil {
				controller = daemonprocess.NewController()
			}
			process, err := controller.Open(owner)
			if process != nil {
				// The refusal case is exactly the one where Open returns a
				// live process handle (a pidfd on Linux); the caller owns it.
				defer func() { _ = process.Close() }()
			}
			if !errors.Is(err, daemonprocess.ErrExited) {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable("resume owner exit is unconfirmed; verify the existing process before launching a replacement")
			}
			if err := cfg.ResumeLocks.ConfirmForceStop(state.ResumeSessionID); err != nil {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable("confirm recovery exit: " + err.Error())
			}
		}
	}
	resumeReq.CompletionOwned = launch.completionOwned
	resumeReq.ActiveResume = launch.active
	resumeDone := trace.stage(ctx, "spawner_resume")
	entry, err := cfg.Spawner.Resume(ctx, resumeReq)
	resumeDone(err)
	if err != nil {
		if cleanup, ok := errors.AsType[*resumeCleanupError](err); ok {
			launch.cleanupErr = cleanup
		}
		return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeFailureError(ctx, cfg, sessionID, err).Error())
	}
	if cfg.Roster != nil {
		refreshDone := trace.stage(ctx, "post_launch_discovery")
		refreshErr := hubRosterRefresh(ctx, cfg.Roster)
		refreshDone(refreshErr)
		if entry.Protocol == appwire.ProtocolVersion && entry.Endpoint != "" && entry.ThreadID != "" && !cfg.Roster.HasConfirmedEntry(entry) {
			// A successful scan can still miss an owner whose status probe
			// failed. Confirm the exact spawned endpoint through its read before
			// relying on the shared roster for subsequent requests.
			readDone := trace.stage(ctx, "daemon_read")
			read, err := cfg.Roster.ReadSpawnedThread(ctx, entry, func(ctx context.Context) (appwire.ThreadReadResponse, error) {
				return readSpawnedLocalThread(ctx, sources, entry)
			})
			readDone(err)
			if err != nil {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable(errors.Join(refreshErr, err).Error())
			}
			annotateThreadProjects([]appwire.Thread{read.Thread})
			read.Thread = applyHubForkCapability(cfg, read.Thread)
			return appwire.ThreadResumeResponse{Thread: read.Thread}, nil
		}
		if refreshErr != nil && !cfg.Roster.HasConfirmedEntry(entry) {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(refreshErr.Error())
		}
	}
	return hubResumedThreadResponse(ctx, cfg, sources, entry.SessionID, entry.ThreadID)
}

// resumeOwnership keeps the verified stopped transcript authoritative even when
// roster cleanup removes its marker, and reserves every retained ownership alias.
func resumeOwnership(cfg hubcore.WebConfig, requestedID, requestedRefID string) (string, []string, error) {
	var aliases []string
	visited := make(map[string]bool)
	current := requestedID
	for {
		if cfg.ResumeLocks.HasSeparatePendingRecovery(requestedID, current) {
			return "", nil, errors.New("resume target has a newer pending recovery; resume the current owning session")
		}
		if visited[current] {
			return "", nil, errors.New("completed session aliases contain a cycle; refresh session ownership before resuming")
		}
		visited[current] = true
		target, groupAliases, err := resumeOwnershipStep(cfg, current)
		if err != nil {
			return "", nil, err
		}
		aliases = append(aliases, groupAliases...)
		if target == current {
			break
		}
		current = target
	}
	if requestedRefID != "" {
		aliases = append(aliases, requestedRefID)
	}
	slices.Sort(aliases)
	return current, slices.Compact(aliases), nil
}

// Each completed group can lead to a newer group after daemon clear. Keep every
// hop's ownership aliases so one reservation covers the complete resolved path.
func resumeOwnershipStep(cfg hubcore.WebConfig, requestedID string) (string, []string, error) {
	aliases := cfg.ResumeLocks.RecoveryAliases(requestedID)
	durableTarget := cfg.ResumeLocks.RecoveryState(requestedID).ResumeSessionID
	target := durableTarget
	found := target != ""
	resolvedTarget := cfg.ResumeLocks.ResolvedSessionID(requestedID)
	referenceTarget := durableTarget
	if referenceTarget == "" {
		referenceTarget = resolvedTarget
	}
	var entries []rendezvous.Entry
	if cfg.RunDir != "" {
		var err error
		entries, err = rendezvous.ListStrict(cfg.RunDir)
		if err != nil {
			// Preserve normal discovery's partial-failure recording and direct probe.
			if cfg.Roster == nil {
				return "", nil, err
			}
			if owner, ok := liveDaemonForThread(cfg.Roster, requestedID); ok {
				entries = []rendezvous.Entry{owner.Entry}
			}
		}
	}
	var claims []rendezvous.Entry
	for _, entry := range entries {
		if !slices.Contains(forceStopAliases(entry), requestedID) && (referenceTarget == "" || !slices.Contains(forceStopAliases(entry), referenceTarget)) {
			continue
		}
		if entry.SourceID != "" && entry.SourceID != "local" {
			return "", nil, errors.New("daemon claims a foreign session source")
		}
		claims = append(claims, entry)
		aliases = append(aliases, forceStopAliases(entry)...)
	}
	if len(claims) > 0 {
		// A completed self-target cannot settle a conflicting exited successor.
		// Only a redirect can advance traversal toward a separately resolved group.
		completedRedirect := resolvedTarget
		if completedRedirect == requestedID {
			completedRedirect = ""
		}
		current, err := resumeClaimTarget(cfg, claims, durableTarget, completedRedirect)
		if err != nil {
			return "", nil, err
		}
		target, found = current, true
	}
	if !found && resolvedTarget != "" {
		target, found = resolvedTarget, true
	}
	if !found {
		if len(aliases) > 1 {
			return "", nil, errors.New("current session identity is missing for this recovery group; restore its daemon rendezvous marker before resuming")
		}
		target = requestedID
	}
	// An older partially overlapping group cannot revive a transcript redirected
	// by a newer stop. Its obligation remains until its own recovery is resolved.
	if state := cfg.ResumeLocks.RecoveryState(target); state.ResumeRequired && state.ResumeSessionID != "" && state.ResumeSessionID != target {
		return "", nil, errors.New("resume target belongs to a newer recovery; resume the current owning session")
	}
	aliases = append(aliases, requestedID, target)
	return target, aliases, nil
}

// Distinct retained transcripts need process evidence: old crash markers are
// not live owners, and an unverified process is never proof that a target is free.
func resumeClaimTarget(cfg hubcore.WebConfig, claims []rendezvous.Entry, durableTarget, resolvedTarget string) (string, error) {
	target := durableTarget
	conflict := false
	for _, entry := range claims {
		current := entry.SessionID
		if current == "" {
			current = entry.ThreadID
		}
		if current == "" {
			return "", errors.New("retained daemon has no current session identity")
		}
		if target != "" && target != current {
			conflict = true
		}
		if target == "" {
			target = current
		}
	}
	if !conflict {
		return target, nil
	}
	controller := cfg.DaemonProcesses
	if controller == nil {
		controller = daemonprocess.NewController()
	}
	liveTarget := ""
	for _, entry := range claims {
		target := hubcore.DaemonTarget(entry)
		process, err := controller.Open(target)
		if errors.Is(err, daemonprocess.ErrExited) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cannot verify retained daemon ownership: %w", err)
		}
		if err := process.Close(); err != nil {
			return "", fmt.Errorf("close retained daemon ownership: %w", err)
		}
		if liveTarget != "" && liveTarget != target.SessionID {
			return "", errors.New("multiple live daemons claim different current sessions")
		}
		liveTarget = target.SessionID
	}
	if liveTarget != "" {
		if durableTarget != "" && durableTarget != liveTarget {
			return "", errors.New("live daemon conflicts with the persisted recovery target")
		}
		return liveTarget, nil
	}
	if durableTarget != "" {
		return durableTarget, nil
	}
	// Completed aliases guide the next hop only after all competing claims are
	// verified exited. A live or unverified process never loses to this fallback.
	if resolvedTarget != "" {
		return resolvedTarget, nil
	}

	return "", errors.New("retained exited daemons have ambiguous current sessions and no persisted recovery target")
}

// resumeFailureError explains a failed replacement spawn when the daemon this
// hub refused to reuse is STILL running. That daemon holds the session's
// exclusive API-log reservation, so no replacement can start until it stops,
// and the spawn failure that comes back names only the locked file — it is
// raised inside the child process, which has no idea a hub is replacing an
// incompatible daemon, and its stock advice ("send work to the live session")
// is the one thing that cannot work here. The hub is the only party holding
// the blocking daemon's pid and address, so it owes the operator both plus the
// command that releases the session (kata ew86).
//
// Every hub path that resumes a local session runs into the same wedge, so
// they all report it through here: hubThreadResume above, which serves both
// /rpc thread/resume and the turn/start auto-resume. The original failure stays wrapped so a
// caller that inspects the error, rather than its text, still sees what the
// spawner returned.
//
// The roster is re-read rather than reused from the pre-spawn check: the spawn
// attempt takes seconds, and naming a pid that has since exited would send the
// operator after a process that is not there.
func resumeFailureError(ctx context.Context, cfg hubcore.WebConfig, sessionID string, err error) error {
	if cfg.Roster == nil {
		return err
	}
	discoveryDone := threadLifecycleFromContext(ctx).stage(ctx, "failure_discovery")
	if refreshErr := hubRosterRefresh(ctx, cfg.Roster); refreshErr != nil {
		discoveryDone(refreshErr)
		return errors.Join(err, refreshErr)
	}
	discoveryDone(nil)
	blocker, ok := cfg.Roster.Find(sessionID)
	if !ok || blocker.Crashed || blocker.Protocol == appwire.ProtocolVersion {
		return err
	}
	remedy := fmt.Sprintf("kill %d", blocker.PID)
	return fmt.Errorf(
		"session %s is still held by live daemon pid %d (AppWire protocol %q; this hub speaks %q), which the hub can neither route to nor replace. Stop it and resume again: %s. Replacement spawn failed: %w",
		sessionID, blocker.PID, blocker.Protocol, appwire.ProtocolVersion, remedy, err)
}

// hubResumedThreadResponse reads the freshly-resumed local thread back and
// wraps it in a ThreadResumeResponse. It is the shared tail of hubThreadResume:
// both a fresh spawn and the double-check reuse of an already-resumed daemon
// resolve the thread the same way. threadID falls back to sessionID when the
// rendezvous entry omitted it.
func hubResumedThreadResponse(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, sessionID, threadID string) (response appwire.ThreadResumeResponse, readErr error) {
	readDone := threadLifecycleFromContext(ctx).stage(ctx, "daemon_read")
	defer func() { readDone(readErr) }()
	if threadID == "" {
		threadID = sessionID
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	source, err := sourceForThread(sources, ref, "")
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	annotateThreadProjects([]appwire.Thread{threadResp.Thread})
	threadResp.Thread = applyHubForkCapability(cfg, threadResp.Thread)
	return appwire.ThreadResumeResponse{Thread: threadResp.Thread}, nil
}

func resumeRequestForConfig(cfg hubcore.WebConfig, id string) (hubcore.ResumeRequest, error) {
	req := hubcore.ResumeRequest{SessionID: id, AgentsDocPath: hubAgentsDocPath(cfg)}
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(id); ok {
			// Restore root, not the live working dir: a session actively
			// inside a worktree must resume at its pre-worktree home so
			// Task 18's resume re-entry (not this `--dir`) takes it back
			// into the worktree, honoring the lock/validation rules there
			// (native worktree tools spec §7 "Hub consumers").
			req.WorkingDir = hubcore.EffectiveWorkingDir(pe.Meta)
			req.StateDir = pe.StateDir
			provider := strings.TrimSpace(pe.Meta.ProfileID)
			if provider == "" {
				return hubcore.ResumeRequest{}, fmt.Errorf("session %s has no provider profile: cannot resume", id)
			}
			project, projectErr := identifier.ResolveProject(req.WorkingDir)
			if projectErr != nil {
				return hubcore.ResumeRequest{}, fmt.Errorf("resolve resume project: %w", projectErr)
			}
			req.Project = project
			if pe.Meta.Model != "" {
				req.Provider = provider
				req.Resolved = launchconfig.Resolved{Effective: launchconfig.Layer{
					Model: provider + "/" + pe.Meta.Model,
				}}
			}
			// The resumed daemon still honors the api_log launch option
			// (buildResumeArgs passes it through), so the session's launch
			// layers must be consulted here or an explicit choice is
			// silently dropped and the hub floor fills the gap. Only
			// api_log is carried: the persisted session meta governs model
			// and provider on resume. A resolve failure is swallowed — a
			// broken launch.toml must not block resume; the unset value
			// then falls through to the hub floor and the daemon's own
			// default.
			if resolved, resolveErr := hubResolveLaunch(hubLaunchConfigRoot(cfg), req.WorkingDir, launchconfig.Layer{}); resolveErr == nil {
				req.Resolved.Effective.APILog = resolved.Effective.APILog
			}
		}
	}
	return req, nil
}

func hubThreadFork(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if ref.SourceID != "local" {
		if params.Aside {
			return appwire.ThreadForkResponse{}, appwire.Unavailable("aside is only supported for local evener threads")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", "", func() (appwire.ThreadForkResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.ThreadForkResponse{}, err
			}
			if threadForkRequiresTurnCapability(params) {
				if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "fork"); err != nil {
					return appwire.ThreadForkResponse{}, err
				}
			}
			return source.ForkThread(ctx, params)
		})
	}
	// A request that cannot be carried out however it is routed is answered
	// first, ahead of every fence and lookup below. None of them belongs on the
	// path of a malformed call, and the recovery fence in particular reaches
	// ownershipEntry's scan of every project directory while a recovery is
	// unconfirmed — so a bad parameter combination would otherwise be reported
	// as an unavailable session, after that scan.
	turn, err := validateThreadForkParams(params)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	refFor := func(id string) string {
		if id == ref.ThreadID {
			return params.Ref
		}
		return ""
	}
	// A target the hub already knows is gone is answered before daemon
	// discovery, which is the one fence below that can fail for its own
	// reasons: deletion is terminal and tells the client to stop, a discovery
	// failure is transient and tells it to retry, so letting the transient
	// answer mask the terminal one keeps a client retrying a fork that can
	// never succeed.
	//
	// Both identities, for the same reason the locked pass covers both: a
	// stable alias resolves to the session a fork would branch, and a deletion
	// fence on that session is every bit as terminal as one on the alias. The
	// resolution here is the roster as it stands, which may be a scan old —
	// acceptable for an answer that is terminal whichever way it lands, and
	// re-derived below once the refresh has run. These reads take no lock; the
	// locked pass still re-reads both identities under theirs. With no deletion
	// store there is nothing to read, and resolving for it would be a roster
	// lookup per fork for an answer that cannot exist.
	if cfg.DeletionStore != nil {
		for _, id := range forkFenceTargets(ref.ThreadID, forkTargetSessionID(cfg, ref.ThreadID)) {
			if err := deletionFenceErrorNaming(cfg, refFor(id), id, params.Ref, ""); err != nil {
				return appwire.ThreadForkResponse{}, err
			}
		}
	}
	// One roster refresh serves every fence below, and it has to land before
	// them: a live-delegate fence read off the previous scan admits a delegate
	// the parent daemon picked up since, and this is also the scan that resolves
	// the session the branch will actually read — which the recovery and
	// deletion fences have to reserve alongside the alias the client asked
	// about, and cannot if they run first.
	if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, ref.ThreadID, ""); err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	sessionID := forkTargetSessionID(cfg, ref.ThreadID)
	// Sample every epoch, then take every lock, then check: an epoch read after
	// its own lock cannot see a recovery that began while this request waited
	// for that lock, and acquiring in sorted order is the convention
	// resumeThread and forceStopThread already follow, so two requests holding
	// these per-session locks cannot deadlock against each other.
	targets := forkFenceTargets(ref.ThreadID, sessionID)
	epochs := make(map[string]uint64, len(targets))
	for _, id := range targets {
		epochs[id] = sessionRequestRecoveryEpoch(ctx, cfg, refFor(id), id)
	}
	var unlockTargets []func()
	defer func() {
		for _, unlock := range slices.Backward(unlockTargets) {
			unlock()
		}
	}()
	lockOrder := slices.Clone(targets)
	slices.Sort(lockOrder)
	for _, id := range lockOrder {
		unlock, err := lockDeletionTarget(ctx, cfg, refFor(id), id)
		if err != nil {
			return appwire.ThreadForkResponse{}, err
		}
		unlockTargets = append(unlockTargets, unlock)
	}
	// The target had to be resolved before the locks, so that both identities
	// could be taken in one sorted pass. A thread/clear landing while this
	// request waited for them moves the session the ref names, and the fences
	// below would then reserve one session while the branch read another. Refuse
	// instead, the same recheck resumeThread performs after acquiring these
	// mutexes; the client re-reads and asks again.
	//
	// Both sources the hub can read cheaply under the locks have to still agree
	// with the pre-lock answer. The rendezvous is the one that catches a clear:
	// the roster learns of it only when its watcher next re-lists, so asking the
	// roster alone would compare the pre-lock reading to the same stale reading.
	// Refreshing the roster here is not the alternative — that probes daemons
	// while two per-session mutexes are held. The roster is still asked because
	// a snapshot that HAS caught up has to agree too: a move the roster already
	// knows about is refused by this same recheck rather than waiting for the
	// rendezvous read to notice it independently. In a configuration that gives
	// the hub only one of the two sources, both readings reach that one source
	// and the comparison is a re-read rather than a second opinion.
	current, rendezvousErr := forkTargetSessionIDUnderLock(cfg, ref.ThreadID)
	if rendezvousErr != nil {
		// The client's refusal is retryable and says nothing an operator can
		// act on. A hub whose process handles cannot be opened at all — a
		// permission or sandbox problem, not a per-fork one — would otherwise
		// refuse every fork with no server-side trace, so the one refusal that
		// reaches the client is traced here and only here.
		log.Printf("fork refused: cannot verify session ownership for %s: %v", params.Ref, rendezvousErr)
		return appwire.ThreadForkResponse{}, appwire.Unavailable("cannot verify session ownership: " + rendezvousErr.Error())
	}
	if current != sessionID || forkTargetSessionID(cfg, ref.ThreadID) != sessionID {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("session ownership changed; refresh before forking")
	}
	// Deletion across every identity first, then recovery across every
	// identity: the two refusals differ in kind — a deleted target is terminal
	// and carries MutationOutcomeTargetDeleted, a recovery fence is retryable
	// once the session is resumed — so which one a client is told about must
	// not depend on the order the locks above happened to need.
	for _, id := range targets {
		if err := deletionFenceErrorNaming(cfg, refFor(id), id, params.Ref, ""); err != nil {
			return appwire.ThreadForkResponse{}, err
		}
	}
	for _, id := range targets {
		if err := sessionActionRecoveryError(ctx, cfg, refFor(id), id, epochs[id]); err != nil {
			return appwire.ThreadForkResponse{}, err
		}
	}
	// Over the same targets as the two passes above, and for the same reason:
	// the capability projection fences both identities on the recovery signals
	// with this one predicate (hubForkRecoveryFencedNow for the alias,
	// hubForkResolvedSessionFenced for the session it resolves to), so the RPC
	// states the rule the same way rather than a second time in a second shape.
	// Both identities land on one roster entry whenever a daemon is live, so the
	// status conjunct costs a Find for the resolved id and adds no refusal there.
	// The recovery locks are keyed by id, so that conjunct stays per-identity and
	// is deliberately stricter over both.
	for _, id := range targets {
		if hubForkIdentityFenced(cfg, id, forkThreadOwnerFor(cfg, id)) {
			return appwire.ThreadForkResponse{}, sessionResumeRequiredError()
		}
	}
	entry, ok, entryErr := ownershipEntry(ctx, cfg, sessionID)
	if entryErr != nil {
		return appwire.ThreadForkResponse{}, appwire.Unavailable(entryErr.Error())
	}
	if !ok {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("local thread ownership is not available")
	}
	// Both identities are fenced: the transcript about to be branched, and the
	// one the capability projection answered for, so the RPC stays at least as
	// strict as the action it advertised. A live delegate is not a recovery
	// fence — an explicit resume cannot clear it — so it keeps its own refusal
	// rather than the shared recovery predicate's.
	if hubForkLiveDelegateFenced(cfg, ref.ThreadID) || hubForkLiveDelegateFenced(cfg, sessionID) {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("a delegate running inside a live daemon cannot be forked")
	}
	if params.Aside {
		stateDir := entry.StateDir
		if stateDir == "" {
			stateDir = cfg.StateDir
		}
		if stateDir == "" {
			return appwire.ThreadForkResponse{}, appwire.Unavailable("state dir not resolvable for parent thread")
		}
		childID, err := hubAsideSession(stateDir, sessionID)
		if err != nil {
			return appwire.ThreadForkResponse{}, err
		}
		if cfg.Past != nil {
			_, _ = cfg.Past.Rebuild()
		}
		childRef := appwire.Ref{SourceID: "local", ThreadID: childID}.String()
		return appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        childID,
			SessionID: childID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: childRef},
		}}, nil
	}
	stateDir := entry.StateDir
	if stateDir == "" {
		stateDir = cfg.StateDir
	}
	if stateDir == "" {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("state dir not resolvable for parent thread")
	}
	var childID, originalInput string
	if params.DeferInput {
		childID, originalInput, err = hubForkSessionAt(stateDir, sessionID, turn, params.Label)
	} else {
		childID, err = hubForkSession(stateDir, sessionID, turn, params.EditedInput, params.Label)
	}
	if err != nil {
		return appwire.ThreadForkResponse{}, forkDivergencePositionWireError(err)
	}
	if cfg.Past != nil {
		_, _ = cfg.Past.Rebuild()
	}
	childRef := appwire.Ref{SourceID: "local", ThreadID: childID}.String()
	return appwire.ThreadForkResponse{
		Thread: appwire.Thread{
			ID:        childID,
			SessionID: childID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: childRef},
		},
		OriginalInput: originalInput,
	}, nil
}

// validateThreadForkParams rejects the parameter combinations no fork can carry
// out, and returns the source turn a divergent fork branches from. It reads only
// the request, so the handler can answer a malformed one without refreshing
// daemon ownership or resolving the target's transcript.
func validateThreadForkParams(params appwire.ThreadForkParams) (int, error) {
	if params.Aside {
		if strings.TrimSpace(params.SourceItemKey) != "" || strings.TrimSpace(params.EditedInput) != "" || strings.TrimSpace(params.Label) != "" || params.DeferInput {
			return 0, appwire.InvalidParams("aside does not accept sourceItemKey, editedInput, deferInput, or label")
		}
		return 0, nil
	}
	turn, err := parseSourceItemKey(params.SourceItemKey)
	if err != nil {
		return 0, appwire.InvalidParams(err.Error())
	}
	if params.DeferInput && strings.TrimSpace(params.EditedInput) != "" {
		return 0, appwire.InvalidParams("editedInput and deferInput are mutually exclusive")
	}
	if !params.DeferInput && strings.TrimSpace(params.EditedInput) == "" {
		return 0, appwire.InvalidParams("editedInput is required")
	}
	return turn, nil
}

// forkThreadOwner is the roster's answer about one thread. liveDaemonForThread
// is a map lookup that falls through to a full roster snapshot clone-and-sort
// for any thread that is not a live daemon's current session — the bulk of a
// list response — so a caller that needs the answer more than once resolves it
// into this and hands it on rather than asking again.
type forkThreadOwner struct {
	entry hubcore.LiveEntry
	live  bool
}

func forkThreadOwnerFor(cfg hubcore.WebConfig, threadID string) forkThreadOwner {
	if cfg.Roster == nil {
		return forkThreadOwner{}
	}
	entry, live := liveDaemonForThread(cfg.Roster, threadID)
	return forkThreadOwner{entry: entry, live: live}
}

// statusFenced reports whether this daemon is announcing a recovery fence in
// the status it reports.
func (o forkThreadOwner) statusFenced() bool {
	if !o.live {
		return false
	}
	return hubForkRecoveryFenced(appwire.Thread{
		Status: appwire.ThreadStatus{Type: o.entry.Status, ActiveFlags: o.entry.ActiveFlags},
	})
}

// sessionID is the session this daemon is running, with the fallback order
// liveDaemonForSession uses: a probe that answered without naming its session
// leaves LiveEntry.SessionID empty, and the rendezvous entry it carries still
// names the daemon's current one. A live owner that names no session at all
// answers the requested id — the claim path under the locks answers the same,
// and a resolver that fell through to the recovery redirect here could disagree
// with it and refuse a fork for an ownership change that did not happen.
//
// It answers "" only when no daemon owns the thread.
func (o forkThreadOwner) sessionID(threadID string) string {
	if !o.live {
		return ""
	}
	return cmp.Or(o.entry.SessionID, o.entry.Entry.SessionID, o.entry.ThreadID, threadID)
}

// forkSource names which of the hub's two ownership sources answers a fork
// resolution, and so which one a caller trusts.
type forkSource int

const (
	// forkSourceRoster reads the roster, the source the pre-lock (advisory)
	// caller consults because it can be read without probing a daemon under a
	// per-session lock.
	forkSourceRoster forkSource = iota
	// forkSourceRendezvous reads the run dir's rendezvous entries, the source a
	// clear updates synchronously and so the source the under-lock
	// (authoritative) caller trusts.
	forkSourceRendezvous
)

// resolveForkTarget is the one source-deciding fork resolver: it reads the
// configured ownership source once and answers the session the requested thread
// names. The pre-lock and under-lock callers are thin wrappers that re-establish
// their own error contracts over this one decision.
//
// authoritative names the source the caller trusts. The two callers differ only
// in that choice and in how they treat a source they cannot read: the under-lock
// caller reads the rendezvous, which a clear updates synchronously, and reports
// the source's refusal; the pre-lock caller reads the roster and replaces the
// refusal with the shared redirect tail. When the trusted source is not
// configured the other one answers, and when neither is the redirect tail does —
// so a hub given only one source cannot make the two callers disagree about an
// alias nothing has touched and refuse a valid fork as an ownership change, and
// neither fallback recurses into the other resolver.
//
// With both sources configured, which production always is (main.go builds the
// roster from the run dir that becomes WebConfig.RunDir), the rendezvous stays
// authoritative under the locks and the roster ahead of them.
func resolveForkTarget(cfg hubcore.WebConfig, threadID string, authoritative forkSource) (string, error) {
	switch {
	case authoritative == forkSourceRoster && cfg.Roster != nil:
		return forkTargetFromRoster(cfg, threadID), nil
	case authoritative == forkSourceRendezvous && cfg.RunDir != "":
		return forkTargetFromRendezvous(cfg, threadID)
	case cfg.Roster != nil:
		return forkTargetFromRoster(cfg, threadID), nil
	case cfg.RunDir != "":
		return forkTargetFromRendezvous(cfg, threadID)
	default:
		return forkRedirectSessionID(cfg, threadID), nil
	}
}

// forkTargetFromRoster resolves through the roster: the live owner it names, or
// the shared redirect tail when no daemon owns the thread. It never reports an
// error; a roster the hub holds is always answerable.
func forkTargetFromRoster(cfg hubcore.WebConfig, threadID string) string {
	return forkTargetSessionIDFor(cfg, threadID, forkThreadOwnerFor(cfg, threadID))
}

// forkTargetFromRendezvous resolves through the run dir's rendezvous entries,
// the source a clear updates synchronously: a daemon rewrites its entry as it
// swaps sessions (cmd/evener/serve.go's clear hook writes it through
// rvreg.UpdateSessionID, before the replacement session goes live, so the file
// is never behind the daemon). That is why resumeThread's recheck is not fooled
// by a roster that has not caught up, and why the fork rechecks here.
//
// Every local claim on the alias that forkClaimIsLiveOwner admits is collected,
// not just the first the directory listed: two daemons can claim one stable
// workspace ref, and nothing downstream catches that — ownershipEntry refuses a
// session id found in two project directories, never a second daemon claiming
// the same alias. So the claims go through resumeClaimTarget, the conflict
// check the resume path already uses for this shape, which settles them by
// liveness and refuses when it cannot.
//
// It diverges from resumeOwnershipStep in one deliberate way: a ListStrict
// failure refuses the fork as unverifiable, where resumeOwnershipStep degrades
// to the roster. A caller that must not resolve through a source it could not
// read gets that refusal, and a retryable one is the safe direction for a
// mutation. A claim whose own process cannot be verified is refused the same
// way, and for the same reason.
//
// For a thread nothing currently claims it answers the requested id's redirect,
// the same tail the roster-backed resolution reaches when no live daemon owns
// the thread.
func forkTargetFromRendezvous(cfg hubcore.WebConfig, threadID string) (string, error) {
	entries, err := rendezvous.ListStrict(cfg.RunDir)
	if err != nil {
		return "", err
	}
	controller := cfg.DaemonProcesses
	if controller == nil {
		controller = daemonprocess.NewController()
	}
	var claims []rendezvous.Entry
	for _, entry := range entries {
		if entry.SourceID != "" && entry.SourceID != "local" {
			continue
		}
		if !slices.Contains(forceStopAliases(entry), threadID) {
			continue
		}
		live, verifyErr := forkClaimIsLiveOwner(controller, entry)
		if verifyErr != nil {
			// Which entry could not be verified rides on the error rather than
			// being logged here: the pre-lock caller swallows the error, so
			// logging at the failure would trace forks that were never refused.
			// The refusing call site logs it once.
			return "", fmt.Errorf("daemon claiming %s (pid %d): %w", localAppRef(threadID), entry.PID, verifyErr)
		}
		if live {
			claims = append(claims, entry)
		}
	}
	if len(claims) > 0 {
		// The claims are the authority when any exist; the recovery redirect
		// below is the fallback for an alias nothing claims, so neither target
		// is passed in here.
		return resumeClaimTarget(cfg, claims, "", "")
	}
	return forkRedirectSessionID(cfg, threadID), nil
}

// forkRedirectSessionID is what a thread no live daemon owns resolves to: the
// session a recovery redirected it onto, else the thread itself. A completed
// explicit resume records that redirect and never clears it
// (ResumeLocks.RecordResolvedSession), so an alias whose daemon has since
// stopped still names the session the resume settled on. Both resolvers end
// here, which is what keeps them from disagreeing while nothing is changing:
// one that stopped at the alias would read "ownership changed" off a redirect
// that has been stable since before the request started.
// Redirects chain. PersistForceStop rewrites only the aliases of the group it
// is handed and RecordResolvedSession writes onto whichever group an alias
// currently points at, so resuming A onto B and later B onto C leaves both
// records standing: one hop would branch B, a session already retired.
// resumeOwnership traverses the same chains for the same reason, which is why
// its loop and cycle guard exist.
//
// A cycle stops rather than refusing. Both resolvers share this tail, so
// whatever a corrupted chain answers they answer alike and no fork is refused
// for an ownership change that did not happen; whether that id can be forked at
// all is still ownershipEntry's to decide.
//
// A completed redirect is followed only while the session it names has not
// durably ended. RecordResolvedSession writes it once and never clears it, so
// once that session has itself ended and awaits an explicit resume, following
// the hop would route every fork into the ended session's recovery fence
// forever. The alias falls back to itself there; whether the alias can be
// forked is still the fence and ownership checks' to decide. A merely temporary
// stop fence is not an ending — a refused force stop gives it back — so the
// redirect still resolves and the target's own fence refuses the fork. A
// durable, still-pending recovery target (ResumeSessionID) is likewise not
// retired: it is the live obligation, and the fork is refused through the
// target's own fence until that resume completes.
func forkRedirectSessionID(cfg hubcore.WebConfig, threadID string) string {
	if cfg.ResumeLocks == nil {
		return threadID
	}
	current := threadID
	seen := map[string]bool{current: true}
	for {
		next := cfg.ResumeLocks.RecoveryState(current).ResumeSessionID
		if next == "" {
			next = cfg.ResumeLocks.ResolvedSessionID(current)
			if next != "" && cfg.ResumeLocks.RecoveryEnded(next) {
				return current
			}
		}
		if next == "" || next == current || seen[next] {
			return current
		}
		seen[next] = true
		current = next
	}
}

// forkFenceTargets is every identity one fork has to reserve, in request order:
// the alias the client asked about, then the session the branch actually reads
// when the two differ. Fences are checked in this order so a refusal reports
// the identity the client named whenever both are fenced the same way; the
// locks are taken over a sorted copy, which is a separate concern.
func forkFenceTargets(requestedID, sessionID string) []string {
	targets := []string{requestedID}
	if sessionID != "" && sessionID != requestedID {
		targets = append(targets, sessionID)
	}
	return targets
}

// forkTargetSessionID resolves the transcript a fork request names. A daemon
// keeps its stable workspace ref across thread/clear while its session id moves
// on (cmd/evener/serve.go's clear hook writes the replacement id into the
// rendezvous entry and leaves WorkspaceRef alone), and both the live read and
// the capability projection answer that stable ref with the CURRENT session. A
// fork has to branch the transcript the client was reading; the requested ref
// stays the identity for recovery and deletion fencing, which reserve the
// alias the client asked about.
//
// It is the pre-lock caller of resolveForkTarget: the roster answers it, and a
// source it cannot read has no refusal to make, so it falls back to the shared
// redirect tail. The under-lock pass reaches the same source again and refuses
// with the reason.
func forkTargetSessionID(cfg hubcore.WebConfig, threadID string) string {
	sessionID, err := resolveForkTarget(cfg, threadID, forkSourceRoster)
	if err != nil {
		return forkRedirectSessionID(cfg, threadID)
	}
	return sessionID
}

// forkTargetSessionIDUnderLock is the under-lock caller of resolveForkTarget:
// the rendezvous answers it and its refusal is reported, so fork admission can
// refuse a target it cannot verify rather than resolve through it.
func forkTargetSessionIDUnderLock(cfg hubcore.WebConfig, threadID string) (string, error) {
	return resolveForkTarget(cfg, threadID, forkSourceRendezvous)
}

// forkTargetSessionIDFor is forkTargetSessionID with the roster already asked,
// for a caller that needs the same answer more than once in one pass.
func forkTargetSessionIDFor(cfg hubcore.WebConfig, threadID string, owner forkThreadOwner) string {
	if sessionID := owner.sessionID(threadID); sessionID != "" {
		return sessionID
	}
	// No live daemon owns the thread, so the recovery redirect answers — the
	// same tail forkTargetSessionIDUnderLock reaches, so the pre-lock reading
	// and the recheck cannot disagree while nothing is moving. The roster has
	// already applied forkClaimIsLiveOwner's rule for this reading: a marker
	// whose process is gone is retained as crashed and liveDaemonForThread
	// skips it.
	return forkRedirectSessionID(cfg, threadID)
}

// forkClaimIsLiveOwner reports whether a rendezvous claim still has a daemon
// behind it. This is the single liveness rule both fork resolutions answer
// from. The pre-lock one gets it from the roster, which marks an entry whose
// PID is confirmed gone as Crashed and retains it only for crash reporting, so
// liveDaemonForThread never returns one; the under-lock collection applies it
// here. Without that, a daemon that cleared to a new session and then crashed
// would leave a marker the two readings disagreed about, and every fork through
// its alias would be refused for an ownership change that never happened.
//
// The two sides reach that rule through different mechanisms and neither should
// be "simplified" into the other: the roster asks whether the PID exists at all
// (processAlive's signal 0, internal/hubcore/roster_unix.go), while this probe
// binds the process and verifies it is still the generation the rendezvous
// describes (daemonprocess's pidfd on Linux, an O_NOFOLLOW handle plus verify()
// on Darwin). They agree on the case that decides this — a PID confirmed gone
// is dead to both — and the stricter one only ever withholds liveness from a
// PID that has been reused, which is not an owner either.
//
// There are three outcomes, not two, and the third is why this returns an
// error. A claim whose process verifies is live. A claim whose process has
// exited is gone and is dropped. A claim whose verification fails for any other
// reason — a malformed entry, a stale one, a PID since reused — is
// UNVERIFIABLE, and the caller refuses the fork rather than resolving through
// it: with a single claim resumeClaimTarget returns it without opening
// anything, so calling an unverifiable claim live would let a file alone
// authorize a fork with ownership never established.
//
// The caller stops at the first unverifiable claim, so an alias carrying one
// unverifiable entry and one that verifies refuses as unverifiable without
// reaching the ambiguity path — the conservative reading: a set the hub cannot
// fully account for is not a set it should pick a winner from.
//
// An ambiguous alias is therefore probed twice, here and again in
// resumeClaimTarget's conflict branch; that is accepted rather than threaded
// through, since only a contested alias pays it.
func forkClaimIsLiveOwner(controller daemonprocess.Controller, entry rendezvous.Entry) (bool, error) {
	process, err := controller.Open(hubcore.DaemonTarget(entry))
	if errors.Is(err, daemonprocess.ErrExited) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The daemon answered, which is the whole question here; the probe handle
	// is released immediately and a close failure does not change that answer.
	_ = process.Close()
	return true, nil
}

func threadForkRequiresTurnCapability(params appwire.ThreadForkParams) bool {
	return strings.TrimSpace(params.SourceItemKey) != "" ||
		strings.TrimSpace(params.EditedInput) != "" ||
		strings.TrimSpace(params.Label) != "" ||
		params.DeferInput
}

// parseSourceItemKey resolves a transcriptKey (transcriptindex.ItemKey) to the
// 1-based transcript entry index agent.ForkSessionAtUserTurn and
// agent.ForkSession divergence on. The key's turn id is not otherwise
// validated here: whether the named entry exists, and whether it is a
// USER_INPUT entry, is answered downstream when the fork actually reads the
// parent transcript.
func parseSourceItemKey(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("sourceItemKey is required")
	}
	_, position, err := transcriptindex.ParseItemKey(raw)
	if err != nil {
		return 0, fmt.Errorf("sourceItemKey is not a valid transcript item key: %w", err)
	}
	if position.Entry == 0 {
		return 0, errors.New("sourceItemKey names the transcript header, which is not a turn")
	}
	return int(position.Entry), nil
}

// forkDivergencePositionWireError maps agent.ErrDivergencePositionOutOfRange
// and agent.ErrDivergencePositionNotUserInput to appwire.InvalidParams: the
// sourceItemKey named an entry the parent transcript doesn't have, or one
// that isn't a USER_INPUT entry. Both are refusals of the client's chosen
// item, not a hub failure, so they must not fall through to InternalError.
// Any other error (a missing parent, an I/O failure, ...) is returned
// unchanged.
func forkDivergencePositionWireError(err error) error {
	if errors.Is(err, agent.ErrDivergencePositionOutOfRange) || errors.Is(err, agent.ErrDivergencePositionNotUserInput) {
		return appwire.InvalidParams(err.Error())
	}
	return err
}

// readSpawnedLocalThread keeps pre-admission reads in the same recovery scope
// as ordinary calls so a stalled read cannot retain resume ownership forever.
func readSpawnedLocalThread(ctx context.Context, sources *appsource.Registry, entry rendezvous.Entry) (appwire.ThreadReadResponse, error) {
	local, err := spawnedLocalDaemonSource(sources)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return local.ReadThreadAtEntry(ctx, entry, appwire.ThreadReadParams{Ref: localSpawnWorkspaceRef(entry)})
}

func spawnedLocalDaemonSource(sources *appsource.Registry) (*appsource.LocalDaemonSource, error) {
	source, ok := sources.Source("local")
	if ok {
		if local, ok := source.(*appsource.LocalDaemonSource); ok {
			return local, nil
		}
	}
	return nil, appwire.Unavailable("local daemon source is not configured")
}
