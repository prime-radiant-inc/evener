package hub

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// daemonIdentity renders the exact ownership identity of one discovered
// daemon from its current rendezvous entry. Generation is computed by
// rendezvous.OwnershipFingerprint — the single hashing implementation; the
// hub never maintains a second one. The identity is recomputed from the
// current entry on every call, so a daemon replacement yields a different
// rendered target.
func daemonIdentity(entry rendezvous.Entry) appwire.DaemonIdentity {
	ref := entry.SessionID
	if ref == "" {
		ref = entry.ThreadID
	}
	if ref == "" {
		if workspace, err := appwire.ParseRef(entry.WorkspaceRef); err == nil && workspace.SourceID == "local" {
			ref = workspace.ThreadID
		}
	}
	return appwire.DaemonIdentity{
		Ref:        localAppRef(ref),
		PID:        entry.PID,
		StartedAt:  entry.StartedAt.UTC().Format(time.RFC3339Nano),
		Generation: rendezvous.OwnershipFingerprint(entry),
	}
}

// registerDaemonHandlers registers the hub-owned resident inventory and the
// identity-fenced safe-retire action. The catalog scopes these Hub/Both; the
// handlers here are the real implementations, not stubs.
func registerDaemonHandlers(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDaemonList, func(ctx context.Context, _ appwire.DaemonListParams) (appwire.DaemonListResponse, error) {
		return listDaemons(ctx, cfg)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDaemonRetire, func(ctx context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		return retireDaemon(ctx, cfg, sources, params)
	})
}

// listDaemons renders the resident inventory from the configured roster
// snapshot. It consumes the last published snapshot only — polling this list
// never probes a daemon, opens a process handle, or increments daemon
// activity. Confirmed and unconfirmed claims merge by exact ownership
// fingerprint, never by alias; archive/name decoration comes from the saved
// metadata index independently of sidebar filtering; unknown and stale probe
// states stay explicit; only a fresh compatible lifecycle enables Retire.
// Force-stop availability means the configured verified path exists — never
// proof that a future action will pass verification.
func listDaemons(_ context.Context, cfg hubcore.WebConfig) (appwire.DaemonListResponse, error) {
	if cfg.Roster == nil {
		return appwire.DaemonListResponse{}, appwire.Unavailable("local daemon discovery is not configured")
	}
	var archiveDecisions map[hubcore.ArchiveKey]bool
	if cfg.Archive != nil {
		decisions, err := cfg.Archive.Decisions()
		if err != nil {
			return appwire.DaemonListResponse{}, appwire.Unavailable(fmt.Sprintf("read archive decisions: %v", err))
		}
		archiveDecisions = decisions
	}
	// The verified force-stop path is configured when ownership discovery and
	// recovery locks exist. Protocol compatibility does not gate it: force
	// stop verifies the process locally and supports older-protocol daemons.
	canForceStop := cfg.RunDir != "" && cfg.ResumeLocks != nil
	response := appwire.DaemonListResponse{
		DefaultTimeoutMillis: cfg.DaemonIdleTimeout.Milliseconds(),
		Daemons:              []appwire.DaemonResident{},
	}
	seen := make(map[string]bool)
	for _, resident := range cfg.Roster.ResidentEntries() {
		confirmed := resident.Confirmed
		if confirmed != nil && confirmed.Crashed {
			continue // retained crash evidence is not a resident
		}
		identity := daemonIdentity(resident.Entry)
		if seen[identity.Generation] {
			continue // the confirmed view and the raw claim agree; one row per exact identity
		}
		seen[identity.Generation] = true
		row := appwire.DaemonResident{
			Identity:     identity,
			Protocol:     resident.Entry.Protocol,
			CanForceStop: canForceStop,
		}
		sessionID := resident.Entry.SessionID
		if sessionID == "" {
			sessionID = resident.Entry.ThreadID
		}
		if confirmed != nil && confirmed.SessionID != "" {
			sessionID = confirmed.SessionID
		}
		switch confirmed {
		case nil:
			row.Compatibility = "unknown"
			row.ProbeState = "unknown"
		default:
			if resident.Entry.Protocol == appwire.ProtocolVersion {
				row.Compatibility = "compatible"
			} else {
				row.Compatibility = "incompatible"
			}
			if confirmed.LifecycleFresh && confirmed.Lifecycle != nil {
				row.ProbeState = "current"
				lifecycle := *confirmed.Lifecycle
				// Producer discipline: "no blockers" must never decode as
				// "capability unknown".
				if lifecycle.Blockers == nil {
					lifecycle.Blockers = []appwire.DaemonBlocker{}
				}
				row.Lifecycle = &lifecycle
			} else {
				row.ProbeState = "stale"
			}
		}
		row.CanRetire = row.Compatibility == "compatible" && row.ProbeState == "current" && row.Lifecycle != nil && row.Lifecycle.Phase == "resident"
		if cfg.Past != nil && sessionID != "" {
			if pastEntry, ok := cfg.Past.Find(sessionID); ok {
				row.Name = pastEntry.Meta.Name
			}
		}
		if sessionID != "" && archiveDecisions[hubcore.ArchiveKey{Kind: "session", ID: sessionID}] {
			row.Archived = true
		}
		response.Daemons = append(response.Daemons, row)
	}
	slices.SortFunc(response.Daemons, func(a, b appwire.DaemonResident) int {
		if c := strings.Compare(a.Identity.Ref, b.Identity.Ref); c != 0 {
			return c
		}
		// RFC3339Nano trims trailing fractional zeros, making the rendered
		// string variable-width: lexicographic comparison is not chronological
		// ("…00Z" > "…00.5Z" because 'Z' > '.'). Parse as time.Time instead.
		aTime, _ := time.Parse(time.RFC3339Nano, a.Identity.StartedAt)
		bTime, _ := time.Parse(time.RFC3339Nano, b.Identity.StartedAt)
		if c := aTime.Compare(bTime); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Identity.PID, b.Identity.PID); c != 0 {
			return c
		}
		return strings.Compare(a.Identity.Generation, b.Identity.Generation)
	})
	return response, nil
}

