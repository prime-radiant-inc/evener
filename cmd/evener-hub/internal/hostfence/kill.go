package hostfence

import (
	"context"
	"fmt"
)

// The fenced kill path of crash-fencing §4:101. The worker signals the
// superseded epoch's lease-tracked work through the trusted wrapper, which
// revalidates each entry's stored ownership identity on the remote before any
// signal — "The token is verified on the remote before signaling, mirroring the
// local boundary-plus-nonce rule in §3." Nothing here composes a raw kill: the
// helper's `kill` operation is the only path, so no direct-SSH kill exists.

// KillReport is the helper's answer to one `kill` call: whether a verified
// process was signaled, and whether any process still matches the entry's
// stored ownership identity. A live answer is what the worker's bounded wait
// keeps polling; a not-live answer is the exit confirmation §4 requires.
type KillReport struct {
	Version int `json:"version"`
	// ID is the lease entry id this call addressed.
	ID string `json:"id"`
	// Signaled reports whether this call signaled at least one verified process.
	Signaled bool `json:"signaled"`
	// Live reports whether any process still matches the entry's stored
	// ownership identity. A missing entry reads not live.
	Live bool `json:"live"`
	// State is the entry's lease state after the call: empty when no entry
	// exists, "killed" once the kill path marked it through the same file, and
	// otherwise the state the entry already carried.
	State string `json:"state"`
	// Remaining are the processes still matching the identity, carrying the
	// start token the remote observed ("unknown" when it could not be read).
	Remaining []Descendant `json:"remaining,omitempty"`
}

// KillCommand builds §4's remote kill of one lease entry under the presented
// epoch. The helper enforces the lease holder server-side: an epoch that does
// not hold the taken-over lease is refused stale.
func (w Wrapper) KillCommand(e Epoch, id string) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if id == "" || len(id) > MaxBootIDBytes || !shellTokenSafe(id) {
		return "", fmt.Errorf("%w: a lease entry id is required", ErrInvalidGuard)
	}
	return fmt.Sprintf("%s kill %s %d %s", w.remotePath(), shellQuote(e.BootID), e.OpSeq, shellQuote(id)), nil
}

// Kill runs one fenced kill through the verified helper: the wrapper
// revalidates the entry's ownership identity remotely, signals the verified
// instance, and reports whether anything still matches. It exists only on a
// Verified helper, so no signal is ever issued behind an unverified one.
func (v Verified) Kill(ctx context.Context, e Epoch, id string) (KillReport, error) {
	return v.wrapper.kill(ctx, e, id)
}

// kill is Kill's body, kept on Wrapper for the verified handle.
func (w Wrapper) kill(ctx context.Context, e Epoch, id string) (KillReport, error) {
	command, err := w.KillCommand(e, id)
	if err != nil {
		return KillReport{}, err
	}
	stdout, stderr, exit, err := w.run(ctx, command)
	if err != nil {
		return KillReport{}, err
	}
	if exit != 0 {
		return KillReport{}, w.refusal(stderr, exit)
	}
	return DecodeKillReport([]byte(stdout))
}

// DecodeKillReport decodes one kill answer. The helper's output is a trust
// boundary, so this is strict like the other decoders: a shape outside the
// protocol is refused, never half-read into a claim that work is dead.
func DecodeKillReport(raw []byte) (KillReport, error) {
	var report KillReport
	if err := decodeStrict(raw, &report); err != nil {
		return KillReport{}, err
	}
	if err := requireFields(raw, []string{"version", "id", "signaled", "live", "state"}, nil); err != nil {
		return KillReport{}, err
	}
	if report.Version != ProtocolVersion {
		return KillReport{}, fmt.Errorf("%w: version %d, want %d", ErrInvalidGuard, report.Version, ProtocolVersion)
	}
	if report.ID == "" {
		return KillReport{}, fmt.Errorf("%w: a kill answer carries no entry id", ErrInvalidGuard)
	}
	switch report.State {
	case "", LeaseRegistering, LeaseRunning, LeaseExited, LeaseKilled:
	default:
		return KillReport{}, fmt.Errorf("%w: a kill answer carries state %q", ErrInvalidGuard, report.State)
	}
	// A live answer requires a live state: "live" is the clean-verdict's
	// negation, and a settled or missing entry can never be reported live.
	if report.Live && report.State != LeaseRegistering && report.State != LeaseRunning {
		return KillReport{}, fmt.Errorf("%w: a kill answer reports state %q live", ErrInvalidGuard, report.State)
	}
	for _, remaining := range report.Remaining {
		if err := remaining.Validate(); err != nil {
			return KillReport{}, err
		}
	}
	return report, nil
}
