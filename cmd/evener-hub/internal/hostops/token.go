package hostops

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Confirmation tokens (spec 08b §3) are minted by `plan` and consumed by
// `deploy`: an opaque nonce bound to the host, the registry pair, the resolved
// entry and target, the refreshed facts, and the probed running state, with one
// deadline covering both its TTL and the freshness of the facts it was built
// from.
//
// This file owns the durable half: the row schema (in the operation-store file,
// under the same atomic temp-plus-rename-plus-fsync writes as every other store
// write, per §3's storage paragraph), the mint write's supersede-on-mint rule,
// the validate/consume pass under the store mutex, the lazy and boot reaps, and
// the wall-clock rollback guard. Deploy's gate, probe, dedup and receipt paths
// are other slices' and are never restated here.

const (
	// DefaultTokenTTL is §3's default confirmation-token TTL: five minutes,
	// owner-adjustable.
	DefaultTokenTTL = 5 * time.Minute
	// DefaultFreshnessBound is §3's default bound on the age of the preflight
	// facts a plan was built from: five minutes, owner-adjustable.
	DefaultFreshnessBound = 5 * time.Minute
	// minTokenValueChars is §3's floor on the token's wire form: an opaque
	// base64url string of at least 32 characters (≈192 bits). The mint emits
	// exactly this many characters.
	minTokenValueChars = 32
	// tokenNonceBytes is the nonce behind that form: 24 bytes, 192 bits, drawn
	// from the CSPRNG — well past §3's 128-bit floor.
	tokenNonceBytes = 24
	// hashHexChars is the canonical digest shape the binding fields carry: the
	// lowercase hex form of a SHA-256 digest.
	hashHexChars = 64
)

// StaleBinding names the token binding a drift refusal fired on, spelled the way
// spec §11's `stale-entry` data names it, so the wire layer can carry the value
// straight through.
type StaleBinding string

const (
	// StaleBindingEntry is the resolved host entry drifting from the token.
	StaleBindingEntry StaleBinding = "entry"
	// StaleBindingTarget is the resolved deploy target drifting.
	StaleBindingTarget StaleBinding = "target"
	// StaleBindingGeneration is the registry's (generation, incarnation id) pair
	// advancing past the minted one. The pair's two halves are one identity
	// (§3: "Generation and incarnation bind like every other binding"), so a
	// difference in either is the registry pair advancing, which §11 spells
	// `generation`.
	StaleBindingGeneration StaleBinding = "generation"
	// StaleBindingHubTOMLFingerprint is the host's own hub.toml entry
	// fingerprint drifting from the token-bound one.
	StaleBindingHubTOMLFingerprint StaleBinding = "hub.toml-fingerprint"
)

// StaleEntryError reports a token whose binding no longer matches the values a
// caller re-resolved. It is §11's `stale-entry` refusal with the binding named.
type StaleEntryError struct {
	Binding StaleBinding
}

func (e *StaleEntryError) Error() string {
	return "hostops: stale entry: the token's " + string(e.Binding) + " binding drifted"
}

