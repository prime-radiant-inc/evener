package hub

// This file owns the durable mutation receipts registry spec 08 §5/§6 defines:
// the [mutation_receipts."<scoped-key>"] records hub.toml carries beside its
// [[hosts]] array, the five-part scoped key they are keyed by, and the dedup
// lookup's arms over them.
//
// A receipt is the durable finalized outcome of one host mutation — the record
// a lost-response retry replays instead of re-applying. It is written by the
// very atomic hub.toml write that persists the mutation's commit (spec §5:
// "The commit, the `teardown-retry` clearance, and the re-add purge are all
// single atomic writes"), and a mutation that un-commits (a failed live phase)
// drops its receipt with the rollback write — a refusal, and an un-committed
// mutation, leave no receipt behind.
//
// CLOSED BY S11: §6's receipt compaction, the bounded pruned markers, and the
// pruned-receipt refusal arm all live in app_host_record_pruning.go; the
// lookup's marker arm below is the frozen key whose receipt a count/TTL
// compaction or a tombstone purge dropped (spec §6: "a replay naming a marked
// key refuses as `stale-entry` (pruned-generation)").
//
// DEFERRED (S12): §5's two-write staged-marker protocol
// (pending_mutation/finalizing_mutation, swapStarted/teardownStarted, the
// runtime phase, attempt tokens, foreign-marker finalization, boot recovery)
// is the later slice's seam. The single-write receipt is safe for this slice
// because no post-commit failure path exists yet: teardowns are best-effort
// and un-commit on failure, exactly as today, and the rollback drops the
// receipt with the entry.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// hostMutationKind is a receipt's mutation kind — the breadcrumb in the scoped
// key that keeps one mutationId's add, update and remove receipts distinct.
// The spellings are the durable-record vocabulary; the wire carries no kind
// field on these mutations (registry spec 08 §11).
type hostMutationKind string

const (
	hostMutationAdd    hostMutationKind = "add"
	hostMutationUpdate hostMutationKind = "update"
	hostMutationRemove hostMutationKind = "remove"
)

// The three outcomes a finalized receipt carries (spec §5/§6/§11):
//
//   - committed: the commit landed and every planned teardown completed;
//   - committed-with-teardown-failure: the commit landed but a post-commit
//     teardown failed — "a failure at or after the commit point is reported as
//     a committed-with-teardown-failure with the seam named, and recovery is
//     forward (retry the teardown / re-apply), never a restore of the prior
//     bytes" — with the durable remnant the response's `remnantId` names;
//   - collision-dropped: the post-rename reconcile observed a foreign write
//     that replaced the just-committed staged entry, so the file's bytes won
//     ("a hand edit the post-rename re-read observes is adopted (the file's
//     bytes win, the receipt says `collision-dropped`)") and the receipt
//     carries the dropped entry plus the winning fingerprint.
const (
	hostReceiptOutcomeCommitted        = "committed"
	hostReceiptOutcomeTeardownFailure  = "committed-with-teardown-failure"
	hostReceiptOutcomeCollisionDropped = "collision-dropped"
)

// MaxHostMutationIDBytes is spec 08 §1's bound on a mutationId: "opaque,
// non-empty, at most 128 bytes, no required structure". A client-supplied id
// over the bound is a validation refusal before the dedup lookup, exactly like
// a missing one.
const MaxHostMutationIDBytes = 128

// HostMutationReceiptRow is a receipt's stored row: the effective
// configuration the response renders, in the wire's lowerCamel spelling
// (spec §6: "the effective-config lowerCamel shape the response renders"). The
// live-state fields a row carries are deliberately absent — a receipt is
// durable state, and a replay renders what committed, never whatever a later
// attach happens to show.
type HostMutationReceiptRow struct {
	Name       string   `toml:"name"`
	Address    string   `toml:"address,omitempty"`
	User       string   `toml:"user,omitempty"`
	KeyPath    string   `toml:"keyPath,omitempty"`    //nolint:tagliatelle // §6 stores this row in the wire's lowerCamel spelling
	EvenerPath string   `toml:"evenerPath,omitempty"` //nolint:tagliatelle // §6 stores this row in the wire's lowerCamel spelling
	ConfigPath string   `toml:"configPath,omitempty"` //nolint:tagliatelle // §6 stores this row in the wire's lowerCamel spelling
	Addr       string   `toml:"addr,omitempty"`
	Roots      []string `toml:"roots,omitempty"`
}

