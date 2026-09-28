//go:build linux || darwin

package hostfence

// Crash-fencing spec 08c §3's local reap and §7's boot reaping. §7 runs it
// first: "Boot runs the operation-store load plus the safety-critical local reap
// of its local orphan boundary first (the reap in §3), then hub.toml load ...
// then the interrupted transition". The call site is main.go's named seam
// (reapLocalOrphanBoundary); everything here is that seam's implementation.
//
// The reap reaps nothing on membership alone. For every open `pending-spawn`
// intent it reopens the persisted boundary, enumerates members, and applies
// §3's clean rule against the launcher-observed (pid, start token) pair bound
// to the nonce: empty is clean, a start-token mismatch is a reused id and reads
// clean, and a member whose pid matches no persisted pair is live — never
// clean. Verified members are signaled and the boundary is proven dead under a
// bounded wait; anything the pass cannot verify fails closed: the record is
// marked `orphan-unverified` with its boundary, the intent stays open, and the
// host's admission stays fenced until a later boot or `orphan-resolve` clears
// it.
//
// The disposition is durable: every step is its own atomic store write, so a
// crash mid-reap leaves either the unverified mark or an open intent that the
// next boot retries. The pass never returns a startup refusal — it reports what
// it could not converge, and the caller logs it and serves.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// DefaultReapWait bounds the dead proof after the pass signals verified
// members: the boundary must re-enumerate clean within it, or the pass fails
// closed. The spec leaves the number to the implementing PR (§3's "bounded
// wait").
const DefaultReapWait = 5 * time.Second

// LocalBoundaryHandle is the boundary surface the reap drives. *execenv.Boundary
// satisfies it; tests substitute a fake so every decision path is exercisable
// without a kernel boundary.
type LocalBoundaryHandle interface {
	// Members enumerates current members with kernel start tokens.
	Members() ([]execenv.BoundaryMember, error)
	// SignalVerified signals one member only while its token still matches.
	SignalVerified(pid int, startToken string) error
	// Await is the bounded dead proof.
	Await(wait time.Duration, clean func([]execenv.BoundaryMember) bool) error
	// Close tears the boundary down once it is proven dead.
	Close() error
}

// ReapOptions are the reap's seams.
type ReapOptions struct {
	// Wait bounds the dead proof. Zero means DefaultReapWait.
	Wait time.Duration
	// Open reopens a persisted boundary identity. Nil means
	// execenv.OpenBoundary. A test substitutes a fake.
	Open func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error)
}

