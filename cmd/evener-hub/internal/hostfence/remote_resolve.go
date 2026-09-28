package hostfence

// Crash-fencing spec 08c §9's remote-fencing resolve verification: the clean-rule
// check `orphan-resolve` runs over a persisted `remote-fencing` boundary, through
// the pinned helper (§6's version gate first), with no SSH of its own — the
// caller supplies the one-shot remote Runner over the manager's existing ssh
// process seam, and tests supply a scripted one.
//
// §9: a remote-fencing boundary "reads clean only when no persisted lease entry
// is still registered live and every persisted lease entry is enumerated and
// exit-confirmed against the live lease state under its stored ownership
// identity". The guard-epoch comparison is deliberately not consulted: a guard
// advance fences future actions only and never proves already-running lease
// commands exited. Enumeration of every persisted entry is mandatory regardless.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrOrphanBoundaryPresent reports a boundary that still holds members: a
// verified member (never signaled by resolve), an unrecognized member, a
// recorded pair still alive, or a remote lease entry still registered live. The
// caller refuses with the transient busy form.
var ErrOrphanBoundaryPresent = errors.New("hostfence: the persisted boundary still holds members")

// ErrOrphanBoundaryUnenumerable reports a boundary that cannot be enumerated or
// whose enumeration cannot prove emptiness (an unreachable boundary with no
// recorded pair, a non-enforcing platform, a failed observation, a
// remote-fencing record with no lease-verification seam, or a helper refusal).
// Fail closed: never clean.
var ErrOrphanBoundaryUnenumerable = errors.New("hostfence: the persisted boundary cannot be enumerated")

// ErrOrphanBoundaryUnavailable reports §9's `boundary-unavailable` custody
// entry: the boundary was lost to corruption, so it is proof of nothing. Such a
// record resolves only through the operator attestation, never through
// enumeration.
var ErrOrphanBoundaryUnavailable = errors.New("hostfence: the boundary was lost to corruption and requires the operator attestation")

// DecodeRemoteFencingBoundary reports whether a persisted boundary's sole member
// is a `remote-fencing` entry, and returns it decoded. A boundary that is not
// exactly that shape reports remote=false: another variant (including the
// boundary-unavailable sentinel) is the local arm's or the attestation's.
func DecodeRemoteFencingBoundary(raw json.RawMessage) (RemoteFencingBoundary, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return RemoteFencingBoundary{}, false, nil
	}
	var members []json.RawMessage
	if err := json.Unmarshal(trimmed, &members); err != nil {
		return RemoteFencingBoundary{}, false, fmt.Errorf("the persisted boundary is not a JSON array: %w", err)
	}
	if len(members) != 1 {
		return RemoteFencingBoundary{}, false, nil
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(members[0], &discriminator); err != nil {
		return RemoteFencingBoundary{}, false, fmt.Errorf("a boundary member carries no kind discriminator: %w", err)
	}
	if discriminator.Kind != RemoteFencingBoundaryKind {
		return RemoteFencingBoundary{}, false, nil
	}
	var boundary RemoteFencingBoundary
	if err := json.Unmarshal(members[0], &boundary); err != nil {
		return RemoteFencingBoundary{}, false, fmt.Errorf("a remote-fencing entry does not decode: %w", err)
	}
	return boundary, true, nil
}

// VerifyRemoteFencingEntries applies §9's remote clean rule for one persisted
// remote-fencing boundary through the pinned helper, over the caller's remote
// Runner (the ssh process seam in production, a scripted fake in tests). It runs
// §6's read-only presence-and-version gate first, then enumerates the lease file
// and matches every persisted entry against the live lease state under its
// stored ownership identity.
//
// A helper the gate refuses returns its typed *HelperGateError — the caller maps
// it to the fencing-helper-absent/untrusted form rather than a generic busy —
// and everything else that cannot prove exit fails closed as
// ErrOrphanBoundaryUnenumerable.
func VerifyRemoteFencingEntries(ctx context.Context, host string, boundary RemoteFencingBoundary, runner Runner) error {
	if runner == nil {
		return fmt.Errorf("%w: remote lease enumeration is not configured", ErrOrphanBoundaryUnenumerable)
	}
	if err := boundary.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrOrphanBoundaryUnenumerable, err)
	}
	wrapper := Wrapper{Runner: runner, Host: host}
	verified, err := wrapper.Check(ctx)
	if err != nil {
		// The helper gate's refusal or a transport failure: neither proves exit.
		return err
	}
	entries, err := verified.WrapperHandle().Entries(ctx)
	if err != nil {
		return fmt.Errorf("%w: enumerating the lease state failed: %w", ErrOrphanBoundaryUnenumerable, err)
	}
	for _, ref := range boundary.LeaseEntries {
		if err := ref.Ownership.Validate(); err != nil {
			// §9: an entry persisted without its ownership identity fails closed —
			// no clear.
			return fmt.Errorf("%w: a persisted lease entry carries no ownership identity: %w", ErrOrphanBoundaryUnenumerable, err)
		}
		if leaseRefLive(ref, entries) {
			return fmt.Errorf("%w: lease entry %q is still registered live", ErrOrphanBoundaryPresent, ref.Command)
		}
	}
	return nil
}

// leaseRefLive reports whether one persisted boundary entry still has a matching
// live holder in the enumerated lease state. The match runs under the entry's
// stored ownership identity: a pid compared with its kernel start time (a reused
// pid names a different process and is not this entry's holder), the nonce
// re-presented as the lease entry's own id, or the remote cgroup identity. An
// entry whose identity matches nothing live is already clean for that member,
// confirmed by the enumeration — never by controller-side inference.
func leaseRefLive(ref LeaseRef, entries []LeaseEntry) bool {
	for _, entry := range entries {
		if !leaseEntryLive(entry.State) {
			continue
		}
		switch {
		case ref.Ownership.PID != nil:
			if entry.Ownership.PID != nil && *entry.Ownership.PID == *ref.Ownership.PID &&
				entry.Ownership.PIDStartTime == ref.Ownership.PIDStartTime {
				return true
			}
		case ref.Ownership.Nonce != "":
			// The nonce arm's stored identity is the wrapper's per-spawn nonce,
			// which is the lease entry's own id in the lease file.
			if entry.ID == ref.Ownership.Nonce {
				return true
			}
		case ref.Ownership.CgroupID != "":
			if entry.Ownership.CgroupID != "" && entry.Ownership.CgroupID == ref.Ownership.CgroupID {
				return true
			}
		}
	}
	return false
}

// leaseEntryLive reports whether a lease entry's state is a live one: only
// registering and running entries are work a resolve must see gone.
func leaseEntryLive(state string) bool {
	switch state {
	case LeaseRegistering, LeaseRunning:
		return true
	}
	return false
}