// HostMutationReceipt is one [mutation_receipts."<scoped-key>"] record (spec 08
// §6): the finalized outcome of one committed host mutation. Its optional
// fields are present exactly on the outcomes that carry them — `droppedEntry`
// and `winningFingerprint` exactly on `collision-dropped`, `removed` exactly on
// a `collision-dropped` receipt whose winning arm is the hand-edit deletion,
// `remnantId` exactly when the commit staged a remnant, `remnantResolvedAt`
// exactly after `teardown-retry` or `teardown-recover` resolves it,
// `recoveryAttestation` exactly on receipts resolved through `teardown-recover`,
// and `bootRecovered` (as true) exactly when boot finalized a crash-window
// staged-receipt marker. Forward preservation for a record this build cannot
// decode is explicitly not offered — validateHostMutationReceipts refuses it
// loudly.
type HostMutationReceipt struct {
	// Outcome is the finalized outcome; "committed" is the only constructible
	// value in this slice.
	Outcome string `toml:"outcome"`
	// Row is the effective configuration the mutation committed, in the shape
	// the response renders.
	Row HostMutationReceiptRow `toml:"row"`
	// Generation and IncarnationID are the post-commit pair the receipt pins:
	// for add, the insert identity; for update, the bumped generation with the
	// live incarnation id; for remove, the removed incarnation's pair. The
	// scoped key repeats both, and validateHostMutationReceipts pins them equal.
	Generation    uint64 `toml:"generation"`
	IncarnationID string `toml:"incarnation_id"`
	// CommittedAt is the UTC RFC3339 instant the commit's atomic write landed.
	CommittedAt string `toml:"committed_at"`
	// Audit marks a keyless-add audit record (spec §11: "a keyless `add`
	// commits the server-keyed audit record with no client idempotency
	// semantics"). It is written exactly on the receipt a keyless add commits
	// under its server-generated key, and it selects the audit compaction
	// bound (at most 64 newest per name under the owner-set audit TTL, §11)
	// instead of the keyed receipts' superseded/current rules.
	Audit bool `toml:"audit,omitempty"`
	// DroppedEntry is the staged entry the post-rename reconcile dropped,
	// persisted "in the same lowerCamel effective-config shape as `HostRow`'s
	// config fields" (spec §6). Present exactly on `collision-dropped` receipts.
	DroppedEntry *HostMutationReceiptRow `toml:"dropped_entry,omitempty"`
	// WinningFingerprint is the winning hub.toml fingerprint the post-rename
	// reconcile observed. Present exactly on `collision-dropped` receipts, so "a
	// lost-response retry, even after restart, reconstructs both what was
	// dropped and which fingerprint won".
	WinningFingerprint string `toml:"winning_fingerprint,omitempty"`
	// Removed is the hand-edit-deletion marker: "the arm carries no `host` and
	// sets `removed: true` — the winning arm is the deletion, with the marker
	// tombstone rows use". Present exactly on a `collision-dropped` receipt
	// whose winning arm is the deletion.
	Removed bool `toml:"removed,omitempty"`
	// RemnantID is the pre-minted remnant id the commit staged. Present exactly
	// when the commit staged a remnant, and it is what the
	// committed-with-teardown-failure response names beside the seam.
	RemnantID string `toml:"remnant_id,omitempty"`
	// RemnantResolvedAt is present exactly after `teardown-retry` or
	// `teardown-recover` resolves the remnant.
	RemnantResolvedAt string `toml:"remnant_resolved_at,omitempty"`
	// RecoveryAttestation is "the `{operator, statement, observedAt}`
	// attestation the recovery call validated — the audited recovery contract's
	// durable record". Present exactly on receipts resolved through
	// `teardown-recover`.
	RecoveryAttestation *HostRecoveryAttestation `toml:"recovery_attestation,omitempty"`
	// BootRecovered is true exactly when boot finalized a crash-window
	// staged-receipt marker (spec §5: "the receipt records `bootRecovered: true`").
	BootRecovered bool `toml:"boot_recovered,omitempty"`
}

// hostReceiptScope is a scoped receipt key's five parts (spec §6: "mutation-id
// / name / kind / generation / incarnation-id order").
type hostReceiptScope struct {
	MutationID    string
	Name          string
	Kind          hostMutationKind
	Generation    uint64
	IncarnationID string
}

