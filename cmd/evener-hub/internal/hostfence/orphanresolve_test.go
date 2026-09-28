//go:build linux || darwin

package hostfence

// Tests for §5's resolve-time boundary verification: the read-only clean rule
// per variant, the fail-closed arms, and the boundary-unavailable refusal. The
// reap's fakes (fakeBoundary, openOnce) drive the local arms; a scripted lease
// seam drives the remote arm — no SSH, no host.

import (
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// boundaryRecord builds the read-only input the verifier consumes.
func boundaryRecord(raw string) hostops.Record {
	return hostops.Record{ID: "00000000000000000001", OrphanBoundary: json.RawMessage(raw)}
}

// goneObserve answers every pid as gone.
func goneObserve(int) (string, error) { return "", execenv.ErrBoundaryMemberGone }

// tokenObserve answers the given start token for the given pid.
func tokenObserve(pid int, token string) func(int) (string, error) {
	return func(candidate int) (string, error) {
		if candidate == pid {
			return token, nil
		}
		return "", execenv.ErrBoundaryMemberGone
	}
}

func TestVerifyOrphanBoundaryLocalCleanRule(t *testing.T) {
	markedLinux := `[{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":41,"startTime":"777"}]`
	markedDarwin := `[{"kind":"local-darwin","pgid":7,"sessionId":9,"pid":41,"startTime":"777","nonce":"n1"}]`
	markerless := `[{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n1","nonce":"n1"}]`
	for name, tc := range map[string]struct {
		boundary string
		handle   *fakeBoundary
		openErr  error
		observe  func(int) (string, error)
		want     error
	}{
		"marked linux, empty boundary, pair gone": {
			boundary: markedLinux, handle: &fakeBoundary{}, observe: goneObserve, want: nil,
		},
		"marked linux, verified member present": {
			boundary: markedLinux,
			handle:   &fakeBoundary{members: []execenv.BoundaryMember{{PID: 41, StartToken: "777"}}},
			observe:  goneObserve, want: ErrOrphanBoundaryPresent,
		},
		"marked linux, unrecognized member present": {
			boundary: markedLinux,
			handle:   &fakeBoundary{members: []execenv.BoundaryMember{{PID: 99, StartToken: "1"}}},
			observe:  goneObserve, want: ErrOrphanBoundaryPresent,
		},
		"marked linux, reused pid reads already clean": {
			boundary: markedLinux,
			handle:   &fakeBoundary{members: []execenv.BoundaryMember{{PID: 41, StartToken: "reused"}}},
			observe:  tokenObserve(41, "reused"), want: nil,
		},
		"marked linux, recorded pair alive outside the boundary": {
			boundary: markedLinux, handle: &fakeBoundary{}, observe: tokenObserve(41, "777"),
			want: ErrOrphanBoundaryPresent,
		},
		"marked darwin, empty boundary, pair gone": {
			boundary: markedDarwin, handle: &fakeBoundary{}, observe: goneObserve, want: nil,
		},
		"markerless, demonstrably empty": {
			boundary: markerless, handle: &fakeBoundary{}, observe: goneObserve, want: nil,
		},
		"markerless, member present": {
			boundary: markerless,
			handle:   &fakeBoundary{members: []execenv.BoundaryMember{{PID: 7, StartToken: "1"}}},
			observe:  goneObserve, want: ErrOrphanBoundaryPresent,
		},
		"non-enforcing platform fails closed": {
			boundary: markerless, handle: &fakeBoundary{notEnforcing: true}, observe: goneObserve,
			want: ErrOrphanBoundaryUnenumerable,
		},
		"enumeration unavailable": {
			boundary: markedLinux, handle: &fakeBoundary{membersErr: errors.New("boom")}, observe: goneObserve,
			want: ErrOrphanBoundaryUnenumerable,
		},
		"marked linux, boundary gone with every pair gone": {
			boundary: markedLinux, handle: &fakeBoundary{}, openErr: execenv.ErrBoundaryGone, observe: goneObserve,
			want: nil,
		},
		"markerless, boundary gone with no recorded pair": {
			boundary: markerless, handle: &fakeBoundary{}, openErr: execenv.ErrBoundaryGone, observe: goneObserve,
			want: ErrOrphanBoundaryUnenumerable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			open := func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) {
				if tc.openErr != nil {
					return nil, tc.openErr
				}
				return tc.handle, nil
			}
			err := VerifyOrphanBoundary(boundaryRecord(tc.boundary), VerifyOptions{Open: open, Observe: tc.observe})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("VerifyOrphanBoundary = %v, want clean", err)
				}
				if len(tc.handle.killed) > 0 {
					t.Fatalf("resolve signaled %v; it must never kill", tc.handle.killed)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("VerifyOrphanBoundary = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyOrphanBoundaryReopenedGoneBoundaryWithPairAlive(t *testing.T) {
	// A boundary that vanished still fences while its recorded process lives: the
	// reap's M3 rule applies to resolve too.
	boundary := `[{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":41,"startTime":"777"}]`
	open := func(execenv.BoundaryIdentity) (LocalBoundaryHandle, error) { return nil, execenv.ErrBoundaryGone }
	err := VerifyOrphanBoundary(boundaryRecord(boundary), VerifyOptions{Open: open, Observe: tokenObserve(41, "777")})
	if !errors.Is(err, ErrOrphanBoundaryPresent) {
		t.Fatalf("gone boundary with a live recorded pair = %v, want %v", err, ErrOrphanBoundaryPresent)
	}
	err = VerifyOrphanBoundary(boundaryRecord(boundary), VerifyOptions{Open: open, Observe: goneObserve})
	if err != nil {
		t.Fatalf("gone boundary with every recorded pair gone = %v, want clean", err)
	}
}

func TestVerifyOrphanBoundaryRemoteFencing(t *testing.T) {
	remote := `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
		`"leaseEntries":[{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{"pid":41,"pidStartTime":"777"}},` +
		`{"command":"restart","registeredAt":"2026-09-28T10:00:01Z","ownership":{"nonce":"n2"}}]}]`
	// Every entry exit-confirmed reads clean, even though the guard epoch may no
	// longer match (the comparison is deliberately not consulted).
	verified := 0
	err := VerifyOrphanBoundary(boundaryRecord(remote), VerifyOptions{VerifyLeaseEntry: func(entry LeaseRef) (bool, error) {
		verified++
		return true, nil
	}})
	if err != nil {
		t.Fatalf("all entries confirmed exited = %v, want clean", err)
	}
	if verified != 2 {
		t.Fatalf("enumeration checked %d entries, want every persisted entry (2)", verified)
	}
	// One entry still live refuses busy, and the enumeration must not stop early.
	checked := 0
	err = VerifyOrphanBoundary(boundaryRecord(remote), VerifyOptions{VerifyLeaseEntry: func(entry LeaseRef) (bool, error) {
		checked++
		return entry.Ownership.Nonce != "n2", nil
	}})
	if !errors.Is(err, ErrOrphanBoundaryPresent) {
		t.Fatalf("live entry = %v, want %v", err, ErrOrphanBoundaryPresent)
	}
	if checked != 2 {
		t.Fatalf("the live entry was not enumerated: %d checks", checked)
	}
	// A missing seam fails closed.
	if err := VerifyOrphanBoundary(boundaryRecord(remote), VerifyOptions{}); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("no lease seam = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
	// A seam error fails closed.
	if err := VerifyOrphanBoundary(boundaryRecord(remote), VerifyOptions{VerifyLeaseEntry: func(LeaseRef) (bool, error) {
		return false, errors.New("remote unreachable")
	}}); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("seam error = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
	// An entry persisted without its ownership identity fails closed.
	noOwnership := `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
		`"leaseEntries":[{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{}}]}]`
	if err := VerifyOrphanBoundary(boundaryRecord(noOwnership), VerifyOptions{VerifyLeaseEntry: func(LeaseRef) (bool, error) {
		return true, nil
	}}); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("identity-less entry = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
}

func TestVerifyOrphanBoundaryUnavailableAndEmpty(t *testing.T) {
	// A boundary-unavailable entry is never clean by enumeration.
	unavailable := `[{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"/state/custody.json"}]`
	if err := VerifyOrphanBoundary(boundaryRecord(unavailable), VerifyOptions{}); !errors.Is(err, ErrOrphanBoundaryUnavailable) {
		t.Fatalf("boundary-unavailable = %v, want %v", err, ErrOrphanBoundaryUnavailable)
	}
	// An empty array is verified empty — clean, and never confused with the
	// unavailable sentinel.
	if err := VerifyOrphanBoundary(boundaryRecord(`[]`), VerifyOptions{}); err != nil {
		t.Fatalf("empty boundary = %v, want clean", err)
	}
	// A malformed boundary never reads clean.
	if err := VerifyOrphanBoundary(boundaryRecord(`{"kind":"local-linux"}`), VerifyOptions{}); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("non-array boundary = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
}
