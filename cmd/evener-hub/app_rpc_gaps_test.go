package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

// TestBlockedUnknownMutationError covers the error construction function.
func TestBlockedUnknownMutationError(t *testing.T) {
	inner := errors.New("persistence unavailable")
	err := blockedUnknownMutationError("mutation-1", inner)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("expected WireError, got %T: %v", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("expected CodeInternalError, got %d", wire.Code)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatal("expected ErrorData")
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("expected ErrorMutationOutcomeUnknown, got %q", data.EvenerErrorInfo)
	}
	if data.ClientMutationID != "mutation-1" {
		t.Fatalf("expected ClientMutationID 'mutation-1', got %q", data.ClientMutationID)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("expected MutationOutcomeUnknown, got %v", data.MutationOutcome)
	}
	if data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("expected RetryDispositionBlocked, got %v", data.RetryDisposition)
	}
	if data.Cause != "persistenceUnavailable" {
		t.Fatalf("expected Cause 'persistenceUnavailable', got %q", data.Cause)
	}
}

// TestHubLaunchConfigRootSet covers the path where cfg.LaunchConfigRoot is set.
func TestHubLaunchConfigRootSet(t *testing.T) {
	cfg := hubcore.WebConfig{LaunchConfigRoot: "/custom/config"}
	got := hubLaunchConfigRoot(cfg)
	if got != "/custom/config" {
		t.Fatalf("expected '/custom/config', got %q", got)
	}
}

// TestHubLaunchConfigRootUnset covers the fallback path.
func TestHubLaunchConfigRootUnset(t *testing.T) {
	cfg := hubcore.WebConfig{}
	got := hubLaunchConfigRoot(cfg)
	if got != cmdutil.DefaultConfigRoot() {
		t.Fatalf("expected default config root %q, got %q", cmdutil.DefaultConfigRoot(), got)
	}
}

// TestHubLaunchConfigRootRejectsRelative pins that a relative root never
// reaches launchconfig. cmdutil.DefaultConfigRoot substitutes the relative
// ./.config/evener when neither XDG_CONFIG_HOME nor a home directory resolves,
// and launchconfig joins that root with launch.toml, so a relative root would
// resolve against the process working directory: a repository-controlled
// launch config. The root must be absolute or absent, never relative.
func TestHubLaunchConfigRootRejectsRelative(t *testing.T) {
	got := hubLaunchConfigRoot(hubcore.WebConfig{LaunchConfigRoot: "./.config/evener"})
	if got != "" {
		t.Fatalf("relative LaunchConfigRoot resolved to %q, want \"\"", got)
	}
}

// TestAllowsPastFallbackAfterLiveReadFailureNonSubscribe covers the
// non-subscribe path (returns true).
func TestAllowsPastFallbackAfterLiveReadFailureNonSubscribe(t *testing.T) {
	// This tests the function logic; we can't create a real Source in the
	// main package test, but we can test the logic with nil source.
	// For non-subscribe, it returns true regardless of source.
	got := allowsPastFallbackAfterLiveReadFailure(nil, appwire.ThreadReadParams{Subscribe: false}, nil)
	if !got {
		t.Fatal("non-subscribe should allow past fallback")
	}
}

// TestAllowsPastFallbackAfterLiveReadFailureSubscribeNilSource covers the
// subscribe path with nil source (not a RelaySessionSource, so returns true).
func TestAllowsPastFallbackAfterLiveReadFailureSubscribeNilSource(t *testing.T) {
	got := allowsPastFallbackAfterLiveReadFailure(nil, appwire.ThreadReadParams{Subscribe: true}, nil)
	// nil source doesn't implement RelaySessionSource, so requiresLiveHandoff=false
	// → returns true
	if !got {
		t.Fatal("subscribe with non-relay source should allow past fallback")
	}
}