// hostReceiptScopedKey renders one receipt's scoped key: the five parts, each
// canonically percent-encoded and joined with "/" (spec §6), so an opaque
// mutation id can never introduce a delimiter, quote, or newline and two
// distinct keys can never collide.
func hostReceiptScopedKey(scope hostReceiptScope) string {
	parts := []string{
		percentEncodeReceiptPart(scope.MutationID),
		percentEncodeReceiptPart(scope.Name),
		percentEncodeReceiptPart(string(scope.Kind)),
		strconv.FormatUint(scope.Generation, 10),
		percentEncodeReceiptPart(scope.IncarnationID),
	}
	return strings.Join(parts, "/")
}

// parseHostReceiptScopedKey splits a scoped key back into its five parts,
// reporting whether the key is one this build's writer could have produced: a
// canonical re-encode, a complete identity, and a known kind. A key the writer
// could not produce is refused by validateHostMutationReceipts at boot, so a
// shipped hub.toml always parses.
func parseHostReceiptScopedKey(key string) (hostReceiptScope, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 5 {
		return hostReceiptScope{}, false
	}
	decoded := make([]string, len(parts))
	for i, part := range parts {
		value, ok := percentDecodeReceiptPart(part)
		if !ok {
			return hostReceiptScope{}, false
		}
		decoded[i] = value
	}
	generation, err := strconv.ParseUint(decoded[3], 10, 64)
	if err != nil || generation == 0 {
		return hostReceiptScope{}, false
	}
	scope := hostReceiptScope{
		MutationID:    decoded[0],
		Name:          decoded[1],
		Kind:          hostMutationKind(decoded[2]),
		Generation:    generation,
		IncarnationID: decoded[4],
	}
	if hostReceiptScopedKey(scope) != key {
		return hostReceiptScope{}, false
	}
	if !hostReceiptKindValid(scope.Kind) {
		return hostReceiptScope{}, false
	}
	return scope, true
}

// hostReceiptKindValid reports whether kind is one this build writes.
func hostReceiptKindValid(kind hostMutationKind) bool {
	switch kind {
	case hostMutationAdd, hostMutationUpdate, hostMutationRemove:
		return true
	default:
		return false
	}
}

// percentEncodeReceiptPart canonically percent-encodes one scoped-key part:
// RFC 3986's unreserved characters (ALPHA / DIGIT / "-" / "." / "_" / "~") stay
// literal and every other byte becomes %XX with uppercase hex. Canonical means
// one byte string has exactly one encoding, which is what makes the key a
// collision-free identity.
func percentEncodeReceiptPart(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isReceiptUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
}

// percentDecodeReceiptPart decodes a %XX-encoded part of a scoped key, refusing
// a stray "%", a truncated escape, an invalid hex pair, or a decoded byte that
// re-encodes differently (the canonicality half of the check).
func percentDecodeReceiptPart(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			if !isReceiptUnreserved(c) {
				// A character the encoder would have escaped: not canonical.
				return "", false
			}
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		high, ok := receiptHexValue(s[i+1])
		if !ok {
			return "", false
		}
		low, ok := receiptHexValue(s[i+2])
		if !ok {
			return "", false
		}
		b.WriteByte(high<<4 | low)
		i += 2
	}
	return b.String(), true
}

// isReceiptUnreserved reports whether c is one of RFC 3986's unreserved bytes.
func isReceiptUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '.' || c == '_' || c == '~':
		return true
	default:
		return false
	}
}

// receiptHexValue decodes one hex digit, refusing lowercase so an escape has
// exactly one canonical spelling.
func receiptHexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// validHostMutationID reports whether id is a shape this build accepts: opaque
// (never trimmed or normalized — the id is an identity, not prose), non-empty,
// valid UTF-8 so it can ride a TOML key, and within §1's 128-byte bound.
func validHostMutationID(id string) bool {
	return id != "" && len(id) <= MaxHostMutationIDBytes && utf8.ValidString(id)
}