// Token is one stored confirmation-token row: the opaque value plus every
// binding §3 lists, the effective freshness bound in force at mint, and the
// minted deadline. `FreshnessBoundSec` is the bound deploy compares the
// token-bound facts age against — deploy never re-reads the owner knob.
type Token struct {
	Host               string    `json:"host"`
	Value              string    `json:"value"`
	Generation         uint64    `json:"generation"`
	IncarnationID      string    `json:"incarnationId"`
	EntryHash          string    `json:"entryHash"`
	HubTOMLFingerprint string    `json:"hubTomlFingerprint"`
	FactsRevision      string    `json:"factsRevision"`
	FactsCapturedAt    time.Time `json:"factsCapturedAt"`
	TargetPath         string    `json:"targetPath"`
	ControllerRevision string    `json:"controllerRevision"`
	// RunningVersion is the probed running revision. It is legitimately empty on
	// a host where nothing runs, which is a value, not a missing field.
	RunningVersion string `json:"runningVersion"`
	RunningHealthy bool   `json:"runningHealthy"`
	// ProcessStartTime is the probed process start time when the probe carried
	// one; absent when it did not.
	ProcessStartTime *time.Time `json:"processStartTime,omitempty"`
	// FreshnessBoundSec is the effective freshness bound in force at mint, in
	// seconds.
	FreshnessBoundSec int64     `json:"freshnessBoundSec"`
	MintedAt          time.Time `json:"mintedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

// MintRequest is the caller-supplied half of a token: every binding, plus the
// configured TTL and the effective freshness bound. A TTL or bound that is not
// positive takes the spec's default.
type MintRequest struct {
	Host               string
	Generation         uint64
	IncarnationID      string
	EntryHash          string
	HubTOMLFingerprint string
	FactsRevision      string
	FactsCapturedAt    time.Time
	TargetPath         string
	ControllerRevision string
	RunningVersion     string
	RunningHealthy     bool
	ProcessStartTime   *time.Time
	FreshnessBound     time.Duration
	TTL                time.Duration
}

// TokenExpectation is the caller's freshly re-resolved half of §6 step 3's
// binding comparison. A zero field is not checked, so a caller compares only
// what it re-resolved.
type TokenExpectation struct {
	Generation         uint64
	IncarnationID      string
	EntryHash          string
	HubTOMLFingerprint string
	TargetPath         string
}

// ErrTokenMissing reports a validate/consume pass that found no row for the
// requested host: nothing was minted, or the row is gone (consumed, superseded,
// revoked, or reaped).
var ErrTokenMissing = errors.New("hostops: confirmation token not found")

// ErrTokenMismatched reports a presented value that is not a token of this store
// at all: not in the wire's token shape, or a live token bound to another host.
var ErrTokenMismatched = errors.New("hostops: confirmation token does not match")

// ErrTokenSuperseded reports a value that was a token for the host but is no
// longer the host's current one: a later mint replaced the nonce, and §3's
// supersede-on-mint write deleted the row.
var ErrTokenSuperseded = errors.New("hostops: confirmation token was superseded")

// ErrTokenExpired reports a token whose deadline the store has observed passing,
// or a row whose own capture timestamp postdates the durable wall-clock mark.
var ErrTokenExpired = errors.New("hostops: confirmation token expired")

// ErrFactsStale reports a mint whose facts were already past their bound at mint
// time, or a facts capture that postdates the durable wall-clock mark. §3:
// "stale facts at mint read as a refresh failure, and re-planning refreshes
// them — never an already-expired token".
var ErrFactsStale = errors.New("hostops: facts are stale")

// ErrInvalidToken reports a mint request or a stored row outside the schema
// every writer of this store produces.
var ErrInvalidToken = errors.New("hostops: invalid confirmation token")

// MintToken mints a confirmation token for req.Host and persists it in one
// atomic store write, superseding the host's earlier unconsumed row in that same
// write (§3: "the same atomic store write that persists the new token deletes
// the host's earlier unconsumed token rows").
//
// The deadline is §3's formula: expiresAt = min(mintTime + configuredTTL,
// factsCapturedAt + freshnessBound), where mintTime is the wall-clock anchor —
// `max(now, mark)`, so a capture taken while a rollback is active anchors at the
// mark rather than at the rewound clock. An owner-set TTL above the bound clamps
// through the formula itself. A mint whose facts are already past their bound,
// or whose capture postdates the mark, refuses with ErrFactsStale and writes
// nothing.
//
// The write also advances the durable wall-clock high-water mark to the anchor,
// so the mark never moves backward across captures and refusals.
//
// A request outside the schema (empty host, zero generation, a binding that is
// not a canonical digest, a sub-second bound) refuses with ErrInvalidToken. A
// failure before the rename wrote nothing; a post-rename failure returned with
// RenameLanded means the token is the durable row (see RenameLanded).
func (s *Store) MintToken(req MintRequest) (Token, error) {
	if s == nil {
		return Token{}, errors.New("hostops: store is not configured")
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	bound := req.FreshnessBound
	if bound <= 0 {
		bound = DefaultFreshnessBound
	}
	if bound < time.Second {
		return Token{}, fmt.Errorf("%w: freshness bound %s is below the whole-second store unit", ErrInvalidToken, bound)
	}

	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	now := s.now()
	next := cloneSnapshot(s.cell.state)
	mark := wallClockMark(next.WallClockHighWaterMark)
	anchor := later(now, mark)
	// The request's bindings are checked before anything else the mint decides,
	// so an unusable request reads as one no matter what the clock or the facts
	// say (and no nonce is drawn for a request this store could never persist).
	row := Token{
		Host:               req.Host,
		Generation:         req.Generation,
		IncarnationID:      req.IncarnationID,
		EntryHash:          req.EntryHash,
		HubTOMLFingerprint: req.HubTOMLFingerprint,
		FactsRevision:      req.FactsRevision,
		FactsCapturedAt:    req.FactsCapturedAt.UTC(),
		TargetPath:         req.TargetPath,
		ControllerRevision: req.ControllerRevision,
		RunningVersion:     req.RunningVersion,
		RunningHealthy:     req.RunningHealthy,
		ProcessStartTime:   utcPointer(req.ProcessStartTime),
	}
	if err := validateTokenBindings(row); err != nil {
		return Token{}, err
	}
	// §3's freshness rule at mint: facts whose bound is exhausted refuse without
	// minting, and a capture that postdates the mark cannot be honest — it reads
	// stale, never as a fresh capture that resets the clock.
	if req.FactsCapturedAt.After(anchor) || !anchor.Before(req.FactsCapturedAt.Add(bound)) {
		return Token{}, fmt.Errorf("%w: facts captured at %s are past their %s bound at %s",
			ErrFactsStale, req.FactsCapturedAt.Format(time.RFC3339), bound, anchor.Format(time.RFC3339))
	}

	value, err := newTokenValue()
	if err != nil {
		return Token{}, err
	}
	row.Value = value
	row.FreshnessBoundSec = int64(bound / time.Second)
	row.MintedAt = anchor.UTC()
	// Both terms are normalized to UTC before the comparison, so the persisted
	// deadline carries the store's own `Z`-suffixed form (§8's write invariant)
	// even when a caller hands its facts across in a local-zone spelling.
	row.ExpiresAt = earlier(anchor.Add(ttl).UTC(), row.FactsCapturedAt.Add(bound).UTC())
	if err := validateTokenRow(row); err != nil {
		return Token{}, err
	}
	next.WallClockHighWaterMark = anchor
	next.Tokens = append(dropTokenHost(next.Tokens, req.Host), row)
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		// Nothing was written: the refusal reports no token.
		return Token{}, err
	}
	// See Create: a landed rename is this token's durable row even when the
	// directory sync behind it failed.
	return cloneToken(row), err
}

// ValidateToken classifies a presented token for host without consuming it. It
// is §6 step 2's fail-fast readability check: nothing is decided here, and a
// concurrent plan can mint a newer token and supersede this one before any gate
// is acquired.
//
// The classification is §6 step 4's set. A value outside the wire's token shape,
// or a live token bound to another host, is ErrTokenMismatched. A value no row
// of the store carries is ErrTokenMissing, unless the host holds a row carrying
// a different value — then the nonce changed and it is ErrTokenSuperseded. A
// matching row whose deadline the store has observed passing, or whose own
// capture timestamp postdates the durable mark, is ErrTokenExpired.
//
// The pass reaps every expired row of the requested host (§3: "expired tokens
// reap lazily on any validate/consume pass for that host") and advances the
// durable wall-clock mark, both in one atomic write; a pass that reaps nothing
// and observes no later time writes nothing.
func (s *Store) ValidateToken(host, value string) (Token, error) {
	return s.tokenPass(host, value, false)
}

// ConsumeToken classifies a presented token for host and, on a match, deletes
// its row in the same atomic write. Consume is delete, never a mark (§6 step 4),
// so a consumed token presented again reads ErrTokenMissing: gone rows never
// validate.
func (s *Store) ConsumeToken(host, value string) (Token, error) {
	return s.tokenPass(host, value, true)
}

// tokenPass runs one validate/consume pass under the store mutex: it classifies
// the presented value against the store's rows, reaps the requested host's
// expired rows, advances the durable wall-clock mark, and — for a consume that
// matched — deletes the consumed row. Classification and every mutation it
// implies land in one atomic write, so a crash between them cannot leave a
// consumed row behind or reap a row the pass accepted.
func (s *Store) tokenPass(host, value string, consume bool) (Token, error) {
	if s == nil {
		return Token{}, errors.New("hostops: store is not configured")
	}
	if host == "" || !utf8.ValidString(host) {
		return Token{}, fmt.Errorf("%w: a token pass needs a host name", ErrInvalidToken)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	mark := wallClockMark(next.WallClockHighWaterMark)
	now := s.now()
	effectiveNow := later(now, mark)

	matched, passErr := classifyToken(next.Tokens, host, value, effectiveNow, mark)
	if passErr == nil && consume {
		// Consume is delete in the same write that carries the reap and the mark.
		next.Tokens = dropTokenValue(next.Tokens, value)
	}
	reaped := false
	kept := make([]Token, 0, len(next.Tokens))
	for _, row := range next.Tokens {
		if row.Host == host && row.expired(effectiveNow, mark) {
			reaped = true
			continue
		}
		kept = append(kept, row)
	}
	next.Tokens = kept

	changed := reaped || now.After(mark)
	if now.After(mark) {
		// §3: the mark is the greatest observed wall-clock value, and it never
		// moves backward. A rollback (now behind the mark) is therefore not a
		// capture: it advances nothing.
		next.WallClockHighWaterMark = now
	}
	if changed || (consume && passErr == nil) {
		landed, err := s.commitLocked(next)
		if err != nil && !landed {
			// Nothing was written: the pass reports the refusal it classified
			// against the state the store still holds.
			return Token{}, err
		}
		// The write landed (see RenameLanded): the row the pass consumed is gone
		// durably and the reaps it applied are the store's state. A post-rename
		// sync failure is reported alongside the classification — a caller that
		// must not act on an unproven write reconciles through RenameLanded, and
		// a refusal keeps its discriminator with the write trouble joined on.
		switch {
		case passErr != nil && err != nil:
			return Token{}, errors.Join(passErr, err)
		case passErr != nil:
			return Token{}, passErr
		default:
			return matched, err
		}
	}
	return matched, passErr
}

// classifyToken is the discriminator set of §6 step 4 applied to the store's
// rows, with no state mutation: a value outside the token shape and a live token
// of another host are mismatches, a value no row carries is missing (or
// superseded when the host holds a newer nonce), and a matching row that has
// expired — or whose own capture postdates the mark (§3's corrupt-row arm) — is
// expired.
func classifyToken(rows []Token, host, value string, effectiveNow, mark time.Time) (Token, error) {
	if !validTokenValue(value) {
		return Token{}, fmt.Errorf("%w: the presented value is not a confirmation token", ErrTokenMismatched)
	}
	for _, row := range rows {
		// §3: "Validate and consume compare with constant-time equality." The
		// value is an opaque bearer, so the comparison never branches on where
		// two values first differ.
		if subtle.ConstantTimeCompare([]byte(row.Value), []byte(value)) != 1 {
			continue
		}
		if row.Host != host {
			return Token{}, fmt.Errorf("%w: the token is bound to %q, not %q", ErrTokenMismatched, row.Host, host)
		}
		if row.expired(effectiveNow, mark) {
			return Token{}, fmt.Errorf("%w: the token's deadline was %s", ErrTokenExpired, row.ExpiresAt.Format(time.RFC3339))
		}
		return row, nil
	}
	for _, row := range rows {
		if row.Host == host {
			// The host holds a current token and its nonce is not the presented
			// one: a later mint superseded this value.
			return Token{}, fmt.Errorf("%w: the host holds a newer token", ErrTokenSuperseded)
		}
	}
	return Token{}, fmt.Errorf("%w: no token for %q", ErrTokenMissing, host)
}

// OutstandingToken returns the host's current token row, consumed or not. It is
// what a caller publishes as the name's outstanding token and what the deploy
// slice's step-(3) revalidation re-reads.
func (s *Store) OutstandingToken(host string) (Token, bool) {
	if s == nil {
		return Token{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	for _, row := range s.cell.state.Tokens {
		if row.Host == host {
			return cloneToken(row), true
		}
	}
	return Token{}, false
}

// RevokeTokens drops every outstanding token row for host in one atomic write —
// the durable half of §3's "live `remove` revokes every outstanding token row
// for the name". A name with no row is a no-op, and a call that drops nothing
// writes nothing. A re-added host starts with zero valid tokens: nothing here
// revives a revoked one.
func (s *Store) RevokeTokens(host string) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	if host == "" || !utf8.ValidString(host) {
		return fmt.Errorf("%w: a revocation needs a host name", ErrInvalidToken)
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	kept := dropTokenHost(next.Tokens, host)
	if len(kept) == len(next.Tokens) {
		return nil
	}
	next.Tokens = kept
	if _, err := s.commitLocked(next); err != nil {
		return err
	}
	return nil
}

// ReapExpiredTokens drops every row of every host whose deadline the store's
// clock has observed passing — §3's boot reap, and the same pass the lazy reaps
// run for one host. It returns how many rows it dropped, for the boot log.
//
// Like every token pass it records what it observed: when the clock has moved
// past the durable mark the reap persists the mark in the same atomic write,
// whether or not it dropped a row, so a later rollback can never present a
// rewound clock as new time. A pass whose clock has not advanced and which drops
// nothing writes nothing.
func (s *Store) ReapExpiredTokens() (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	now := s.now()
	next := cloneSnapshot(s.cell.state)
	mark := wallClockMark(next.WallClockHighWaterMark)
	effectiveNow := later(now, mark)
	kept := make([]Token, 0, len(next.Tokens))
	for _, row := range next.Tokens {
		if row.expired(effectiveNow, mark) {
			continue
		}
		kept = append(kept, row)
	}
	reaped := len(next.Tokens) - len(kept)
	if reaped == 0 && !now.After(mark) {
		return 0, nil
	}
	if now.After(mark) {
		// §3: "the mark is the greatest observed wall-clock value" — the reap
		// observes the clock like every other pass, so it advances the mark even
		// when it reaped nothing.
		next.WallClockHighWaterMark = now
	}
	next.Tokens = kept
	landed, err := s.commitLocked(next)
	if err != nil && !landed {
		return 0, err
	}
	return reaped, err
}

// CheckTokenBindings compares a token's bindings against the values a caller
// re-resolved, and reports §11's `stale-entry` refusal naming the half that
// drifted. A zero field of expect is not checked, so a caller compares only what
// it actually re-resolved; an empty expectation is always satisfied.
//
// The check is a check, never a consumption: it does not touch the store, so a
// caller can run it under its gate and hand a refusal back with the token
// unconsumed.
func CheckTokenBindings(token Token, expect TokenExpectation) error {
	if (expect.Generation != 0 && token.Generation != expect.Generation) ||
		(expect.IncarnationID != "" && token.IncarnationID != expect.IncarnationID) {
		return &StaleEntryError{Binding: StaleBindingGeneration}
	}
	if expect.EntryHash != "" && token.EntryHash != expect.EntryHash {
		return &StaleEntryError{Binding: StaleBindingEntry}
	}
	if expect.HubTOMLFingerprint != "" && token.HubTOMLFingerprint != expect.HubTOMLFingerprint {
		return &StaleEntryError{Binding: StaleBindingHubTOMLFingerprint}
	}
	if expect.TargetPath != "" && token.TargetPath != expect.TargetPath {
		return &StaleEntryError{Binding: StaleBindingTarget}
	}
	return nil
}

// expired applies §3's one algorithm: a record invalidates only when its own
// timestamp postdates the mark — impossible for honest captures, so the arm
// covers corrupt rows only — and a deadline at or before the effective now has
// passed. Every expiresAt comparison substitutes max(now, mark) while a rollback
// is active, so a pre-rollback token keeps exactly the real-time lifetime its
// persisted deadline granted, and one already expired stays expired.
func (t Token) expired(effectiveNow, mark time.Time) bool {
	return t.MintedAt.After(mark) || !t.ExpiresAt.After(effectiveNow)
}

// wallClockMark normalizes the durable mark: everything before the Unix epoch is
// not an observed wall clock, so it reads as "no mark yet". That keeps the
// writer's own zero-mark value round-tripping (the zero Time marshals as
// "0001-01-01T00:00:00Z", which parses back as a year-one instant, not as the
// zero Time) without special-casing the string form.
func wallClockMark(mark time.Time) time.Time {
	mark = mark.UTC()
	if mark.Before(unixEpoch) {
		return time.Time{}
	}
	return mark
}

// unixEpoch is the earliest wall-clock value a rollback guard treats as
// observed.
var unixEpoch = time.Unix(0, 0).UTC()

// later and earlier are the timestamp comparisons §3's formula is stated in.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// newTokenValue draws one token value: a fresh CSPRNG nonce in the wire's
// base64url form (§3: "unique per mint", at least 128 bits of entropy).
func newTokenValue() (string, error) {
	nonce := make([]byte, tokenNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("hostops: draw token nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(nonce), nil
}

// validTokenValue reports whether value is in the token wire shape: the
// canonical base64url form of a nonce, at the mint's own length. A value of any
// other shape is not a token this store minted, so a pass refuses it as a
// mismatch rather than searching rows for it.
func validTokenValue(value string) bool {
	if len(value) != minTokenValueChars {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(nonce) != tokenNonceBytes {
		return false
	}
	return base64.RawURLEncoding.EncodeToString(nonce) == value
}

// utcPointer copies an optional timestamp into UTC, so a caller's offset form
// never lands in the file (spec §8 normalizes stored timestamps).
func utcPointer(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	utc := at.UTC()
	return &utc
}

// dropTokenHost returns rows without every row bound to host.
func dropTokenHost(rows []Token, host string) []Token {
	kept := make([]Token, 0, len(rows))
	for _, row := range rows {
		if row.Host != host {
			kept = append(kept, row)
		}
	}
	return kept
}

// dropTokenValue returns rows without the row carrying value.
func dropTokenValue(rows []Token, value string) []Token {
	kept := make([]Token, 0, len(rows))
	for _, row := range rows {
		if row.Value != value {
			kept = append(kept, row)
		}
	}
	return kept
}

// cloneToken copies one row deeply: ProcessStartTime is the row's only pointer,
// and a caller holding the copy must not reach into store state through it.
func cloneToken(row Token) Token {
	out := row
	out.ProcessStartTime = utcPointer(row.ProcessStartTime)
	return out
}

// cloneTokens copies a row set deeply.
func cloneTokens(rows []Token) []Token {
	if rows == nil {
		return nil
	}
	out := make([]Token, len(rows))
	for i, row := range rows {
		out[i] = cloneToken(row)
	}
	return out
}

// validateTokenBindings checks the binding fields a token carries, whether the
// value is a mint request's proposed row or a row read back from the file: the
// fields both halves share, so a request and a stored row are held to one
// schema.
func validateTokenBindings(row Token) error {
	if row.Host == "" {
		return fmt.Errorf("%w: a token row names no host", ErrInvalidToken)
	}
	if !utf8.ValidString(row.Host) {
		return fmt.Errorf("%w: token row host is not valid UTF-8", ErrInvalidToken)
	}
	if row.Generation == 0 {
		return fmt.Errorf("%w: token for %q pins no generation", ErrInvalidToken, row.Host)
	}
	if row.IncarnationID == "" || len(row.IncarnationID) > MaxIncarnationIDBytes || !utf8.ValidString(row.IncarnationID) {
		return fmt.Errorf("%w: token for %q pins an unusable incarnation id", ErrInvalidToken, row.Host)
	}
	for _, digest := range []struct {
		value string
		what  string
	}{
		{row.EntryHash, "entry hash"},
		{row.HubTOMLFingerprint, "hub.toml fingerprint"},
		{row.FactsRevision, "facts revision"},
	} {
		if !validDigest(digest.value) {
			return fmt.Errorf("%w: token for %q carries a %s outside the canonical digest shape", ErrInvalidToken, row.Host, digest.what)
		}
	}
	if row.FactsCapturedAt.IsZero() {
		return fmt.Errorf("%w: token for %q captures no facts time", ErrInvalidToken, row.Host)
	}
	if row.TargetPath == "" {
		return fmt.Errorf("%w: token for %q names no target path", ErrInvalidToken, row.Host)
	}
	if row.ControllerRevision == "" {
		return fmt.Errorf("%w: token for %q pins no controller revision", ErrInvalidToken, row.Host)
	}
	for _, text := range []struct {
		value string
		what  string
	}{
		{row.TargetPath, "target path"},
		{row.ControllerRevision, "controller revision"},
		{row.RunningVersion, "running version"},
	} {
		if !utf8.ValidString(text.value) {
			return fmt.Errorf("%w: token for %q carries a %s that is not valid UTF-8", ErrInvalidToken, row.Host, text.what)
		}
	}
	if row.ProcessStartTime != nil && row.ProcessStartTime.IsZero() {
		return fmt.Errorf("%w: token for %q carries a zero process start time", ErrInvalidToken, row.Host)
	}
	return nil
}

// validateTokenRow checks one complete row against the schema every writer of
// this store produces. The rules are the refuse-always kind: a value no mint
// path can emit never enters the file, so the boot and pass paths never have to
// guess what a malformed row meant. What is deliberately not a schema rule here
// is where a row's capture timestamps sit relative to the durable mark: §3 makes
// that the read path's arm (a row whose timestamp postdates the mark reads
// expired), not a load refusal.
func validateTokenRow(row Token) error {
	if err := validateTokenBindings(row); err != nil {
		return err
	}
	if !validTokenValue(row.Value) {
		return fmt.Errorf("%w: token for %q carries a value outside the wire shape", ErrInvalidToken, row.Host)
	}
	if row.MintedAt.IsZero() || row.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: token for %q carries no timestamps", ErrInvalidToken, row.Host)
	}
	if row.FactsCapturedAt.After(row.MintedAt) {
		return fmt.Errorf("%w: token for %q captured facts after its own mint", ErrInvalidToken, row.Host)
	}
	if !row.ExpiresAt.After(row.MintedAt) {
		return fmt.Errorf("%w: token for %q expires at or before its own mint", ErrInvalidToken, row.Host)
	}
	if row.FreshnessBoundSec < 1 {
		return fmt.Errorf("%w: token for %q carries no freshness bound", ErrInvalidToken, row.Host)
	}
	return nil
}

// validDigest reports whether value is the canonical digest shape the binding
// fields carry: lowercase hex, SHA-256 wide.
func validDigest(value string) bool {
	if len(value) != hashHexChars {
		return false
	}
	for _, digit := range value {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

// validateTokenRows checks a whole row set: every row's schema, plus the two
// set-level rules the mint write keeps — one row per host name and one row per
// value. Both are facts a hand-edited file can break, and neither has a defined
// reading: two rows for one host would make "the host's current token"
// ambiguous, and two rows sharing a value would let one value validate for two
// hosts.
func validateTokenRows(rows []Token) error {
	hosts := make(map[string]struct{}, len(rows))
	values := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if err := validateTokenRow(row); err != nil {
			return err
		}
		if _, duplicate := hosts[row.Host]; duplicate {
			return fmt.Errorf("%w: more than one token row for %q", ErrInvalidToken, row.Host)
		}
		hosts[row.Host] = struct{}{}
		if _, duplicate := values[row.Value]; duplicate {
			return fmt.Errorf("%w: two token rows carry the same value", ErrInvalidToken)
		}
		values[row.Value] = struct{}{}
	}
	return nil
}

// tokenFile is the decode shape of one token row. Every field is a pointer so a
// row that omits one — or carries null — is refused rather than decoded as a
// zero-valued row, exactly as the record shape does it. ProcessStartTime is the
// one optional field: the probe either carries a process start time or does not.
type tokenFile struct {
	Host               *string    `json:"host"`
	Value              *string    `json:"value"`
	Generation         *uint64    `json:"generation"`
	IncarnationID      *string    `json:"incarnationId"`
	EntryHash          *string    `json:"entryHash"`
	HubTOMLFingerprint *string    `json:"hubTomlFingerprint"`
	FactsRevision      *string    `json:"factsRevision"`
	FactsCapturedAt    *time.Time `json:"factsCapturedAt"`
	TargetPath         *string    `json:"targetPath"`
	ControllerRevision *string    `json:"controllerRevision"`
	RunningVersion     *string    `json:"runningVersion"`
	RunningHealthy     *bool      `json:"runningHealthy"`
	ProcessStartTime   *time.Time `json:"processStartTime"`
	FreshnessBoundSec  *int64     `json:"freshnessBoundSec"`
	MintedAt           *time.Time `json:"mintedAt"`
	ExpiresAt          *time.Time `json:"expiresAt"`
}

// token maps the decode shape to a row, refusing every omitted field.
func (f tokenFile) token() (Token, error) {
	for _, required := range []struct {
		present bool
		what    string
	}{
		{f.Host != nil, "host"},
		{f.Value != nil, "value"},
		{f.Generation != nil, "generation"},
		{f.IncarnationID != nil, "incarnation id"},
		{f.EntryHash != nil, "entry hash"},
		{f.HubTOMLFingerprint != nil, "hub.toml fingerprint"},
		{f.FactsRevision != nil, "facts revision"},
		{f.FactsCapturedAt != nil, "facts capture"},
		{f.TargetPath != nil, "target path"},
		{f.ControllerRevision != nil, "controller revision"},
		{f.RunningVersion != nil, "running version"},
		{f.RunningHealthy != nil, "running-health flag"},
		{f.FreshnessBoundSec != nil, "freshness bound"},
		{f.MintedAt != nil, "mint time"},
		{f.ExpiresAt != nil, "deadline"},
	} {
		if !required.present {
			return Token{}, fmt.Errorf("a token row carries no %s", required.what)
		}
	}
	row := Token{
		Host:               *f.Host,
		Value:              *f.Value,
		Generation:         *f.Generation,
		IncarnationID:      *f.IncarnationID,
		EntryHash:          *f.EntryHash,
		HubTOMLFingerprint: *f.HubTOMLFingerprint,
		FactsRevision:      *f.FactsRevision,
		FactsCapturedAt:    f.FactsCapturedAt.UTC(),
		TargetPath:         *f.TargetPath,
		ControllerRevision: *f.ControllerRevision,
		RunningVersion:     *f.RunningVersion,
		RunningHealthy:     *f.RunningHealthy,
		ProcessStartTime:   utcPointer(f.ProcessStartTime),
		FreshnessBoundSec:  *f.FreshnessBoundSec,
		MintedAt:           f.MintedAt.UTC(),
		ExpiresAt:          f.ExpiresAt.UTC(),
	}
	if err := validateTokenRow(row); err != nil {
		return Token{}, err
	}
	return row, nil
}