// requireSavedHistoryFailureStamp pins the identity and the typed diagnostic
// of a saved-history read's failure. Both are machine values: the boot
// generation a client compares, and the error info it acts on. No message copy
// is read (Copy Is Not a Test Oracle).
func requireSavedHistoryFailureStamp(t *testing.T, err error, wantGeneration string) {
	t.Helper()
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("saved-history failure = %T %v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("saved-history failure data = %#v, want HistoryReadErrorData", wire.Data)
	}
	if data.BootGeneration != wantGeneration {
		t.Fatalf("saved-history failure boot generation = %q, want %q", data.BootGeneration, wantGeneration)
	}
	if data.Epoch == nil || *data.Epoch != 0 {
		t.Fatalf("saved-history failure epoch = %v, want 0", data.Epoch)
	}
	// The diagnostic survives the stamp: the failure still names what went
	// wrong with the transcript, not merely who did not serve it.
	if data.EvenerErrorInfo != appwire.ErrorTranscriptHistoryFailed {
		t.Fatalf("saved-history failure info = %q, want %q", data.EvenerErrorInfo, appwire.ErrorTranscriptHistoryFailed)
	}
}

// dispatchThreadReadOver dispatches one thread/read through the hub's real
// router over the given sources.
func dispatchThreadReadOver(t *testing.T, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadReadParams) error {
	t.Helper()
	server := newHubAppServer(cfg, sources)
	_, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead, Params: mustPagingJSON(t, params),
	})
	return err
}

// TestPastFallbackRouteMissWithIncompatibleOwnerStampsOwnerUnknown pins the case
// the review found half-done. A local "thread not found" is a route miss: the
// local source's lookup admits only daemons at this protocol whose status is not
// restartRequired, so it skips an owner the roster has confirmed as running but
// incompatible. That is not absence, and the stamp must say so.
func TestPastFallbackRouteMissWithIncompatibleOwnerStampsOwnerUnknown(t *testing.T) {
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{PID: 4242, Protocol: "evener-appwire-v4", ThreadID: sessionID, SessionID: sessionID},
		SessionID: sessionID,
		Status:    appwire.ThreadStatusRestartRequired,
	})
	if roster.DaemonOwnershipAbsent() {
		t.Fatal("fixture does not hold a confirmed running owner")
	}
	stamp := savedHistoryStampFor(t.Context(), hubcore.WebConfig{Roster: roster}, appwire.ThreadReadParams{Ref: "local:" + sessionID}, appwire.SessionUnavailable(threadNotFoundMessagePrefix+sessionID))
	err := stamp(appwire.TranscriptHistoryFailed(0))
	requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
}

