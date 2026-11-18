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
		// The older publication holds a confirmed, incompatible owner.
		beforeRefresh := hubcore.RosterSnapshot{
			Live: []hubcore.LiveEntry{{
				Entry:     rendezvous.Entry{PID: 4242, Protocol: "evener-appwire-v4", ThreadID: sessionID, SessionID: sessionID},
				SessionID: sessionID,
				Status:    appwire.ThreadStatusRestartRequired,
			}},
		}
		// The newer one replaced that owner with an unverified claim.
		afterRefresh := hubcore.RosterSnapshot{
			Unconfirmed: []rendezvous.Entry{{PID: 4242, ThreadID: sessionID}},
		}
		original := rosterSnapshot
		t.Cleanup(func() { rosterSnapshot = original })
		publications := []hubcore.RosterSnapshot{beforeRefresh, afterRefresh}
		reads := 0
		rosterSnapshot = func(*hubcore.Roster) hubcore.RosterSnapshot {
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
		// child ids — so nothing in it names the child directly.
		beforeRefresh := hubcore.RosterSnapshot{
			Live: []hubcore.LiveEntry{{
				Entry:     rendezvous.Entry{PID: 4242, Protocol: "evener-appwire-v4", ThreadID: parentID, SessionID: parentID},
				SessionID: parentID,
				Status:    appwire.ThreadStatusRestartRequired,
			}},
		}
		original := rosterSnapshot
		t.Cleanup(func() { rosterSnapshot = original })
		rosterSnapshot = func(*hubcore.Roster) hubcore.RosterSnapshot { return beforeRefresh }
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
