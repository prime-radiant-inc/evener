package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
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
	hubResolvePlugins  = func(ctx context.Context, pluginRoot string, dirs []string, enabled *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.NewManager(pluginRoot).ResolveForLaunch(ctx, dirs, enabled)
	}
)

func hubThreadStart(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	sourceID := launchSourceID(params)
	if sourceID != "" && sourceID != "local" {
		source, ok := sources.Source(sourceID)
		if !ok || source == nil {
			return appwire.ThreadStartResponse{}, appwire.Unavailable("spawn source is not available: " + sourceID)
		}
		return source.StartThread(ctx, params)
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
	pluginResolution, pluginErr := hubResolvePlugins(ctx, cfg.PluginRoot, spawnResolved.Effective.PluginDirs, spawnResolved.Effective.EnabledPlugins)
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
	if cfg.ResumeLocks != nil {
		epoch := sessionRequestRecoveryEpoch(ctx, cfg, "", requestedID)
		if err := sessionConnectionRecoveryError(ctx, cfg, "", requestedID); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		target, aliases, err := resumeOwnership(cfg, requestedID, requestedRefID)
		if err != nil {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
		}
		epochs := make(map[string]uint64, len(aliases))
		for _, id := range aliases {
			epochs[id] = sessionRequestRecoveryEpoch(ctx, cfg, "", id)
		}
		epochs[requestedID] = epoch
		// Use force stop's sorted ownership order, retaining the original mutexes.
		for _, id := range aliases {
			cfg.ResumeLocks.For(id).Lock()
		}
		defer func() {
			for _, id := range slices.Backward(aliases) {
				cfg.ResumeLocks.For(id).Unlock()
			}
		}()
		for _, id := range aliases {
			if err := sessionConnectionRecoveryError(ctx, cfg, "", id); err != nil {
				return appwire.ThreadResumeResponse{}, err
			}
			state := cfg.ResumeLocks.RecoveryState(id)
			if state.Epoch != epochs[id] || state.Stopping > 0 || (automatic && state.ResumeRequired) {
				return appwire.ThreadResumeResponse{}, sessionRecoveryAdmissionError{appwire.Unavailable("session recovery requires a fresh explicit thread/resume request")}
			}
		}
		currentTarget, currentAliases, err := resumeOwnership(cfg, requestedID, requestedRefID)
		if err != nil {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
		}
		if currentTarget != target || !slices.Equal(currentAliases, aliases) {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable("session ownership changed; refresh before resuming")
		}
		sessionID = target
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

	if err := deletionFenceError(cfg, params.Ref, requestedID, ""); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	for _, id := range []string{requestedID, sessionID} {
		if err := deletionFenceError(cfg, "", id, ""); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
	}

	var discoveryErr error
	if cfg.Roster != nil {
		discoveryErr = hubRosterRefresh(ctx, cfg.Roster)
	}
	if err := daemonRestartRequiredError(ctx, cfg, "", sessionID, ""); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	if discoveryErr != nil {
		// Incomplete discovery cannot authorize a replacement, but a direct
		// probe can establish that a previously confirmed owner still serves
		// this session. Keep the global discovery failure for other owners.
		if owner, ok := liveDaemonForThread(cfg.Roster, sessionID); ok && owner.Protocol == appwire.ProtocolVersion {
			if err := cfg.Roster.RefreshEntry(ctx, owner.Entry); err != nil {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable(errors.Join(discoveryErr, err).Error())
			}
			return hubResumedThreadResponse(ctx, cfg, sources, owner.SessionID, owner.ThreadID)
		}
		return appwire.ThreadResumeResponse{}, appwire.Unavailable(discoveryErr.Error())
	}
	owner, _, err := lookupDaemonOwner(ctx, cfg, "", sessionID, true)
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
	resumeReq, err := resumeRequestForConfig(cfg, sessionID)
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
		// crash marker and must fall through to spawning.
		if cfg.Roster != nil {
			if le, ok := liveDaemonForThread(cfg.Roster, sessionID); ok &&
				le.Protocol == appwire.ProtocolVersion {
				return hubResumedThreadResponse(ctx, cfg, sources, le.SessionID, le.ThreadID)
			}
		}
	}
	entry, err := cfg.Spawner.Resume(ctx, resumeReq)
	if err != nil {
		return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeFailureError(ctx, cfg, sessionID, err).Error())
	}
	if cfg.Roster != nil {
		refreshErr := hubRosterRefresh(ctx, cfg.Roster)
		if entry.Protocol == appwire.ProtocolVersion && entry.Endpoint != "" && entry.ThreadID != "" && !cfg.Roster.HasConfirmedEntry(entry) {
			// A successful scan can still miss an owner whose status probe
			// failed. Confirm the exact spawned endpoint through its read before
			// relying on the shared roster for subsequent requests.
			read, err := cfg.Roster.ReadSpawnedThread(ctx, entry, func(ctx context.Context) (appwire.ThreadReadResponse, error) {
				return readSpawnedLocalThread(ctx, sources, entry)
			})
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
		current := entry.SessionID
		if current == "" {
			current = entry.ThreadID
		}
		process, err := controller.Open(daemonprocess.Target{PID: entry.PID, SessionID: current, StateDir: entry.StateDir, StartedAt: entry.StartedAt})
		if errors.Is(err, daemonprocess.ErrExited) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cannot verify retained daemon ownership: %w", err)
		}
		if err := process.Close(); err != nil {
			return "", fmt.Errorf("close retained daemon ownership: %w", err)
		}
		if liveTarget != "" && liveTarget != current {
			return "", errors.New("multiple live daemons claim different current sessions")
		}
		liveTarget = current
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
	if refreshErr := hubRosterRefresh(ctx, cfg.Roster); refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
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
func hubResumedThreadResponse(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, sessionID, threadID string) (appwire.ThreadResumeResponse, error) {
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
	refFor := func(id string) string {
		if id == ref.ThreadID {
			return params.Ref
		}
		return ""
	}
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
		unlockTargets = append(unlockTargets, lockDeletionTarget(cfg, refFor(id), id))
	}
	// The target had to be resolved before the locks, so that both identities
	// could be taken in one sorted pass. A thread/clear landing while this
	// request waited for them moves the session the ref names, and the fences
	// below would then reserve one session while the branch read another. Refuse
	// instead, the same recheck resumeThread performs after acquiring these
	// mutexes; the client re-reads and asks again.
	if forkTargetSessionID(cfg, ref.ThreadID) != sessionID {
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
	if hubForkLiveStatusFenced(cfg, ref.ThreadID) {
		return appwire.ThreadForkResponse{}, sessionResumeRequiredError()
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
	// strict as the action it advertised.
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
		return appwire.ThreadForkResponse{}, err
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
		if strings.TrimSpace(params.SourceTurnID) != "" || strings.TrimSpace(params.EditedInput) != "" || strings.TrimSpace(params.Label) != "" || params.DeferInput {
			return 0, appwire.InvalidParams("aside does not accept sourceTurnId, editedInput, deferInput, or label")
		}
		return 0, nil
	}
	turn, err := parseSourceTurnID(params.SourceTurnID)
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

// hubForkLiveStatusFenced reports whether the daemon behind this thread is
// announcing a recovery fence in the status it reports. Both the capability
// projection and fork admission decide that signal here, with one predicate
// rather than two descriptions of it.
//
// It reads the roster and never probes a daemon itself, so what it answers is
// as fresh as the last scan. Admission refreshes immediately before calling it,
// so there it is current; the projection reads whatever scan last landed, and
// its staleness is bounded by the roster's own change notification — a daemon
// that raises or drops a status flag moves the fingerprint (rosterFingerprint)
// even when nothing else about it changed, so navigation re-projects rather
// than holding the old answer indefinitely.
//
// Its restart-required conjunct overlaps refreshDaemonRestartRequiredError,
// which in admission runs first and refuses with its own error whenever it can
// resolve the same daemon as this thread's owner.
func hubForkLiveStatusFenced(cfg hubcore.WebConfig, threadID string) bool {
	if cfg.Roster == nil {
		return false
	}
	owner, ok := liveDaemonForThread(cfg.Roster, threadID)
	if !ok {
		return false
	}
	return hubForkRecoveryFenced(appwire.Thread{
		Status: appwire.ThreadStatus{Type: owner.Status, ActiveFlags: owner.ActiveFlags},
	})
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
func forkTargetSessionID(cfg hubcore.WebConfig, threadID string) string {
	if cfg.Roster == nil {
		return threadID
	}
	owner, ok := liveDaemonForThread(cfg.Roster, threadID)
	if !ok {
		return threadID
	}
	// Same fallback order as liveDaemonForSession: a probe that answered without
	// naming its session leaves LiveEntry.SessionID empty, and the rendezvous
	// entry it carries still names the daemon's current one.
	return cmp.Or(owner.SessionID, owner.Entry.SessionID, owner.ThreadID, threadID)
}

func threadForkRequiresTurnCapability(params appwire.ThreadForkParams) bool {
	return strings.TrimSpace(params.SourceTurnID) != "" ||
		strings.TrimSpace(params.EditedInput) != "" ||
		strings.TrimSpace(params.Label) != "" ||
		params.DeferInput
}

func parseSourceTurnID(raw string) (int, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "turn_"))
	if raw == "" {
		return 0, errors.New("sourceTurnId is required")
	}
	turn, err := strconv.Atoi(raw)
	if err != nil || turn < 1 {
		return 0, errors.New("sourceTurnId must be a positive turn number")
	}
	return turn, nil
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