// mutationIDShapeRefusal is the field-carrying validation refusal for a
// mutationId that is missing or over the bound, spelled the way the wire names
// the input (HostEntry's sibling policy, §11).
func mutationIDShapeRefusal(name, id string) error {
	if validHostMutationID(id) {
		return nil
	}
	message := fmt.Sprintf("host %q: mutationId must be non-empty and at most %d bytes", name, MaxHostMutationIDBytes)
	if id != "" {
		if !utf8.ValidString(id) {
			message = fmt.Sprintf("host %q: mutationId is not valid UTF-8", name)
		} else {
			message = fmt.Sprintf("host %q: mutationId is %d bytes, over the %d-byte bound", name, len(id), MaxHostMutationIDBytes)
		}
	}
	return appwire.InvalidHostField("mutationId", message)
}

// hostGuardedMutationRefusal validates update/remove's guarded-mutation fields:
// presence of all three first (spec §4: "Presence of all three is validated
// before the dedup check (missing any is a validation refusal committing
// nothing)"), then the mutation id's §1 shape. Every refusal is the
// field-carrying validation form, spelled with the wire's field names (the
// params' json tags), so the dialog can place the message.
func hostGuardedMutationRefusal(name, mutationID string, expectedGeneration uint64, expectedIncarnationID string) error {
	if mutationID == "" {
		return appwire.InvalidHostField("mutationId",
			fmt.Sprintf("host %q: mutationId is required, together with expectedGeneration and expectedIncarnationId", name))
	}
	if expectedGeneration == 0 {
		return appwire.InvalidHostField("expectedGeneration",
			fmt.Sprintf("host %q: expectedGeneration is required and must name a live generation", name))
	}
	if expectedIncarnationID == "" {
		return appwire.InvalidHostField("expectedIncarnationId",
			fmt.Sprintf("host %q: expectedIncarnationId is required", name))
	}
	return mutationIDShapeRefusal(name, mutationID)
}

// hostStaleEntryRefusal is §4's typed guard refusal: the target's current
// (generation, incarnation id) pair differs from the request's expectations on
// either half. It is conflict class, bound to `generation` (§11's
// StaleEntryBindingGeneration), and it commits nothing — no pair check ever
// changes state.
func hostStaleEntryRefusal(name string, expected, current hostMutationIdentity) error {
	return appwire.StaleEntry(appwire.StaleEntryBindingGeneration, fmt.Sprintf(
		"host %q: the entry moved (expected generation %d, incarnation %q; live generation %d, incarnation %q); re-read the row and retry",
		name, expected.Generation, expected.IncarnationID, current.Generation, current.IncarnationID))
}

// hostMutationReceiptKey builds the scoped key for one mutation's receipt from
// the identity the commit pins.
func hostMutationReceiptKey(mutationID, name string, kind hostMutationKind, identity hostMutationIdentity) string {
	return hostReceiptScopedKey(hostReceiptScope{
		MutationID:    mutationID,
		Name:          name,
		Kind:          kind,
		Generation:    identity.Generation,
		IncarnationID: identity.IncarnationID,
	})
}

// newHostMutationReceipt builds the receipt one committed mutation writes, from
// the entry as the commit left it (the stamped identity for add/update, the
// removed entry for remove). committedAt is the commit instant.
func newHostMutationReceipt(mutationID string, kind hostMutationKind, entry hostreg.Host, committedAt time.Time) HostMutationReceipt {
	return HostMutationReceipt{
		Outcome:       hostReceiptOutcomeCommitted,
		Row:           hostReceiptRowFor(entry),
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
		CommittedAt:   committedAt.UTC().Format(time.RFC3339),
	}
}

// hostReceiptRowFor renders the effective-config row a receipt stores.
func hostReceiptRowFor(entry hostreg.Host) HostMutationReceiptRow {
	return HostMutationReceiptRow{
		Name:       entry.Name,
		Address:    entry.SSH,
		User:       entry.User,
		KeyPath:    entry.KeyPath,
		EvenerPath: entry.EvenerPath,
		ConfigPath: entry.ConfigPath,
		Addr:       entry.Addr,
		Roots:      append([]string(nil), entry.Roots...),
	}
}

