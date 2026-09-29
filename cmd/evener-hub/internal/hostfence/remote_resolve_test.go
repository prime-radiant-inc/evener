package hostfence

// Tests for §9's remote-fencing resolve verification through the pinned helper:
// the read-only gate, the per-entry ownership matching, and the fail-closed
// arms — all over a scripted Runner, no ssh and no host.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedFenceRunner serves the helper protocol's two read-only answers.
type scriptedFenceRunner struct {
	version     string
	versionExit int
	entries     string
	entriesExit int
	commands    []string
}

func (r *scriptedFenceRunner) Run(_ context.Context, command string) (string, string, int, error) {
	r.commands = append(r.commands, command)
	switch {
	case strings.HasSuffix(command, " version"):
		if r.versionExit != 0 {
			return "", "no helper", r.versionExit, nil
		}
		if r.version == "" {
			return "1\n", "", 0, nil
		}
		return r.version, "", 0, nil
	case strings.HasSuffix(command, " entries"):
		if r.entriesExit != 0 {
			return "", "lease refusals", r.entriesExit, nil
		}
		return r.entries, "", 0, nil
	}
	return "", "unexpected command", 2, nil
}

// remoteBoundary builds §9's remote-fencing boundary around one persisted entry
// with the given ownership JSON.
func remoteBoundary(t *testing.T, ownership string) RemoteFencingBoundary {
	t.Helper()
	return RemoteFencingBoundary{
		Kind:         RemoteFencingBoundaryKind,
		FencingEpoch: Epoch{BootID: "boot-1", OpSeq: 2},
		GuardEpoch:   3,
		LeaseEntries: []LeaseRef{{
			Command: "deploy --now", RegisteredAt: "2026-09-28T10:00:00Z",
			Ownership: ownershipFromJSON(t, ownership),
		}},
	}
}

// ownershipFromJSON decodes one ownership variant through the strict decoder.
func ownershipFromJSON(t *testing.T, raw string) Ownership {
	t.Helper()
	var ownership Ownership
	if err := ownership.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatalf("ownership %s: %v", raw, err)
	}
	return ownership
}

// leaseEnvelope wraps lease-entry JSON in the helper's enumeration shape.
func leaseEnvelope(entries string) string {
	return `{"version":1,"entries":[` + entries + `]}`
}

func TestVerifyRemoteFencingEntriesCleanRule(t *testing.T) {
	pidBoundary := remoteBoundary(t, `{"pid":41,"pidStartTime":"777"}`)
	nonceBoundary := remoteBoundary(t, `{"nonce":"n2"}`)
	cgroupBoundary := remoteBoundary(t, `{"cgroupId":"/cg/remote"}`)
	livePid := `{"id":"n1","command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z",` +
		`"ownership":{"pid":41,"pidStartTime":"777"},"state":"running","descendants":[]}`
	reusedPid := `{"id":"n1","command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z",` +
		`"ownership":{"pid":41,"pidStartTime":"999"},"state":"running","descendants":[]}`
	settledPid := `{"id":"n1","command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z",` +
		`"ownership":{"pid":41,"pidStartTime":"777"},"state":"exited","exit":0,` +
		`"exitedAt":"2026-09-28T10:00:05Z","descendants":[]}`
	liveNonce := `{"id":"n2","command":"restart","registeredAt":"2026-09-28T10:00:01Z",` +
		`"ownership":{"nonce":"n2"},"state":"registering","descendants":[]}`
	liveCgroup := `{"id":"n3","command":"deploy","registeredAt":"2026-09-28T10:00:02Z",` +
		`"ownership":{"cgroupId":"/cg/remote"},"state":"running","descendants":[]}`

	for name, tc := range map[string]struct {
		boundary RemoteFencingBoundary
		entries  string
		want     error
	}{
		"no matching live entry reads clean":  {boundary: pidBoundary, entries: leaseEnvelope(""), want: nil},
		"settled entry reads clean":           {boundary: pidBoundary, entries: leaseEnvelope(settledPid), want: nil},
		"reused pid reads clean":              {boundary: pidBoundary, entries: leaseEnvelope(reusedPid), want: nil},
		"matching live pid refuses busy":      {boundary: pidBoundary, entries: leaseEnvelope(livePid), want: ErrOrphanBoundaryPresent},
		"matching live nonce refuses busy":    {boundary: nonceBoundary, entries: leaseEnvelope(liveNonce), want: ErrOrphanBoundaryPresent},
		"matching live cgroup refuses busy":   {boundary: cgroupBoundary, entries: leaseEnvelope(liveCgroup), want: ErrOrphanBoundaryPresent},
		"other live entry reads clean for it": {boundary: nonceBoundary, entries: leaseEnvelope(livePid), want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &scriptedFenceRunner{entries: tc.entries}
			err := VerifyRemoteFencingEntries(context.Background(), "side", tc.boundary, runner)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("VerifyRemoteFencingEntries = %v, want clean", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("VerifyRemoteFencingEntries = %v, want %v", err, tc.want)
			}
			if len(runner.commands) != 2 || !strings.HasSuffix(runner.commands[0], " version") || !strings.HasSuffix(runner.commands[1], " entries") {
				t.Fatalf("helper commands = %v, want the version self-test then the enumeration", runner.commands)
			}
		})
	}
}