// ReapLocalOrphanBoundary runs §3's local reap over every open `pending-spawn`
// intent in store and reports how many intents it converged (dropped because
// their boundary proved clean). Fail-closed dispositions are durable, not
// errors; the returned error names what the pass could not converge at all — a
// store write that failed, or an unverified orphan on a record that is already
// terminal and therefore cannot be marked (its intent stays open for the next
// boot).
func ReapLocalOrphanBoundary(store *hostops.Store, opts ReapOptions) (int, error) {
	if store == nil {
		return 0, nil
	}
	wait := opts.Wait
	if wait <= 0 {
		wait = DefaultReapWait
	}
	open := opts.Open
	if open == nil {
		open = func(id execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
			return execenv.OpenBoundary(id)
		}
	}

	dropped := 0
	var failures []error
	for _, record := range store.SpawnIntentRecords() {
		if record.State == hostops.StateOrphanUnverified && boundaryHasForeignVariant(record.OrphanBoundary) {
			// §4's fencing-quarantine boundary is remote and §5's
			// boundary-unavailable entry resolves only on operator attestation;
			// neither is this pass's to enumerate (boot performs no SSH), and
			// rewriting either boundary from local intent data would destroy the
			// only proof `orphan-resolve` has. The record and its intents stay
			// exactly as they are.
			continue
		}
		groups, err := groupSpawnIntents(record.PendingSpawns)
		if err != nil {
			failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
			continue
		}
		var cleanNonces []string
		var failing []hostops.SpawnIntent
		for _, group := range groups {
			clean := reapBoundaryGroup(group, open, wait)
			if clean {
				for _, intent := range group.intents {
					cleanNonces = append(cleanNonces, intent.Nonce)
				}
				continue
			}
			failing = append(failing, group.intents...)
		}
		if len(failing) == 0 {
			if record.State == hostops.StateOrphanUnverified {
				if err := resolveReapedRecord(store, record.ID, cleanNonces); err != nil {
					failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
					continue
				}
			} else if err := store.ClearSpawnIntents(record.ID, cleanNonces); err != nil {
				failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
				continue
			}
			dropped += len(cleanNonces)
			continue
		}
		// Fail closed: mark the record `orphan-unverified` with the boundary the
		// remaining intents describe and keep every intent open, so the next boot
		// retries the enumeration (§3).
		entries, err := boundaryEntries(failing)
		if err != nil {
			failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
			continue
		}
		if record.State.Terminal() {
			failures = append(failures, fmt.Errorf(
				"record %s is %s but its boundary still holds unverified members; the intent stays open", record.ID, record.State))
			continue
		}
		if record.State != hostops.StateOrphanUnverified || !bytes.Equal(record.OrphanBoundary, entries) {
			if _, err := store.Transition(record.ID, hostops.StateOrphanUnverified, func(r *hostops.Record) {
				r.OrphanBoundary = entries
			}); err != nil {
				failures = append(failures, fmt.Errorf("record %s: mark orphan-unverified: %w", record.ID, err))
				continue
			}
		}
		// The clean groups on the same record converge independently of the
		// failing one: dropping their intents here keeps the retry scoped to what
		// is still unverified.
		if err := store.ClearSpawnIntents(record.ID, cleanNonces); err != nil {
			failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
			continue
		}
		dropped += len(cleanNonces)
	}
	return dropped, errors.Join(failures...)
}

// resolveReapedRecord converges one record the reap proved clean: §3's
// `orphan-unverified`→`interrupted` transition with the persisted boundary
// cleared, in its own atomic write, and then the open intents dropped in a
// second one. The order is the crash-safe one: a crash between them leaves an
// interrupted record with an open intent, which the next boot's reap finds
// clean and drops; the reverse order would leave an `orphan-unverified` record
// with no intent and no enumerable boundary, wedged with no retry.
func resolveReapedRecord(store *hostops.Store, recordID string, nonces []string) error {
	if _, err := store.Transition(recordID, hostops.StateInterrupted, func(r *hostops.Record) {
		r.OrphanBoundary = nil
		r.Result = &hostops.Result{OK: false, Message: hostops.InterruptedNote}
	}); err != nil {
		return err
	}
	return store.ClearSpawnIntents(recordID, nonces)
}

// boundaryGroup is one pre-created boundary's intents: the intents that share an
// identity and whose members must therefore be matched together (§9's
// "matched member-by-member at verify time").
type boundaryGroup struct {
	identity execenv.BoundaryIdentity
	intents  []hostops.SpawnIntent
}

// groupSpawnIntents gathers one record's intents by boundary identity, in
// first-seen order so the disposition is deterministic.
func groupSpawnIntents(intents []hostops.SpawnIntent) ([]boundaryGroup, error) {
	var groups []boundaryGroup
	index := make(map[execenv.BoundaryIdentity]int, len(intents))
	for _, intent := range intents {
		identity, err := boundaryIdentity(intent)
		if err != nil {
			return nil, err
		}
		at, seen := index[identity]
		if !seen {
			at = len(groups)
			index[identity] = at
			groups = append(groups, boundaryGroup{identity: identity})
		}
		groups[at].intents = append(groups[at].intents, intent)
	}
	return groups, nil
}

// boundaryHasForeignVariant reports whether a persisted `orphan-unverified`
// boundary carries any member that is not one of the local reap's own variants
// (`remote-fencing`, `boundary-unavailable`, or anything a build this old
// cannot parse). Such a record is never the local reap's to rewrite.
func boundaryHasForeignVariant(raw json.RawMessage) bool {
	var members []struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &members); err != nil {
		// Unparseable is not this pass's evidence to act on: fail closed by
		// leaving the record alone.
		return true
	}
	for _, member := range members {
		switch member.Kind {
		case "local-linux", "local-darwin", "local-markerless":
		default:
			return true
		}
	}
	return false
}