// hostReceiptRow renders a receipt's recorded row as the wire row a replay
// returns: the effective configuration, the pinned pair, the one origin marker,
// and the removed discriminator a remove's row carries. Live state is
// deliberately absent (attached false, midEnsure/midAttach false): the row a
// replay renders is the durable outcome, not a later attach's state.
func hostReceiptRow(receipt HostMutationReceipt, kind hostMutationKind) appwire.HostRow {
	return appwire.HostRow{
		Name:          receipt.Row.Name,
		Address:       receipt.Row.Address,
		User:          receipt.Row.User,
		KeyPath:       receipt.Row.KeyPath,
		EvenerPath:    receipt.Row.EvenerPath,
		ConfigPath:    receipt.Row.ConfigPath,
		Addr:          receipt.Row.Addr,
		Roots:         append([]string(nil), receipt.Row.Roots...),
		Origin:        hostOriginHubTOML,
		Generation:    receipt.Generation,
		IncarnationID: receipt.IncarnationID,
		Removed:       kind == hostMutationRemove,
	}
}

// validateHostMutationReceipts checks the receipts a hub.toml document carries
// against the record shape this build writes: every key is canonical and
// complete, the record's own fields agree with the key (name, pair), the
// outcome is one this build can construct, the incarnation id is within the
// shared bound, and committed_at is an RFC3339 instant. Spec §6's reserved
// namespace refuses "a reserved value whose shape this build cannot decode"
// loudly before any rewrite, and forward preservation is explicitly not
// offered — so anything a later slice's record might add is refused here until
// this build learns it.
func validateHostMutationReceipts(receipts map[string]HostMutationReceipt) error {
	for key, receipt := range receipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			return fmt.Errorf("mutation_receipts[%q] is not a canonical five-part scoped key this build writes (mutation id / name / kind / generation / incarnation id)", key)
		}
		if !validHostMutationID(scope.MutationID) {
			return fmt.Errorf("mutation_receipts[%q] carries a mutation id of %d bytes, over the %d-byte bound (or empty)",
				key, len(scope.MutationID), MaxHostMutationIDBytes)
		}
		switch receipt.Outcome {
		case hostReceiptOutcomeCommitted, hostReceiptOutcomeTeardownFailure:
			if receipt.DroppedEntry != nil || receipt.WinningFingerprint != "" || receipt.Removed {
				return fmt.Errorf("mutation_receipts[%q] carries a collision-dropped field on outcome %q", key, receipt.Outcome)
			}
		case hostReceiptOutcomeCollisionDropped:
			if receipt.DroppedEntry == nil {
				return fmt.Errorf("mutation_receipts[%q] carries outcome %q with no dropped_entry", key, receipt.Outcome)
			}
			if strings.TrimSpace(receipt.WinningFingerprint) == "" {
				return fmt.Errorf("mutation_receipts[%q] carries outcome %q with no winning_fingerprint", key, receipt.Outcome)
			}
			if receipt.Removed && receipt.Row.Name != "" {
				return fmt.Errorf("mutation_receipts[%q] carries the hand-edit-deletion arm with a row", key)
			}
		default:
			return fmt.Errorf("mutation_receipts[%q] carries outcome %q, which this build cannot produce or decode", key, receipt.Outcome)
		}
		if receipt.RemnantID != "" && !validRemnantID(receipt.RemnantID) {
			return fmt.Errorf("mutation_receipts[%q] carries a remnant id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				key, len(receipt.RemnantID), MaxRemnantIDBytes)
		}
		if receipt.RemnantResolvedAt != "" {
			if _, err := time.Parse(time.RFC3339, receipt.RemnantResolvedAt); err != nil {
				return fmt.Errorf("mutation_receipts[%q] carries remnant_resolved_at %q, not an RFC3339 instant: %w", key, receipt.RemnantResolvedAt, err)
			}
			if receipt.RemnantID == "" {
				return fmt.Errorf("mutation_receipts[%q] records a resolved remnant with no remnant_id", key)
			}
		}
		if receipt.RecoveryAttestation != nil {
			if err := validateRecoveryAttestationShape(*receipt.RecoveryAttestation); err != nil {
				return fmt.Errorf("mutation_receipts[%q]: %w", key, err)
			}
			if receipt.RemnantResolvedAt == "" {
				return fmt.Errorf("mutation_receipts[%q] carries a recovery attestation with no remnant_resolved_at", key)
			}
		}
		if receipt.Generation != scope.Generation {
			return fmt.Errorf("mutation_receipts[%q] pins generation %d while the record carries %d", key, scope.Generation, receipt.Generation)
		}
		if receipt.IncarnationID != scope.IncarnationID {
			return fmt.Errorf("mutation_receipts[%q] pins incarnation %q while the record carries %q", key, scope.IncarnationID, receipt.IncarnationID)
		}
		if !validIncarnationID(receipt.IncarnationID) {
			return fmt.Errorf("mutation_receipts[%q] carries an incarnation id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				key, len(receipt.IncarnationID), hostops.MaxIncarnationIDBytes)
		}
		if receipt.Row.Name == "" {
			if receipt.Outcome != hostReceiptOutcomeCollisionDropped || !receipt.Removed {
				return fmt.Errorf("mutation_receipts[%q] carries no row name", key)
			}
		} else if receipt.Row.Name != scope.Name {
			return fmt.Errorf("mutation_receipts[%q] names host %q in its key and %q in its row", key, scope.Name, receipt.Row.Name)
		}
		if dropped := receipt.DroppedEntry; dropped != nil && dropped.Name != scope.Name {
			return fmt.Errorf("mutation_receipts[%q] names host %q in its key and drops an entry for %q", key, scope.Name, dropped.Name)
		}
		if _, err := time.Parse(time.RFC3339, receipt.CommittedAt); err != nil {
			return fmt.Errorf("mutation_receipts[%q] carries committed_at %q, not an RFC3339 instant: %w", key, receipt.CommittedAt, err)
		}
	}
	return nil
}