// TestPastFallbackIncompleteDiscoveryStampsOwnerUnknown pins the second half of
// that finding: when the roster's scan is incomplete, a route miss proves
// nothing about ownership either, so the failure must not claim absence.
func TestPastFallbackIncompleteDiscoveryStampsOwnerUnknown(t *testing.T) {
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"
	runDir := t.TempDir()
	// An unparseable claim makes the roster's own scan incomplete.
	if err := os.WriteFile(filepath.Join(runDir, "424242.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(runDir, nil).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	if roster.OwnershipError() == nil {
		t.Fatal("fixture does not hold an incomplete scan")
	}
	stamp := savedHistoryStampFor(t.Context(), hubcore.WebConfig{Roster: roster}, appwire.ThreadReadParams{Ref: "local:" + sessionID}, appwire.SessionUnavailable(threadNotFoundMessagePrefix+sessionID))
	err := stamp(appwire.TranscriptHistoryFailed(0))
	requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
}

// TestPastFallbackRosterAbsenceStampsDaemonless is the third case: the roster
// answered and nobody owns the thread, so the same route miss is real absence
// and keeps the daemonless stamp.
func TestPastFallbackRosterAbsenceStampsDaemonless(t *testing.T) {
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"
	roster := hubcore.NewRoster("", nil)
	if !roster.DaemonOwnershipAbsent() {
		t.Fatal("fixture does not hold established absence")
	}
	stamp := savedHistoryStampFor(t.Context(), hubcore.WebConfig{Roster: roster}, appwire.ThreadReadParams{Ref: "local:" + sessionID}, appwire.SessionUnavailable(threadNotFoundMessagePrefix+sessionID))
	err := stamp(appwire.TranscriptHistoryFailed(0))
	requireSavedHistoryFailureStamp(t, err, appwire.DaemonlessBootGeneration)
}

// TestPastFallbackReadsOneRosterPublication pins the race a decision spanning
// several roster reads has. Each subtest gives the check an older publication
// through the seam and a newer one through the roster itself: a refresh landed
// in between. The decision must come from the older publication alone, so
// where it holds an owner the answer is unknown, never absence.
//
// No sleep is involved; the interleaving is the fixture.
func TestPastFallbackReadsOneRosterPublication(t *testing.T) {
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"

	t.Run("claims and live entries", func(t *testing.T) {
		// The older publication holds a confirmed owner that only the per-PID
		// projection carries. Two daemons report the same session id — which is
		// NOT the thread id — and the newer one wins the per-session preference,
		// so Live holds the decoy and Confirmed holds both. Only the entry Live
		// drops names the thread, through its workspace ref.
		//
		// That makes the mutation meaningful: a direct scan of Live, and the
		// ancestry walk over the same Live, both miss the owner and read the
		// publication as absence. A fixture whose owner sat in both fields would
		// pass either way, because the ancestry walk would recover what the
		// direct scan missed.
		const sharedSessionID = "02wMz5Txv9sharedSessionX"
		older := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
		beforeRefresh := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{
				Entry: rendezvous.Entry{
					PID: 4242, Protocol: "evener-appwire-v4", ThreadID: "other-thread", SessionID: sharedSessionID,
					WorkspaceRef: localAppRef(sessionID), StartedAt: older,
				},
				SessionID: sharedSessionID,
				Status:    appwire.ThreadStatusActive,
			},
			hubcore.LiveEntry{
				Entry: rendezvous.Entry{
					PID: 4343, Protocol: "evener-appwire-v4", ThreadID: "third-thread", SessionID: sharedSessionID,
					StartedAt: older.Add(time.Hour),
				},
				SessionID: sharedSessionID,
				Status:    appwire.ThreadStatusActive,
			},
		).OwnershipSnapshot()
		// The fixture only means something while Live hides the owner.
		if live := beforeRefresh.Live; len(live) != 1 || live[0].PID != 4343 {
			t.Fatalf("fixture does not dedupe the owner out of Live: %+v", live)
		}
		if len(beforeRefresh.Confirmed) != 2 {
			t.Fatalf("fixture does not carry both processes in Confirmed: %+v", beforeRefresh.Confirmed)
		}
		// The newer one replaced that owner with an unverified claim.
		afterRefresh := hubcore.OwnershipSnapshot{
			Unconfirmed: []rendezvous.Entry{{PID: 4242, ThreadID: sessionID}},
		}
		original := rosterOwnershipSnapshot
		t.Cleanup(func() { rosterOwnershipSnapshot = original })
		publications := []hubcore.OwnershipSnapshot{beforeRefresh, afterRefresh}
		reads := 0
		rosterOwnershipSnapshot = func(*hubcore.Roster) hubcore.OwnershipSnapshot {
			if reads >= len(publications) {
				return publications[len(publications)-1]
			}
			reads++
			return publications[reads-1]
		}
		stamp := savedHistoryStampFor(t.Context(), hubcore.WebConfig{Roster: hubcore.NewRoster("", nil)}, appwire.ThreadReadParams{Ref: "local:" + sessionID}, appwire.SessionUnavailable(threadNotFoundMessagePrefix+sessionID))
		err := stamp(appwire.TranscriptHistoryFailed(0))
		requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
		if reads != 1 {
			t.Fatalf("absence check read %d roster publications, want exactly 1", reads)
		}
	})

	t.Run("ancestry walk", func(t *testing.T) {
		// This one reaches the ancestry lookup, where the check used to leave
		// the publication it had captured. The older publication holds the
		// delegate's parent: a live, incompatible daemon that reports no child
		// ids, so only ancestry can connect the two. By the time a walk over
		// the live roster got to the parent, a refresh had removed it, and an
		// empty newer publication read as absence although the older one —
		// the publication the check started from — held a running owner.
		const (
			parentID = "02wMz5Txv1C3Hut0M8GCeB"
			childID  = "02wMz5Txv2enqVTitaig6F"
		)
		root := t.TempDir()
		stateDir := filepath.Join(root, "projects", "project-delegate-0000000000")
		now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		for _, meta := range []schema.SessionMeta{
			{ID: parentID, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
			{ID: childID, CreatedAt: now, UpdatedAt: now, ParentSessionID: parentID, IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		} {
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
		}
		// The older publication: the parent runs, incompatible, and reports no
		// child ids — so nothing in it names the child directly. Built by the
		// roster so its owner appears in the same fields a real one produces.
		beforeRefresh := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
			Entry:     rendezvous.Entry{PID: 4242, Protocol: "evener-appwire-v4", ThreadID: parentID, SessionID: parentID},
			SessionID: parentID,
			Status:    appwire.ThreadStatusRestartRequired,
		}).OwnershipSnapshot()
		original := rosterOwnershipSnapshot
		t.Cleanup(func() { rosterOwnershipSnapshot = original })
		rosterOwnershipSnapshot = func(*hubcore.Roster) hubcore.OwnershipSnapshot { return beforeRefresh }
		// The newer publication no longer has the parent at all: the roster the
		// walk would otherwise read is empty.
		stamp := savedHistoryStampFor(
			t.Context(),
			hubcore.WebConfig{Roster: hubcore.NewRoster("", nil), StateDir: stateDir},
			appwire.ThreadReadParams{Ref: "local:" + childID},
			appwire.SessionUnavailable(threadNotFoundMessagePrefix+childID),
		)
		err := stamp(appwire.TranscriptHistoryFailed(0))
		requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
	})
}