func TestVerifyRemoteFencingEntriesFailClosed(t *testing.T) {
	boundary := remoteBoundary(t, `{"pid":41,"pidStartTime":"777"}`)
	// A helper the gate refuses returns its typed refusal, never a clean verdict.
	absent := &scriptedFenceRunner{versionExit: 1}
	err := VerifyRemoteFencingEntries(context.Background(), "side", boundary, absent)
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("absent helper = %v, want the typed %s refusal", err, DiscriminatorHelperAbsent)
	}
	untrusted := &scriptedFenceRunner{version: "2\n"}
	err = VerifyRemoteFencingEntries(context.Background(), "side", boundary, untrusted)
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperUntrusted {
		t.Fatalf("untrusted helper = %v, want the typed %s refusal", err, DiscriminatorHelperUntrusted)
	}
	// A refused enumeration fails closed.
	refused := &scriptedFenceRunner{entriesExit: 3}
	if err := VerifyRemoteFencingEntries(context.Background(), "side", boundary, refused); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("refused enumeration = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
	// No runner at all fails closed.
	if err := VerifyRemoteFencingEntries(context.Background(), "side", boundary, nil); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("no runner = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
	// An entry persisted without its ownership identity fails closed before any
	// helper round trip.
	identityless := RemoteFencingBoundary{
		Kind: RemoteFencingBoundaryKind, FencingEpoch: Epoch{BootID: "boot-1", OpSeq: 2}, GuardEpoch: 3,
		LeaseEntries: []LeaseRef{{Command: "deploy", RegisteredAt: "2026-09-28T10:00:00Z"}},
	}
	runner := &scriptedFenceRunner{entries: leaseEnvelope("")}
	if err := VerifyRemoteFencingEntries(context.Background(), "side", identityless, runner); !errors.Is(err, ErrOrphanBoundaryUnenumerable) {
		t.Fatalf("identity-less entry = %v, want %v", err, ErrOrphanBoundaryUnenumerable)
	}
}

func TestDecodeRemoteFencingBoundary(t *testing.T) {
	raw := `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
		`"leaseEntries":[{"command":"deploy","registeredAt":"2026-09-28T10:00:00Z","ownership":{"nonce":"n1"}}]}]`
	boundary, remote, err := DecodeRemoteFencingBoundary([]byte(raw))
	if err != nil || !remote || boundary.GuardEpoch != 3 || len(boundary.LeaseEntries) != 1 {
		t.Fatalf("DecodeRemoteFencingBoundary = (%+v, %v, %v), want the one-member remote boundary", boundary, remote, err)
	}
	for name, other := range map[string]string{
		"local entry":  `[{"kind":"local-linux","cgroupId":"/cg","nonce":"n1","pid":1,"startTime":"2"}]`,
		"markerless":   `[{"kind":"local-markerless","platform":"linux","nonce":"n1"}]`,
		"unavailable":  `[{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"/x"}]`,
		"multi member": `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"b","opSeq":1},"guardEpoch":2,"leaseEntries":[]},{"kind":"local-markerless","platform":"linux","nonce":"n"}]`,
		"empty":        ``,
	} {
		if _, remote, err := DecodeRemoteFencingBoundary([]byte(other)); err != nil || remote {
			t.Fatalf("%s: (remote %v, err %v), want not-remote without an error", name, remote, err)
		}
	}
}
