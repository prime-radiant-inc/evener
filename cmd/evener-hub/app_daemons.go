package hub

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
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

// retireOwnershipCapability is a test-only seam: nil in production, where the
// platform capability is read directly. Safe retire requires the daemon's
// kernel-serialized ownership contract, and a single platform build cannot
// exercise both branches of CanRetire, so tests override this to prove the hub
// agrees with the daemon's own retirement refusal. It is read on the daemon
// list path, which can run on a goroutine outliving the test that set it, so
// the seam is synchronized: a plain read racing a test's plain write is a data
// race under the Go memory model, and this repository runs -race.
var (
	retireOwnershipCapabilityMu sync.Mutex
	retireOwnershipCapability   func() bool
)

// setRetireOwnershipCapability installs fn as the ownership-capability seam and
// returns a function that restores the previous value. Tests pair it with
// t.Cleanup; production never calls it.
func setRetireOwnershipCapability(fn func() bool) func() {
	retireOwnershipCapabilityMu.Lock()
	prev := retireOwnershipCapability
	retireOwnershipCapability = fn
	retireOwnershipCapabilityMu.Unlock()
	return func() {
		retireOwnershipCapabilityMu.Lock()
		retireOwnershipCapability = prev
		retireOwnershipCapabilityMu.Unlock()
	}
}

// strongOwnershipAvailable reports whether the local daemon can uphold the
// strong rendezvous ownership contract retirement requires
// (cmd/evener/serve.go refuses retirement outright where it cannot).
func strongOwnershipAvailable() bool {
	retireOwnershipCapabilityMu.Lock()
	capability := retireOwnershipCapability
	retireOwnershipCapabilityMu.Unlock()
	if capability != nil {
		return capability()
	}
	return rendezvous.StrongOwnershipAvailable()
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
// states stay explicit; only a fresh compatible lifecycle on a platform that
// can uphold strong rendezvous ownership enables Retire.
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
	// Safe retire runs through the same configured ownership path as force
	// stop (retireDaemon refuses outright when RunDir is empty or ResumeLocks
	// is nil), and additionally needs the daemon's kernel-serialized ownership
	// contract: where it is unavailable the daemon refuses retirement outright
	// (cmd/evener/serve.go). The inventory must not offer an action the server
	// always rejects, so derive retire from exactly those prerequisites rather
	// than repeating them.
	canRetire := canForceStop && strongOwnershipAvailable()
	response := appwire.DaemonListResponse{
		DefaultTimeoutMillis: appwire.DurationMillis(cfg.DaemonIdleTimeout),
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
		if sessionID != "" && archiveDecisions[hubcore.ArchiveKey{Kind: "session", ID: sessionID}] {
			row.Archived = true
		}
		// Archived rows stay visible with retirement disabled: archive is a
		// separate axis from probe freshness, and retiring an archived session's
		// daemon is not offered.
		row.CanRetire = canRetire && row.Compatibility == "compatible" && row.ProbeState == "current" &&
			row.Lifecycle != nil && row.Lifecycle.Phase == "resident" && !row.Archived
		if cfg.Past != nil && sessionID != "" {
			if pastEntry, ok := cfg.Past.Find(sessionID); ok {
				row.Name = pastEntry.Meta.Name
			}
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
	entry, err := forceStopEntry(cfg.RunDir, ref.ThreadID, cfg.DaemonProcesses, nil, recoveryTarget, &params.Identity)
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
	// The retire request carries a context, so acquire each alias through it and
	// release the prefix already held when a later alias blocks past
	// cancellation instead of hanging and retaining the earlier aliases.
	acquired := 0
	defer func() {
		for _, alias := range slices.Backward(aliases[:acquired]) {
			cfg.ResumeLocks.For(alias).Unlock()
		}
	}()
	for _, id := range aliases {
		if err := cfg.ResumeLocks.For(id).LockContext(ctx); err != nil {
			return appwire.DaemonRetireResponse{}, err
		}
		acquired++
	}
	if err := ctx.Err(); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	currentTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	if currentTarget != recoveryTarget {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable("session recovery authority changed; retry daemon retire")
	}
	current, err := forceStopRereadEntry(cfg.RunDir, ref.ThreadID, entry, cfg.DaemonProcesses, currentTarget, &params.Identity)
	if err != nil {
		return appwire.DaemonRetireResponse{}, appwire.Unavailable(err.Error())
	}
	if err := expectedDaemonConflict(current, &params.Identity); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	if err := archivedRetireRefusal(cfg, current); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	if err := deletionFenceError(cfg, params.Identity.Ref, ref.ThreadID, ""); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	// A deletion record may name any alias in the ownership group, not only
	// the ref the request addressed (the e68ec81fa class): every alias's
	// reservation is already held here, so the whole group is fenced before
	// the retire RPC is forwarded.
	if err := deletionFenceErrorForGroup(cfg, aliases); err != nil {
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

// archivedRetireRefusal refuses a safe retire of a daemon whose session is
// archived. Archived rows stay visible with retirement disabled, and the
// decision is revalidated at action time so a row rendered before the archive
// (or an unarchived row for an archived session) cannot still retire. Both the
// resolved session id and the alias thread id are checked, since the archive
// decision is keyed by the stored session id.
func archivedRetireRefusal(cfg hubcore.WebConfig, entry rendezvous.Entry) error {
	if cfg.Archive == nil {
		return nil
	}
	decisions, err := cfg.Archive.Decisions()
	if err != nil {
		return appwire.Unavailable(fmt.Sprintf("read archive decisions: %v", err))
	}
	for _, id := range []string{entry.SessionID, entry.ThreadID} {
		if id == "" {
			continue
		}
		if decisions[hubcore.ArchiveKey{Kind: "session", ID: id}] {
			return appwire.Conflict("archived daemons cannot be safely retired")
		}
	}
	return nil
}