// TestPastFallbackAfterLiveFailureStampsOwnerUnknown pins the read issue #6815
// names. A browser refresh of a thread it already holds sends subscribe:false,
// so the hub tries saved history after a live read fails even though the daemon
// owning the thread may still be running. When that saved read fails too, the
// failure must say its owner is unknown: the live read failed for a reason that
// establishes nothing about who serves the thread.
func TestPastFallbackAfterLiveFailureStampsOwnerUnknown(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	// The saved read fails on its own: the transcript cannot be decoded. That
	// keeps the failure the stamp rides on, and the typed diagnostic with it.
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()
	// The ref's source is registered and bound to the thread, so a daemon may
	// be serving it. Its read fails for a reason that names no absence.
	sources.Add(&pastFallbackRelaySource{readErr: appwire.Unavailable("relay route publication failed")})

	err := dispatchThreadReadOver(t, cfg, sources, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
	})
	requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
}

// TestPastFallbackDelegateOfLiveDaemonStampsOwnerUnknown pins the delegate
// hole in the absence check. The delegate's own id never matches its parent's
// ids or workspace ref, so only persisted ancestry says a live daemon owns it.
// Both parent states must be covered: an incompatible parent is skipped by the
// local route lookup and found through ancestry, and a compatible parent is
// found the same way. Either way a running daemon serves the delegate, so a
// failed saved read there must not claim absence.
func TestPastFallbackDelegateOfLiveDaemonStampsOwnerUnknown(t *testing.T) {
	const (
		parentID = "02wMz5Txv1C3Hut0M8GCeB"
		childID  = "02wMz5Txv2enqVTitaig6F"
	)
	for _, tc := range []struct {
		name   string
		status string
	}{
		{name: "incompatible parent", status: appwire.ThreadStatusRestartRequired},
		{name: "compatible parent", status: "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-delegate-0000000000")
			now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			for _, meta := range []schema.SessionMeta{
				{ID: parentID, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
				{ID: childID, CreatedAt: now, UpdatedAt: now, ParentSessionID: parentID, IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
			} {
				if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
					t.Fatal(err)
				}
			}
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
				Entry:     rendezvous.Entry{PID: 1, Protocol: "evener-appwire-v4", SessionID: parentID, ThreadID: parentID},
				SessionID: parentID,
				Status:    tc.status,
			})
			if roster.DaemonOwnershipAbsent() {
				t.Fatal("fixture does not hold a live owner")
			}
			stamp := savedHistoryStampFor(
				t.Context(),
				hubcore.WebConfig{Roster: roster, StateDir: stateDir},
				appwire.ThreadReadParams{Ref: "local:" + childID},
				appwire.SessionUnavailable(threadNotFoundMessagePrefix+childID),
			)
			err := stamp(appwire.TranscriptHistoryFailed(0))
			requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
		})
	}
}