// boundaryIdentity maps one intent's platform arm onto the boundary identity.
func boundaryIdentity(intent hostops.SpawnIntent) (execenv.BoundaryIdentity, error) {
	switch intent.Platform {
	case hostops.SpawnPlatformLinux:
		return execenv.BoundaryIdentity{Platform: execenv.BoundaryPlatformLinux, CgroupID: intent.CgroupID}, nil
	case hostops.SpawnPlatformDarwin:
		if intent.PGID == nil || intent.SessionID == nil {
			return execenv.BoundaryIdentity{}, fmt.Errorf("%w: intent %q carries no darwin pair", hostops.ErrInvalidSpawnIntent, intent.Nonce)
		}
		return execenv.BoundaryIdentity{Platform: execenv.BoundaryPlatformDarwin, PGID: *intent.PGID, SessionID: *intent.SessionID}, nil
	default:
		return execenv.BoundaryIdentity{}, fmt.Errorf("%w: intent %q carries platform %q", hostops.ErrInvalidSpawnIntent, intent.Nonce, intent.Platform)
	}
}

// reapBoundaryGroup applies §3's clean rule to one group and reports whether the
// boundary proved clean. A false result is the fail-closed disposition, never
// "unknown": the caller marks the record `orphan-unverified` and keeps the
// intent.
func reapBoundaryGroup(group boundaryGroup, open func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error), wait time.Duration) bool {
	handle, err := open(group.identity)
	if err != nil {
		// A vanished boundary is clean — the kernel only removes an empty cgroup
		// — and any other failure to reach the boundary is fail-closed, never an
		// empty boundary.
		return errors.Is(err, execenv.ErrBoundaryGone)
	}
	members, err := handle.Members()
	if err != nil {
		return false
	}
	pairs := persistedPairs(group.intents)
	verified, unrecognized := partitionMembers(members, pairs)
	if len(unrecognized) > 0 {
		// §3: a member whose pid matches no persisted pair — a forked descendant
		// the launcher never observed — reads as live. Membership alone never
		// authorizes a kill, so the pass reaps nothing here.
		return false
	}
	if len(verified) == 0 {
		// Empty, or every member's start token differs: §3's already-clean arms.
		_ = handle.Close()
		return true
	}
	for _, member := range verified {
		err := handle.SignalVerified(member.PID, member.StartToken)
		switch {
		case err == nil:
		case errors.Is(err, execenv.ErrBoundaryMemberGone):
			// The member exited between enumeration and signal: already clean.
		case errors.Is(err, execenv.ErrBoundaryIdentityChanged):
			// The pid was recycled between enumeration and signal; §3 reads a
			// start-token mismatch as already clean, and the signal was refused.
		default:
			return false
		}
	}
	if err := handle.Await(wait, func(current []execenv.BoundaryMember) bool {
		leftVerified, leftUnrecognized := partitionMembers(current, pairs)
		return len(leftVerified) == 0 && len(leftUnrecognized) == 0
	}); err != nil {
		return false
	}
	_ = handle.Close()
	return true
}

// persistedPair is one launcher-observed kernel instance marker.
type persistedPair struct {
	pid        int
	startToken string
}

// persistedPairs collects the group's marker pairs. A markerless intent
// contributes none, so every member reads as unrecognized and only a
// demonstrably empty boundary clears it (§3).
func persistedPairs(intents []hostops.SpawnIntent) []persistedPair {
	var pairs []persistedPair
	for _, intent := range intents {
		if !intent.ValidMarker() || intent.PID == nil {
			continue
		}
		pairs = append(pairs, persistedPair{pid: *intent.PID, startToken: intent.StartTime})
	}
	return pairs
}

