package hostops

import (
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// Probe epochs and the serving hub's guard epoch (deploy pipeline 08b §6 step 2,
// §10).
//
// A probe epoch is an epoch-shaped record — a (controller boot id, per-host
// monotonic op sequence) pair — that authorizes exactly one bounded
// probe mutation. The controller side persists it in the operation-store file in
// its own atomic write before the first `evener/host/running` call, bound to the
// host's current (generation, incarnation id) pair; the eventual token mint
// supersedes it, a plan failure deletes it, and boot deletes an epoch-only row
// silently (§4, §7). It is an ephemeral non-listed row: it carries no token, no
// worker, no deploy/restart kind, and never appears in `operations` reads.
//
// The serving side is the guard-epoch row: §10 requires the serving hub to
// validate the epoch the caller presented against the guard epoch it last
// admitted before the probe's write half runs, refuse a stale same-boot epoch
// without persisting it or probing, and persist only an admitted epoch.
//
// The crash-fencing execution the epoch model once anticipated — lease
// takeover, bounded kill/wait of the superseded epoch, the remote guard file's
// compare-and-advance, and the fencing quarantine — was removed with the rest
// of the program (comp08). What remains is the honest subset this file always
// implemented: the probe epoch's persistence and the guard epoch's admission,
// where the guard row admits an epoch whose boot id differs from the stored one
// and only refuses a lower op sequence for the same boot id — the accepted
// cross-restart limitation. The row is the single current epoch this hub was
// last presented, since v1 admits one calling controller per host.

// ProbeEpoch is one host's durable probe-epoch record: the controller boot id it
// was minted under, the per-host monotonic op sequence that orders it within
// that boot, and the (generation, incarnation id) pair it is bound to.
type ProbeEpoch struct {
	Host          string    `json:"host"`
	BootID        string    `json:"bootId"`
	OpSeq         uint64    `json:"opSeq"`
	Generation    uint64    `json:"generation"`
	IncarnationID string    `json:"incarnationId"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ProbeEpochRequest is a caller's half of a probe epoch: the host and the
// current registry pair to bind it to, plus the controller boot id. The store
// assigns the per-host op sequence, so two plans in one boot cannot collide and
// the sequence never moves backward — not even across a mint that superseded the
// previous row.
type ProbeEpochRequest struct {
	Host          string
	BootID        string
	Generation    uint64
	IncarnationID string
}

// GuardEpoch is the fencing-epoch shape the serving hub persists for the caller
// it was last presented by: the caller's controller boot id and per-host op
// sequence.
type GuardEpoch struct {
	BootID string `json:"bootId"`
	OpSeq  uint64 `json:"opSeq"`
}

// ErrInvalidProbeEpoch reports a probe-epoch request or row outside the schema
// every writer produces.
var ErrInvalidProbeEpoch = errors.New("hostops: invalid probe epoch")

// ErrStaleGuardEpoch reports a presented epoch older than the one the serving
// hub already admitted for the same controller boot: §10's stale-epoch refusal,
// answered without probing.
var ErrStaleGuardEpoch = errors.New("hostops: stale fencing epoch")

// ErrInvalidGuardEpoch reports a presented epoch outside the fencing-epoch
// shape: an absent or malformed epoch is never served as an unfenced write.
var ErrInvalidGuardEpoch = errors.New("hostops: invalid fencing epoch")

// PersistProbeEpoch writes host's probe epoch in its own atomic store write,
// superseding any earlier row for the host, and returns the record it wrote. The
// per-host op sequence it assigns is strictly above every sequence the host has
// ever been assigned (the durable counter survives both supersede and reap), so
// an abandoned epoch's sequence is never reused within a controller boot.
//
// The caller persists before the first probe call, and keeps the response's
// epoch so the probe can present it (§6 step 2: "presenting the persisted probe
// epoch (never a default, never absent)"). A write failure leaves the store
// exactly as it was, so nothing was launched.
func (s *Store) PersistProbeEpoch(req ProbeEpochRequest) (ProbeEpoch, error) {
	if s == nil {
		return ProbeEpoch{}, errors.New("hostops: store is not configured")
	}
	if err := validateProbeEpochRequest(req); err != nil {
		return ProbeEpoch{}, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	if next.ProbeEpochSeq == nil {
		next.ProbeEpochSeq = map[string]uint64{}
	}
	next.ProbeEpochSeq[req.Host]++
	row := ProbeEpoch{
		Host:          req.Host,
		BootID:        req.BootID,
		OpSeq:         next.ProbeEpochSeq[req.Host],
		Generation:    req.Generation,
		IncarnationID: req.IncarnationID,
		CreatedAt:     nowUTC(),
	}
	if err := validateProbeEpoch(row); err != nil {
		return ProbeEpoch{}, err
	}
	next.ProbeEpochs = append(dropProbeEpochHost(next.ProbeEpochs, req.Host), row)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return ProbeEpoch{}, err
	}
	// See Create: a landed rename means the row is durable even when the
	// directory sync behind it failed.
	return row, err
}

// ProbeEpoch returns host's current probe-epoch row, if one is persisted. It is
// the read the probe path and the tests use; the row never appears in
// `operations` reads.
func (s *Store) ProbeEpoch(host string) (ProbeEpoch, bool) {
	if s == nil {
		return ProbeEpoch{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	for _, row := range s.cell.state.ProbeEpochs {
		if row.Host == host {
			return row, true
		}
	}
	return ProbeEpoch{}, false
}

// ReapProbeEpochs deletes every probe-epoch row in one atomic write and returns
// how many it dropped, for the boot log. §7: an epoch-only row is deleted
// silently — never transitioned to `interrupted`, never reviving a token or a
// worker — so a crash between the epoch's persist and the probe (or between the
// probe and a plan's mint) leaves nothing a later boot acts on. The per-host op
// sequence counters deliberately survive: a later plan in the same boot never
// reuses an abandoned epoch's sequence.
func (s *Store) ReapProbeEpochs() (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	next := cloneSnapshot(s.cell.state)
	reaped := len(next.ProbeEpochs)
	if reaped == 0 {
		return 0, nil
	}
	next.ProbeEpochs = nil
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return 0, err
	}
	return reaped, err
}

// DropProbeEpoch deletes host's probe-epoch row when it is exactly the epoch
// identified by (bootID, opSeq) — §6 step 2's failure path: "probe epochs never
// launch workers and never outlive their `plan` call — the mint supersedes them
// or the call's failure path deletes them". The match is exact so a failure
// handler can never drop a row a different plan persisted; a row that is
// already gone (the mint superseded it) or that names another epoch is a
// no-op, which also makes a double drop harmless.
func (s *Store) DropProbeEpoch(host, bootID string, opSeq uint64) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if !slices.ContainsFunc(s.cell.state.ProbeEpochs, func(row ProbeEpoch) bool {
		return row.Host == host && row.BootID == bootID && row.OpSeq == opSeq
	}) {
		return nil
	}
	next := cloneSnapshot(s.cell.state)
	next.ProbeEpochs = slices.DeleteFunc(next.ProbeEpochs, func(row ProbeEpoch) bool {
		return row.Host == host && row.BootID == bootID && row.OpSeq == opSeq
	})
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return err
	}
	return err
}

// GuardEpoch returns the epoch the serving hub last admitted, if any.
func (s *Store) GuardEpoch() (GuardEpoch, bool) {
	if s == nil {
		return GuardEpoch{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if s.cell.state.GuardEpoch == nil {
		return GuardEpoch{}, false
	}
	return *s.cell.state.GuardEpoch, true
}

// AdmitGuardEpoch validates a presented epoch against the guard epoch this hub
// last admitted and persists the admitted one before the probe's write half runs
// (§10: "the serving hub validates the presented epoch against the guard epoch
// it last admitted for the host before anything is written or probed ... only
// an admitted epoch is persisted before the write half runs").
//
// Staleness as far as this machinery can honestly decide it: an epoch carrying
// the same controller boot id as the stored one but a lower op sequence is
// stale and refused — the current epoch, or a replay of it, is admitted
// idempotently, and a higher sequence is persisted. An epoch from a different
// boot id has no defined order against the stored one (the pair alone has no
// defined cross-restart order; the remote guard file's compare-and-advance that
// once supplied it was withdrawn with the crash-fencing program, comp08), so it
// is admitted and persisted — the accepted cross-restart limitation.
//
// A malformed epoch — no boot id, or a zero op sequence, both of which are what
// an absent or defaulted wire field decodes to — is ErrInvalidGuardEpoch: it is
// never persisted and never authorizes a probe.
func (s *Store) AdmitGuardEpoch(presented GuardEpoch) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	if err := validateGuardEpoch(presented); err != nil {
		return err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if stored := s.cell.state.GuardEpoch; stored != nil {
		if stored.BootID == presented.BootID && presented.OpSeq < stored.OpSeq {
			return fmt.Errorf("%w: epoch %s/%d is older than the admitted %s/%d",
				ErrStaleGuardEpoch, presented.BootID, presented.OpSeq, stored.BootID, stored.OpSeq)
		}
		if *stored == presented {
			// Already the admitted epoch: the persistence §10 requires already
			// happened, and a replay writes nothing.
			return nil
		}
	}
	next := cloneSnapshot(s.cell.state)
	admitted := presented
	next.GuardEpoch = &admitted
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return err
	}
	return err
}

// dropProbeEpochHost removes host's probe-epoch row from rows, preserving order.
func dropProbeEpochHost(rows []ProbeEpoch, host string) []ProbeEpoch {
	return slices.DeleteFunc(rows, func(row ProbeEpoch) bool { return row.Host == host })
}

// validateProbeEpochRequest checks the caller-supplied half of a probe epoch.
func validateProbeEpochRequest(req ProbeEpochRequest) error {
	switch {
	case req.Host == "":
		return fmt.Errorf("%w: a probe epoch needs a host", ErrInvalidProbeEpoch)
	case !utf8.ValidString(req.Host):
		return fmt.Errorf("%w: the host name is not valid UTF-8", ErrInvalidProbeEpoch)
	case req.BootID == "":
		// §6: the epoch is "never a default, never absent" — a missing boot id is
		// what an unset caller field looks like, and a defaulted epoch must never
		// authorize a probe.
		return fmt.Errorf("%w: a probe epoch needs the controller boot id", ErrInvalidProbeEpoch)
	case req.Generation == 0:
		return fmt.Errorf("%w: a probe epoch pins no generation", ErrInvalidProbeEpoch)
	case req.IncarnationID == "":
		return fmt.Errorf("%w: a probe epoch pins no incarnation id", ErrInvalidProbeEpoch)
	}
	return nil
}

// validateProbeEpoch checks one persisted probe-epoch row.
func validateProbeEpoch(row ProbeEpoch) error {
	if err := validateProbeEpochRequest(ProbeEpochRequest{
		Host: row.Host, BootID: row.BootID, Generation: row.Generation, IncarnationID: row.IncarnationID,
	}); err != nil {
		return err
	}
	switch {
	case row.OpSeq == 0:
		return fmt.Errorf("%w: probe epoch for %q carries no op sequence", ErrInvalidProbeEpoch, row.Host)
	case row.CreatedAt.IsZero():
		return fmt.Errorf("%w: probe epoch for %q carries no timestamp", ErrInvalidProbeEpoch, row.Host)
	}
	return nil
}

// validateGuardEpoch checks a presented or stored guard epoch.
func validateGuardEpoch(epoch GuardEpoch) error {
	switch {
	case epoch.BootID == "":
		return fmt.Errorf("%w: the epoch presents no boot id", ErrInvalidGuardEpoch)
	case !utf8.ValidString(epoch.BootID):
		return fmt.Errorf("%w: the boot id is not valid UTF-8", ErrInvalidGuardEpoch)
	case epoch.OpSeq == 0:
		return fmt.Errorf("%w: the epoch presents no op sequence", ErrInvalidGuardEpoch)
	}
	return nil
}

// cloneGuardEpoch copies a stored guard epoch, pointer included.
func cloneGuardEpoch(epoch *GuardEpoch) *GuardEpoch {
	if epoch == nil {
		return nil
	}
	out := *epoch
	return &out
}