// TestPastFallbackWithoutServingSourceStampsDaemonless is the other direction,
// over the same failing saved read: when the ref names a source this hub does
// not serve, no daemon here can be serving the thread, so the failure keeps
// claiming the real absence. Fixing the unknown-owner direction must not
// weaken this one.
func TestPastFallbackWithoutServingSourceStampsDaemonless(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nothing serves the ref. A hub with no local daemon is exactly this shape
	// in production (newHubSourceRegistry registers no local source without a
	// roster), and its saved reads already answer as daemonless when they
	// succeed.
	err := dispatchThreadReadOver(t, cfg, appsource.NewRegistry(), appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
	})
	requireSavedHistoryFailureStamp(t, err, appwire.DaemonlessBootGeneration)
}

// TestSuccessfulSavedFallbackAfterRelayOutageStaysDaemonless is the case that
// keeps the fix honest. A relay outage leaves ownership unresolved, but the
// saved read SUCCEEDS: the fallback must still be taken, and the response must
// still carry daemonless, because that is what a client already treats as a
// served-from-transcript answer. A fix that stamped unknown-owner everywhere
// would turn an honesty change into a reliability regression.
func TestSuccessfulSavedFallbackAfterRelayOutageStaysDaemonless(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	sources := appsource.NewRegistry()
	// The source is registered and bound, and its live read fails with an
	// outage that names no absence.
	sources.Add(&pastFallbackRelaySource{readErr: appwire.RetryableSessionUnavailable("relay connection ended before route publication")})

	server := newHubAppServer(cfg, sources)
	value, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
		Params: mustPagingJSON(t, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40}),
	})
	if err != nil {
		t.Fatalf("saved fallback after a relay outage was refused: %v", err)
	}
	response, ok := value.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read response = %T", value)
	}
	if len(response.Thread.Turns) == 0 {
		t.Fatal("saved fallback answered no turns")
	}
	if response.BootGeneration != appwire.DaemonlessBootGeneration {
		t.Fatalf("successful saved fallback boot generation = %q, want %q", response.BootGeneration, appwire.DaemonlessBootGeneration)
	}
}

// TestThreadReadServesSavedHistoryDuringIncompleteDiscovery pins the other half
// of the discovery-error contract. A malformed rendezvous record makes the
// roster's scan incomplete, so the hub cannot say who owns the session. When the
// roster also cannot be refreshed, that uncertainty must not be reported as an
// unavailable thread: the saved transcript is readable, and read-only history
// stays available precisely because the answer is "unknown", not "gone".
//
// The failure this guards is subtle. The gate before the read asks the roster
// for the discovery error and lets the read continue only while that error is
// still there; the ancestry walk asks again at its end. A live view that
// answered either question from a zero-valued snapshot reported no error at all,
// so the read refused instead of falling back — the opposite of what the typed
// error exists for.
func TestThreadReadServesSavedHistoryDuringIncompleteDiscovery(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	runDir := t.TempDir()
	// An unparseable claim makes the roster's own scan incomplete.
	if err := os.WriteFile(filepath.Join(runDir, "424242.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(runDir, nil).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	if roster.OwnershipError() == nil {
		t.Fatal("fixture does not hold an incomplete scan")
	}
	// A refresh that fails on its own is what makes the read consult the
	// discovery error instead of simply retrying.
	previousRefresh := hubRosterRefresh
	t.Cleanup(func() { hubRosterRefresh = previousRefresh })
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
		return errors.New("refresh blocked by an unrelated marker")
	}
	cfg.RunDir = runDir
	cfg.Roster = roster
	// The local source answers the ref and its live read fails for a reason
	// that names no absence, so the read must fall back to saved history.
	sources := appsource.NewRegistry()
	sources.Add(&pastFallbackRelaySource{readErr: appwire.SessionUnavailable("daemon route vanished")})
	server := newHubAppServer(cfg, sources)
	value, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
		Params: mustPagingJSON(t, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40}),
	})
	if err != nil {
		t.Fatalf("read with incomplete discovery refused readable saved history: %v", err)
	}
	response, ok := value.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read response = %T", value)
	}
	if len(response.Thread.Turns) == 0 {
		t.Fatal("read during incomplete discovery answered no turns")
	}
	// This response is a *successful* saved fallback, and such a read carries
	// daemonless even when ownership was never resolved — the guide says a
	// client must not read that token on a success as proof the daemon is gone.
	// The stamp a *failed* saved read earns is pinned by the fallback tests.
}

