//go:build linux || darwin

package hostfence

// Crash-fencing spec 08c §5's resolve-time boundary verification: the read-only
// clean-rule check `orphan-resolve` runs over a record's persisted boundary
// before its one atomic store write. It is the local reap's enumeration made
// read-only — no signal, no teardown — plus §9's per-entry remote lease check:
//
//   - a marked local boundary reads clean when empty, or when every member
//     carries a pid/start-time mismatch against the persisted pair;
//   - a markerless local boundary reads clean only when demonstrably empty;
//   - a `remote-fencing` boundary reads clean only when every persisted lease
//     entry is enumerated and exit-confirmed under its stored ownership
//     identity, regardless of the guard comparison;
//   - a `boundary-unavailable` entry is never clean here: it resolves only
//     through the operator attestation §5 requires (the handler's path).
//
// Every unavailable-enumeration arm fails closed — the record stays fenced —
// and resolve never kills: the operator confirms the listed members gone
// out-of-band, then calls `orphan-resolve`.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// VerifyOptions are the resolve-time enumeration seams. Nil fields take the
// production defaults for the local arms; a nil VerifyLeaseEntry means the
// remote lease enumeration is unavailable, which fails closed.
type VerifyOptions struct {
	// Open reopens a persisted local boundary identity. Nil means
	// execenv.OpenBoundary.
	Open func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error)
	// Observe reads one process's kernel start token. Nil means
	// execenv.ObserveProcess.
	Observe func(pid int) (string, error)
	// VerifyLeaseEntry confirms one persisted remote lease entry has no matching
	// live holder under its stored ownership identity. Nil means the remote
	// enumeration is unavailable.
	VerifyLeaseEntry func(entry LeaseRef) (gone bool, err error)
}

// VerifyOrphanBoundary applies §5's clean rule to one record's persisted
// boundary. It returns nil only when the boundary is proven clean per its
// variant; otherwise it returns one of the three typed errors above, and the
// record stays fenced.
func VerifyOrphanBoundary(record hostops.Record, opts VerifyOptions) error {
	members, err := decodeBoundaryMembers(record.OrphanBoundary)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOrphanBoundaryUnenumerable, err)
	}
	if len(members) == 0 {
		// §9: an empty array means no spawned subprocess survived the crash —
		// verified empty, so clean by definition.
		return nil
	}
	var remote []RemoteFencingBoundary
	unavailable := 0
	local := false
	for _, member := range members {
		var discriminator struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(member, &discriminator); err != nil {
			return fmt.Errorf("%w: a boundary member carries no kind discriminator: %w", ErrOrphanBoundaryUnenumerable, err)
		}
		switch discriminator.Kind {
		case "boundary-unavailable":
			unavailable++
		case RemoteFencingBoundaryKind:
			var entry RemoteFencingBoundary
			if err := json.Unmarshal(member, &entry); err != nil {
				return fmt.Errorf("%w: a remote-fencing entry does not decode: %w", ErrOrphanBoundaryUnenumerable, err)
			}
			if err := entry.Validate(); err != nil {
				return fmt.Errorf("%w: a remote-fencing entry is outside its schema: %w", ErrOrphanBoundaryUnenumerable, err)
			}
			remote = append(remote, entry)
		default:
			local = true
		}
	}
	if unavailable > 0 {
		// §9's custody sentinel is a boundary of exactly one member. The
		// attestation is the proof for that sole entry; a boundary that mixes it
		// with members this build can enumerate must never clear on the
		// attestation alone, so the mixed shape fails closed.
		if unavailable == 1 && len(members) == 1 {
			return ErrOrphanBoundaryUnavailable
		}
		return fmt.Errorf("%w: the persisted boundary mixes the boundary-unavailable sentinel with other entries", ErrOrphanBoundaryUnenumerable)
	}
	if len(remote) > 0 {
		if local || len(remote) > 1 {
			return fmt.Errorf("%w: a persisted boundary mixes remote-fencing entries with another variant", ErrOrphanBoundaryUnenumerable)
		}
		return verifyRemoteFencing(remote[0], opts)
	}
	return verifyLocalBoundary(record.OrphanBoundary, opts)
}

// decodeBoundaryMembers splits a persisted boundary into its raw members. Only a
// real JSON array is a boundary: `null` unmarshals into a nil slice with no
// error, so it would otherwise read as verified-empty — the opposite of what a
// lost value means. A scalar, an object, or a malformed array is refused the
// same way.
func decodeBoundaryMembers(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("no persisted boundary")
	}
	if trimmed[0] != '[' {
		return nil, errors.New("the persisted boundary is not a JSON array")
	}
	var members []json.RawMessage
	if err := json.Unmarshal(trimmed, &members); err != nil {
		return nil, fmt.Errorf("the persisted boundary is not an array: %w", err)
	}
	return members, nil
}