// retireDaemon forwards a safe-retire request to the exact owning daemon.
// The rendered identity is recomputed from the current rendezvous entry and
// compared on every call — before any fence, and again after the sorted alias
// locks — so a stale row conflicts before any RPC reaches a replacement.
// Ownership, deletion, and protocol fences are revalidated under the locks.
// The daemon's answer passes through verbatim: a fresh blocker refusal or
// Accepted with the current lifecycle; acceptance is never reported as exit.
func retireDaemon(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
	ref, err := appwire.ParseRef(params.Identity.Ref)
	if err != nil || ref.SourceID != "local" {
		return appwire.DaemonRetireResponse{}, appwire.InvalidParams("daemon retire requires a local session ref")
	}
	if cfg.RunDir == "" || cfg.ResumeLocks == nil {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("local session ownership is not configured")
	}
	recoveryTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	entry, err := forceStopEntry(cfg.RunDir, ref.ThreadID, cfg.DaemonProcesses, nil, recoveryTarget)
	if err != nil {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable(err.Error())
	}
	if err := expectedDaemonConflict(entry, &params.Identity); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	// Serialize against resume/clear through every session alias. Retire never
	// calls BeginForceStop/PersistForceStop: the daemon stays the retirement
	// authority and this path leaves no recovery fence behind.
	aliases := forceStopAliases(entry)
	for _, id := range aliases {
		cfg.ResumeLocks.For(id).Lock()
	}
	defer func() {
		for _, alias := range slices.Backward(aliases) {
			cfg.ResumeLocks.For(alias).Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	currentTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	if currentTarget != recoveryTarget {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("session recovery authority changed; retry daemon retire")
	}
	current, err := forceStopRereadEntry(cfg.RunDir, ref.ThreadID, entry, cfg.DaemonProcesses, currentTarget)
	if err != nil {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable(err.Error())
	}
	if err := expectedDaemonConflict(current, &params.Identity); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	if err := deletionFenceError(cfg, params.Identity.Ref, ref.ThreadID, ""); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	// No automatic fallback against a peer that cannot answer the current
	// protocol's safe-retire RPC.
	if current.Protocol != appwire.ProtocolVersion || current.Endpoint == "" {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("daemon does not speak the current protocol; safe retire is unavailable")
	}
	if sources == nil {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("local daemon source is not configured")
	}
	source, ok := sources.Source("local")
	if !ok {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("local daemon source is not configured")
	}
	local, ok := source.(*appsource.LocalDaemonSource)
	if !ok {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("local daemon source cannot retire daemons")
	}
	return local.RetireDaemonAtEntry(ctx, current, params)
}