// TestSessionOperationsStampsFailedWindowContinuation pins the second low. A
// turns-list request carrying both a turn window and a cursor takes the Manager
// continuation path, which the plain listing's stamp does not cover. When local
// routing misses and the saved index then rejects the cursor, the continuation
// used to return the typed diagnostic with no boot generation at all, so a
// caller could not tell established absence from unknown ownership.
//
// The stamp is the same one every other saved fallback uses: the routing error
// is the evidence, and the history diagnostic survives it.
func TestSessionOperationsStampsFailedWindowContinuation(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	// The local source exists, so routing resolves a source, and its live
	// continuation fails as a dead session — the shape that sends the request
	// to the saved index.
	sources := appsource.NewRegistry()
	sources.Add(&pastFallbackRelaySource{readErr: appwire.SessionUnavailable(threadNotFoundMessagePrefix + entry.Meta.ID)})
	server := newHubAppServer(cfg, sources)
	operations := &sessionOperations{cfg: cfg, sources: sources, relays: newHubRelay(server, cfg, sources), server: server}
	_, err := operations.turns(t.Context(), appwire.ThreadTurnsListParams{
		Ref:        "local:" + entry.Meta.ID,
		Cursor:     "a-cursor-the-saved-index-does-not-own",
		TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_m1"},
	})
	if err == nil {
		t.Fatal("a continuation whose cursor the saved index rejects answered a page")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("failed continuation = %T %v, want a typed wire error", err, err)
	}
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("failed continuation data = %#v, want HistoryReadErrorData", wire.Data)
	}
	// The diagnostic survives the stamp: this failure is the saved index
	// rejecting the cursor, and it still says so.
	if data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
		t.Fatalf("failed continuation info = %q, want %q", data.EvenerErrorInfo, appwire.ErrorTranscriptItemCursorStale)
	}
	// Absence was never established here: routing missed and the saved index
	// could not answer, so the failure must not claim a daemon is gone.
	if data.BootGeneration != appwire.UnknownOwnerBootGeneration {
		t.Fatalf("failed continuation boot generation = %q, want %q", data.BootGeneration, appwire.UnknownOwnerBootGeneration)
	}
}

// TestSuccessfulSavedFallbackTakesNoRosterSnapshot pins the cost of the stamp
// decision. savedHistoryStampFor asks the roster whether absence is established,
// which takes a snapshot and can walk persisted ancestry; only a FAILED saved
// read is stamped, so a successful fallback must not pay for either. The
// fallback must still be taken and must still carry daemonless.
func TestSuccessfulSavedFallbackTakesNoRosterSnapshot(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	sources := appsource.NewRegistry()
	// A relay outage with no roster configured: the saved read succeeds, so
	// nothing about ownership is ever asked.
	sources.Add(&pastFallbackRelaySource{readErr: appwire.RetryableSessionUnavailable("relay connection ended before route publication")})
	originalOwnership := rosterOwnershipSnapshot
	t.Cleanup(func() { rosterOwnershipSnapshot = originalOwnership })
	snapshots := 0
	// This is the seam the saved-history stamp reaches, so counting it observes
	// the call a successful fallback must not make.
	rosterOwnershipSnapshot = func(r *hubcore.Roster) hubcore.OwnershipSnapshot {
		snapshots++
		return originalOwnership(r)
	}
	cfg.Roster = hubcore.NewRoster("", nil)
	server := newHubAppServer(cfg, sources)
	value, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
		Params: mustPagingJSON(t, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40}),
	})
	if err != nil {
		t.Fatalf("saved fallback after a relay outage was refused: %v", err)
	}
	response, ok := value.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read response = %T", value)
	}
	if len(response.Thread.Turns) == 0 {
		t.Fatal("saved fallback answered no turns")
	}
	if response.BootGeneration != appwire.DaemonlessBootGeneration {
		t.Fatalf("successful saved fallback boot generation = %q, want %q", response.BootGeneration, appwire.DaemonlessBootGeneration)
	}
	if snapshots != 0 {
		t.Fatalf("successful saved fallback took %d roster snapshots, want 0", snapshots)
	}
}