// hostReceiptQuery is one dedup lookup's inputs (spec §5).
type hostReceiptQuery struct {
	MutationID string
	Name       string
	Kind       hostMutationKind
	// Current is name's current (generation, incarnation id) pair, and
	// CurrentKnown reports whether the name has one at all — absent for a name
	// with no live entry and no retained high-water record.
	Current      hostMutationIdentity
	CurrentKnown bool
}

// hostMutationIdentity is one (generation, incarnation id) pair.
type hostMutationIdentity struct {
	Generation    uint64
	IncarnationID string
}

// hostReceiptHit is a dedup lookup's result: a receipt matched for replay.
// Direct means the receipt is pinned to the name's current pair — a
// current-generation hit; a non-direct hit is superseded (another generation or
// incarnation) and is returned "for recovery only, never authorizing work".
type hostReceiptHit struct {
	Receipt HostMutationReceipt
	Scope   hostReceiptScope
	Direct  bool
}

// lookupHostMutationReceipt answers §5's dedup check over one keyed mutation:
//
//   - the direct hit — same name, kind, mutationId, and the name's current
//     (generation, incarnation id) pair — returns the recorded receipt;
//   - a mutationId colliding with a current-generation receipt of a different
//     name or kind is refused with the typed conflicting-mutation-id error;
//   - the superseded hit — the same name, kind, and mutationId pinned to
//     another generation or incarnation — returns the recorded outcome for
//     recovery only, never authorizing work;
//   - a mutationId whose key survives only as a pruned marker — the receipt
//     was compacted by the count/TTL bound or dropped with its tombstone — is
//     refused as the typed `stale-entry` (pruned-generation): the marker is
//     the backstop that keeps a same-key replay from fresh-applying against a
//     re-added incarnation (spec §6: "a replay naming a marked key refuses as
//     `stale-entry` (pruned-generation); a key with neither a retained receipt
//     nor a pruned marker commits fresh by the commits-fresh clause");
//   - no match returns nil, and the caller may open fresh.
//
// The "current pair" of a receipt's own name is the registry's live pair, or
// the retained high-water pair of a removed incarnation; a name with neither is
// history, never a collision (the operation store's mirrored-boundary rule,
// applied to mutations — §16's mutation-idempotency bullet).
//
// It is a read: nothing is consumed. It takes no mutation lock and no host
// gate, because §5 orders dedup before both — a replay returns its receipt
// without consulting the gate.
func (m *hubHostManager) lookupHostMutationReceipt(q hostReceiptQuery) (*hostReceiptHit, error) {
	receipts := m.cfg.store.receiptsSnapshot()
	var sameKind []struct {
		scope   hostReceiptScope
		receipt HostMutationReceipt
	}
	for key, receipt := range receipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok || scope.MutationID != q.MutationID {
			// A key this build cannot parse cannot exist in a loaded file
			// (validateHostMutationReceipts refuses it at boot), so this is
			// defensive only.
			continue
		}
		if scope.Name != q.Name || scope.Kind != q.Kind {
			continue
		}
		if q.CurrentKnown && scope.Generation == q.Current.Generation && scope.IncarnationID == q.Current.IncarnationID {
			return &hostReceiptHit{Receipt: receipt, Scope: scope, Direct: true}, nil
		}
		sameKind = append(sameKind, struct {
			scope   hostReceiptScope
			receipt HostMutationReceipt
		}{scope: scope, receipt: receipt})
	}
	// No direct hit: a same-key receipt of another name or kind that is still
	// current for its own name is a collision, never a hit and never a fresh
	// apply under the colliding key.
	for key := range receipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok || scope.MutationID != q.MutationID {
			continue
		}
		if scope.Name == q.Name && scope.Kind == q.Kind {
			continue
		}
		otherCurrent, known := m.currentHostIdentity(scope.Name)
		if known && otherCurrent.Generation == scope.Generation && otherCurrent.IncarnationID == scope.IncarnationID {
			return nil, appwire.ConflictingMutationID(fmt.Sprintf(
				"host %q: mutationId %q is already used by a %s of host %q", q.Name, q.MutationID, scope.Kind, scope.Name))
		}
	}
	if len(sameKind) > 0 {
		// The newest retained receipt for the key is the replay's outcome. This
		// build drops no receipt but an un-commit's, so in practice one exists;
		// generation first and the scoped key as the tie-break keep the arm
		// deterministic even for a state no writer of this build can produce.
		newest := sameKind[0]
		for _, candidate := range sameKind[1:] {
			candidateKey := hostReceiptScopedKey(candidate.scope)
			newestKey := hostReceiptScopedKey(newest.scope)
			if candidate.scope.Generation > newest.scope.Generation ||
				(candidate.scope.Generation == newest.scope.Generation && candidateKey < newestKey) {
				newest = candidate
			}
		}
		return &hostReceiptHit{Receipt: newest.receipt, Scope: newest.scope}, nil
	}
	// No retained receipt survives for the key: a pruned marker naming it is
	// the backstop refusal, never a fresh apply. The marker's scope names the
	// mutation the receipt belonged to; a replay is the same mutation id, name,
	// and kind — the generation the marker pins is exactly the generation the
	// replay must not act on, and the client's (expectedGeneration,
	// expectedIncarnationId) pair cannot override it.
	if marked, ok := m.cfg.store.prunedMarkerFor(q.MutationID, q.Name, q.Kind); ok {
		return nil, appwire.StaleEntry(appwire.StaleEntryBindingPrunedGeneration, fmt.Sprintf(
			"host %q: mutation %q's %s receipt for generation %d (incarnation %q) was pruned; the mutation must not re-apply — mint a fresh mutationId",
			q.Name, q.MutationID, q.Kind, marked.Generation, marked.IncarnationID))
	}
	return nil, nil
}