// partitionMembers splits membership into the verified instances and the
// unrecognized live ones, dropping the reused-pid members that §3 reads as
// already clean.
func partitionMembers(members []execenv.BoundaryMember, pairs []persistedPair) (verified, unrecognized []execenv.BoundaryMember) {
	for _, member := range members {
		pidSeen, exact := false, false
		for _, pair := range pairs {
			if pair.pid != member.PID {
				continue
			}
			pidSeen = true
			if pair.startToken == member.StartToken {
				exact = true
				break
			}
		}
		switch {
		case exact:
			verified = append(verified, member)
		case pidSeen:
			// A reused pid naming a different process: already clean, never
			// signaled (§3).
		default:
			unrecognized = append(unrecognized, member)
		}
	}
	return verified, unrecognized
}

// boundaryEntries renders the failing intents as §9's per-member
// `BoundaryEntry[]`: a marked local-linux/local-darwin entry per intent that
// carries its launcher marker, and a single local-markerless entry per
// marker-less intent. The local variants are the ones the local reap persists;
// the union's remote and boundary-unavailable variants belong to the fencing
// slices that own them. Entries are sorted by nonce so a repeated boot composes
// byte-identical bytes and skips a rewrite.
func boundaryEntries(intents []hostops.SpawnIntent) (json.RawMessage, error) {
	ordered := slices.Clone(intents)
	slices.SortFunc(ordered, func(a, b hostops.SpawnIntent) int {
		if a.Nonce < b.Nonce {
			return -1
		}
		if a.Nonce > b.Nonce {
			return 1
		}
		return 0
	})
	members := make([]any, 0, len(ordered))
	for _, intent := range ordered {
		entry, err := boundaryEntry(intent)
		if err != nil {
			return nil, err
		}
		members = append(members, entry)
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return nil, fmt.Errorf("hostfence: render the orphan boundary: %w", err)
	}
	return raw, nil
}

// boundaryEntry renders one intent as its §9 entry.
func boundaryEntry(intent hostops.SpawnIntent) (any, error) {
	switch intent.Platform {
	case hostops.SpawnPlatformLinux:
		if intent.ValidMarker() {
			return struct {
				Kind      string `json:"kind"`
				CgroupID  string `json:"cgroupId"`
				Nonce     string `json:"nonce"`
				PID       int    `json:"pid"`
				StartTime string `json:"startTime"`
			}{Kind: "local-linux", CgroupID: intent.CgroupID, Nonce: intent.Nonce, PID: *intent.PID, StartTime: intent.StartTime}, nil
		}
		return struct {
			Kind     string `json:"kind"`
			Platform string `json:"platform"`
			CgroupID string `json:"cgroupId"`
			Nonce    string `json:"nonce"`
		}{Kind: "local-markerless", Platform: "linux", CgroupID: intent.CgroupID, Nonce: intent.Nonce}, nil
	case hostops.SpawnPlatformDarwin:
		if intent.PGID == nil || intent.SessionID == nil {
			return nil, fmt.Errorf("%w: intent %q carries no darwin pair", hostops.ErrInvalidSpawnIntent, intent.Nonce)
		}
		if intent.ValidMarker() {
			return struct {
				Kind      string `json:"kind"`
				PGID      int    `json:"pgid"`
				SessionID int    `json:"sessionId"`
				PID       int    `json:"pid"`
				StartTime string `json:"startTime"`
				Nonce     string `json:"nonce"`
			}{Kind: "local-darwin", PGID: *intent.PGID, SessionID: *intent.SessionID, PID: *intent.PID, StartTime: intent.StartTime, Nonce: intent.Nonce}, nil
		}
		return struct {
			Kind      string `json:"kind"`
			Platform  string `json:"platform"`
			PGID      *int   `json:"pgid"`
			SessionID *int   `json:"sessionId"`
			Nonce     string `json:"nonce"`
		}{Kind: "local-markerless", Platform: "darwin", PGID: intent.PGID, SessionID: intent.SessionID, Nonce: intent.Nonce}, nil
	default:
		return nil, fmt.Errorf("%w: intent %q carries platform %q", hostops.ErrInvalidSpawnIntent, intent.Nonce, intent.Platform)
	}
}