// TestPastFallbackPaddedThreadIDWithOwnerStampsOwnerUnknown pins the identity
// the absence check asks about. The saved lookup resolves the target through
// localPastThreadID, which trims a padded id; the roster check used the raw
// value, matched no owner for the session the lookup had just found, and called
// it absent. A confirmed owner plus a padded id must not produce daemonless.
//
// The same normalization must not invent absence either: a genuinely absent
// owner keeps the daemonless stamp.
func TestPastFallbackPaddedThreadIDWithOwnerStampsOwnerUnknown(t *testing.T) {
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"
	for _, tc := range []struct {
		name    string
		roster  *hubcore.Roster
		want    string
		wantWhy string
	}{
		{
			name: "confirmed owner",
			roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{
				Entry:     rendezvous.Entry{PID: 4242, Protocol: "evener-appwire-v4", ThreadID: sessionID, SessionID: sessionID},
				SessionID: sessionID,
				Status:    appwire.ThreadStatusRestartRequired,
			}),
			want:    appwire.UnknownOwnerBootGeneration,
			wantWhy: "a confirmed owner the route lookup skips is not absence",
		},
		{
			name:    "no owner",
			roster:  hubcore.NewRoster("", nil),
			want:    appwire.DaemonlessBootGeneration,
			wantWhy: "an answered roster with no owner is established absence",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == appwire.UnknownOwnerBootGeneration && tc.roster.DaemonOwnershipAbsent() {
				t.Fatal("fixture does not hold a confirmed running owner")
			}
			if tc.want == appwire.DaemonlessBootGeneration && !tc.roster.DaemonOwnershipAbsent() {
				t.Fatal("fixture does not hold established absence")
			}
			// The padded id is the whole point: it is the string the saved
			// lookup trims and the roster check must trim with it.
			params := appwire.ThreadReadParams{ThreadID: "  " + sessionID + "\t"}
			stamp := savedHistoryStampFor(t.Context(), hubcore.WebConfig{Roster: tc.roster}, params, appwire.SessionUnavailable(threadNotFoundMessagePrefix+sessionID))
			err := stamp(appwire.TranscriptHistoryFailed(0))
			requireSavedHistoryFailureStamp(t, err, tc.want)
		})
	}
}

// TestPastFallbackOverlappingDaemonsStampsOwnerUnknown pins a hole the per-session
// listing leaves in the absence check. Two confirmed daemons can report the same
// session id; the listing keeps one per session, preferring the newer process.
// When the entry it keeps names no route to the requested thread and the entry it
// drops does, a scan of that listing finds no owner and calls the thread absent,
// although a live confirmed process holds the workspace ref.
//
// The two entries deliberately share a session id that is NOT the thread id. Were
// it the thread id, the surviving entry would match on its own session field and
// the test would pass without ever consulting the dropped owner.
func TestPastFallbackOverlappingDaemonsStampsOwnerUnknown(t *testing.T) {
	const (
		sessionID       = "02wMz5Txv5aIxgf9yVdd0N"
		sharedSessionID = "02wMz5Txv9sharedSessionX"
	)
	older := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	// The older process carries the workspace route to this thread.
	owner := hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID: 4242, Protocol: "evener-appwire-v4", ThreadID: "other-thread", SessionID: sharedSessionID,
			WorkspaceRef: localAppRef(sessionID), StartedAt: older,
		},
		SessionID: sharedSessionID,
		Status:    appwire.ThreadStatusActive,
	}
	// A newer process claims the same session id without that route. It wins the
	// per-session preference, so the listing drops the owner above.
	decoy := hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID: 4343, Protocol: "evener-appwire-v4", ThreadID: "third-thread", SessionID: sharedSessionID,
			StartedAt: newer,
		},
		SessionID: sharedSessionID,
		Status:    appwire.ThreadStatusActive,
	}
	roster := hubcore.NewRosterWithEntries(owner, decoy)
	// The fixture only means something while the listing hides the owner.
	if live := roster.List(); len(live) != 1 || live[0].PID != decoy.PID {
		t.Fatalf("fixture does not dedupe the owner away: %+v", live)
	}
	if roster.DaemonOwnershipAbsent() {
		t.Fatal("fixture does not hold a live owner")
	}
	stamp := savedHistoryStampFor(t.Context(), hubcore.WebConfig{Roster: roster}, appwire.ThreadReadParams{Ref: "local:" + sessionID}, appwire.SessionUnavailable(threadNotFoundMessagePrefix+sessionID))
	err := stamp(appwire.TranscriptHistoryFailed(0))
	requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
}