// verifyRemoteFencing applies §9's remote clean rule: every persisted lease
// entry must be enumerated and exit-confirmed against the live lease state
// under its stored ownership identity. The guard-epoch comparison is
// deliberately not consulted: a guard advance fences future actions only and
// never proves already-running lease commands exited.
func verifyRemoteFencing(boundary RemoteFencingBoundary, opts VerifyOptions) error {
	if opts.VerifyLeaseEntry == nil {
		return fmt.Errorf("%w: remote lease enumeration is not configured", ErrOrphanBoundaryUnenumerable)
	}
	for _, entry := range boundary.LeaseEntries {
		if err := entry.Ownership.Validate(); err != nil {
			// §9: an entry persisted without its ownership identity fails closed —
			// no clear.
			return fmt.Errorf("%w: a persisted lease entry carries no ownership identity: %w", ErrOrphanBoundaryUnenumerable, err)
		}
		gone, err := opts.VerifyLeaseEntry(entry)
		if err != nil {
			return fmt.Errorf("%w: verifying a persisted lease entry failed: %w", ErrOrphanBoundaryUnenumerable, err)
		}
		if !gone {
			return fmt.Errorf("%w: lease entry %q is still registered live", ErrOrphanBoundaryPresent, entry.Command)
		}
	}
	return nil
}

// verifyLocalBoundary applies §3/§5's local clean rule to the persisted local
// entries, member-by-member against the recorded (pid, start-time) pairs. It is
// read-only: no member is signaled and no boundary is torn down.
func verifyLocalBoundary(raw json.RawMessage, opts VerifyOptions) error {
	open := opts.Open
	if open == nil {
		open = func(id execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
			return execenv.OpenBoundary(id)
		}
	}
	observe := opts.Observe
	if observe == nil {
		observe = execenv.ObserveProcess
	}
	groups, err := groupPersistedBoundary(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOrphanBoundaryUnenumerable, err)
	}
	for _, group := range groups {
		if err := verifyLocalGroup(group, open, observe); err != nil {
			return err
		}
	}
	return nil
}

// verifyLocalGroup checks one persisted local boundary group.
func verifyLocalGroup(group boundaryGroup, open func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error), observe func(int) (string, error)) error {
	handle, err := open(group.identity)
	if err != nil {
		if errors.Is(err, execenv.ErrBoundaryGone) {
			// A vanished boundary is not on its own proof the process is dead: every
			// recorded pair must still be gone, and a boundary that recorded none
			// has no evidence at all and stays fenced.
			if len(group.pairs) == 0 {
				return fmt.Errorf("%w: the boundary is gone and no recorded pair proves the process gone", ErrOrphanBoundaryUnenumerable)
			}
			if err := recordedPairsGone(observe, group.pairs); err != nil {
				return fmt.Errorf("%w: %w", ErrOrphanBoundaryPresent, err)
			}
			return nil
		}
		return fmt.Errorf("%w: the boundary could not be opened: %w", ErrOrphanBoundaryUnenumerable, err)
	}
	// The handle is an open resource even though resolve never signals through
	// it: close it on every path. Teardown is deliberately not part of §5's clean
	// rule — the operator confirms the members gone, and removing the boundary is
	// the reap's or the operator's to do — so a close failure never flips a
	// proven-clean verdict.
	defer func() { _ = handle.Close() }()
	members, err := handle.Members()
	if err != nil {
		return fmt.Errorf("%w: enumeration is unavailable: %w", ErrOrphanBoundaryUnenumerable, err)
	}
	if !handle.Enforcing() {
		return fmt.Errorf("%w: this platform's boundary cannot prove emptiness", ErrOrphanBoundaryUnenumerable)
	}
	verified, unrecognized := partitionMembers(members, group.pairs)
	if len(verified) > 0 || len(unrecognized) > 0 {
		// §5: resolve never reaches a response with members still present, and it
		// never force-clears. A verified member is live work the operator must
		// confirm gone; an unrecognized member reads as live per §3.
		return fmt.Errorf("%w: the boundary still holds %d member(s)", ErrOrphanBoundaryPresent, len(verified)+len(unrecognized))
	}
	// Empty, or every member's start token differs: re-verify every recorded pair
	// the way the reap does, so a pair alive outside the boundary keeps the
	// record fenced rather than clearing it.
	if err := recordedPairsGone(observe, group.pairs); err != nil {
		return fmt.Errorf("%w: %w", ErrOrphanBoundaryPresent, err)
	}
	return nil
}