// prunedMarkerFor reports whether a pruned marker survives for one
// (mutationId, name, kind) triple — the identity half of the five-part scoped
// key — returning the scope it pins. Marker maps are small (bounded per name)
// and the lookup is a read, so it takes only the store's own mutex.
func (s *hostStore) prunedMarkerFor(mutationID, name string, kind hostMutationKind) (hostReceiptScope, bool) {
	for key := range s.prunedReceiptSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			continue
		}
		if scope.MutationID == mutationID && scope.Name == name && scope.Kind == kind {
			return scope, true
		}
	}
	return hostReceiptScope{}, false
}

// currentHostIdentity returns name's current (generation, incarnation id) pair:
// the live entry's pair when the name is live, else the retained high-water
// record's pair — the removed incarnation the name's receipts pin — and false
// when the name has neither. It takes no mutation mutex: the pair is read from
// the registry and the store's own snapshot.
func (m *hubHostManager) currentHostIdentity(name string) (hostMutationIdentity, bool) {
	if host, ok := m.cfg.hosts.Get(name); ok && host.Generation != 0 && host.IncarnationID != "" {
		return hostMutationIdentity{Generation: host.Generation, IncarnationID: host.IncarnationID}, true
	}
	if mark, ok := m.cfg.store.highWaterSnapshot()[name]; ok && mark.Generation != 0 && validIncarnationID(mark.IncarnationID) {
		return hostMutationIdentity{Generation: mark.Generation, IncarnationID: mark.IncarnationID}, true
	}
	return hostMutationIdentity{}, false
}
