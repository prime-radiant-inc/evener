package hostops

// Confirmation-token tests (spec 08b §3, §6 step 4, §12). The matrix is §12's
// token bullet: missing, mismatched, expired, superseded, consumed-then-replayed
// (which pins to token-missing, because consume deletes the row),
// expiry-across-the-wait, the wall-clock rollback guard, and
// supersede-between-validate-and-consume; plus what §3's storage and lifecycle
// paragraph requires of the durable row: mint persists under the same atomic
// write as every other store write, a consumed row stays gone across a reload,
// expiry reaps lazily and at boot, and a generation-bound token is refused once
// the registry pair moves.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// tokenEpoch is the fixed wall-clock instant these tests run at: every fixture
// is an offset from it, so the arithmetic in each test is readable and nothing
// depends on the real clock.
var tokenEpoch = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// testClock is the injectable clock the token paths read.
type testClock struct{ at time.Time }

func (c *testClock) now() time.Time          { return c.at.UTC() }
func (c *testClock) set(at time.Time)        { c.at = at.UTC() }
func (c *testClock) advance(d time.Duration) { c.set(c.at.Add(d)) }

// openClockStore opens a fresh store whose token paths read the returned clock.
func openClockStore(t *testing.T) (*Store, string, *testClock) {
	t.Helper()
	path := StorePath(t.TempDir())
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	clock := &testClock{at: tokenEpoch}
	store.clock = clock.now
	return store, path, clock
}

// reopenClockStore reopens a store path the way a process restart does, with the
// same injected clock.
func reopenClockStore(t *testing.T, path string, clock *testClock) *Store {
	t.Helper()
	store := reopenFresh(t, path)
	store.clock = clock.now
	return store
}

// testTokenHash renders a canonical 64-hex digest for the binding fields the
// caller hands the store. The store never interprets these values; the fixtures
// just need values of the shape a real caller passes.
func testTokenHash(seed string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(seed)))
}

// mintDefaults is the MintRequest fixture every test starts from: one host, one
// generation, facts captured at capturedAt, the default TTL and freshness bound.
func mintDefaults(host string, capturedAt time.Time) MintRequest {
	return MintRequest{
		Host:               host,
		Generation:         7,
		IncarnationID:      "inc-" + host,
		EntryHash:          testTokenHash("entry-" + host),
		HubTOMLFingerprint: testTokenHash("toml-" + host),
		FactsRevision:      testTokenHash("facts-" + host),
		FactsCapturedAt:    capturedAt,
		TargetPath:         "/opt/evener",
		ControllerRevision: "v1.2.3",
		RunningVersion:     "v1.1.0",
		RunningHealthy:     true,
		FreshnessBound:     DefaultFreshnessBound,
		TTL:                DefaultTokenTTL,
	}
}

// mustMint mints a token or fails the test.
func mustMint(t *testing.T, store *Store, req MintRequest) Token {
	t.Helper()
	token, err := store.MintToken(req)
	if err != nil {
		t.Fatalf("MintToken(%s): %v", req.Host, err)
	}
	return token
}

// tokenRows decodes the token rows of the store file at path. A store file that
// does not exist yet has no rows.
func tokenRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	var doc struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode store %s: %v", path, err)
	}
	return doc.Tokens
}

// tokenRowJSON renders one token row in the store file's shape, so the fixture
// pins the on-disk schema rather than reading it back from the implementation's
// marshaler.
func tokenRowJSON(host, value string, mintedAt, expiresAt, factsCapturedAt time.Time) string {
	return `{"host":"` + host + `","value":"` + value + `","generation":7,` +
		`"incarnationId":"inc-` + host + `",` +
		`"entryHash":"` + testTokenHash("entry-"+host) + `",` +
		`"hubTomlFingerprint":"` + testTokenHash("toml-"+host) + `",` +
		`"factsRevision":"` + testTokenHash("facts-"+host) + `",` +
		`"factsCapturedAt":"` + factsCapturedAt.Format(time.RFC3339) + `",` +
		`"targetPath":"/opt/evener","controllerRevision":"v1.2.3",` +
		`"runningVersion":"v1.1.0","runningHealthy":true,` +
		`"freshnessBoundSec":300,` +
		`"mintedAt":"` + mintedAt.Format(time.RFC3339) + `",` +
		`"expiresAt":"` + expiresAt.Format(time.RFC3339) + `"}`
}

// tokenStoreJSON renders a whole store file carrying the wall-clock mark and the
// given token rows.
func tokenStoreJSON(mark time.Time, rows ...string) string {
	return `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"boundaries":{},` +
		`"wallClockHighWaterMark":"` + mark.Format(time.RFC3339) + `",` +
		`"tokens":[` + strings.Join(rows, ",") + `]}`
}

// wantTokenError asserts the classified refusal a validate/consume pass
// returned. Callers pass every result through it — never behind `if err != nil`,
// which would let a pass that wrongly succeeded go untested.
func wantTokenError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestTokenMintValidateConsumeRoundTrip is the baseline positive control: a
// minted token validates for its own host, carries every binding §3 lists, and
// consuming it both returns the row and deletes it — a replay then reads
// token-missing (§3: "a consumed token presented again reads as token-missing").
func TestTokenMintValidateConsumeRoundTrip(t *testing.T) {
	store, path, _ := openClockStore(t)
	minted := mustMint(t, store, mintDefaults("m4", tokenEpoch))

	if got := len(minted.Value); got < minTokenValueChars {
		t.Fatalf("minted value is %d characters, want at least %d", got, minTokenValueChars)
	}
	if minted.Host != "m4" || minted.Generation != 7 || minted.IncarnationID != "inc-m4" {
		t.Fatalf("minted bindings = %+v, want the requested host/generation/incarnation", minted)
	}
	if minted.EntryHash != testTokenHash("entry-m4") || minted.HubTOMLFingerprint != testTokenHash("toml-m4") ||
		minted.FactsRevision != testTokenHash("facts-m4") {
		t.Fatalf("minted hashes = %+v, want the requested bindings", minted)
	}
	if minted.TargetPath != "/opt/evener" || minted.ControllerRevision != "v1.2.3" ||
		minted.RunningVersion != "v1.1.0" || !minted.RunningHealthy {
		t.Fatalf("minted plan bindings = %+v, want the requested values", minted)
	}
	if !minted.FactsCapturedAt.Equal(tokenEpoch) {
		t.Fatalf("factsCapturedAt = %s, want %s", minted.FactsCapturedAt, tokenEpoch)
	}
	if minted.ProcessStartTime != nil {
		t.Fatalf("processStartTime = %s, want absent when the probe carried none", minted.ProcessStartTime)
	}
	if minted.FreshnessBoundSec != int64(DefaultFreshnessBound/time.Second) {
		t.Fatalf("freshnessBoundSec = %d, want the effective bound", minted.FreshnessBoundSec)
	}
	// expiresAt = min(mintTime + ttl, factsCapturedAt + bound): both terms land
	// on the same instant here.
	if want := tokenEpoch.Add(DefaultTokenTTL); !minted.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want %s", minted.ExpiresAt, want)
	}
	if !minted.MintedAt.Equal(tokenEpoch) {
		t.Fatalf("mintedAt = %s, want %s", minted.MintedAt, tokenEpoch)
	}

	validated, err := store.ValidateToken("m4", minted.Value)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if validated.Value != minted.Value || validated.Host != "m4" || !validated.ExpiresAt.Equal(minted.ExpiresAt) {
		t.Fatalf("validated = %+v, want the minted row", validated)
	}
	if row, ok := store.OutstandingToken("m4"); !ok || row.Value != minted.Value {
		t.Fatalf("OutstandingToken = %+v/%v, want the minted row", row, ok)
	}
	if rows := tokenRows(t, path); len(rows) != 1 || rows[0]["value"] != minted.Value {
		t.Fatalf("store file rows = %+v, want exactly the minted row", rows)
	}

	consumed, err := store.ConsumeToken("m4", minted.Value)
	if err != nil {
		t.Fatalf("ConsumeToken: %v", err)
	}
	if consumed.Value != minted.Value {
		t.Fatalf("consumed = %+v, want the minted row", consumed)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("OutstandingToken still reports a consumed token")
	}
	if rows := tokenRows(t, path); len(rows) != 0 {
		t.Fatalf("store file rows after consume = %+v, want the row deleted", rows)
	}
	// Consume is delete, never a mark: the replayed token reads token-missing.
	if _, err := store.ValidateToken("m4", minted.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
	if _, err := store.ConsumeToken("m4", minted.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
}

// TestTokenMintSupersedesEarlierToken pins supersede-on-mint: minting a new
// token for a host immediately supersedes the earlier unconsumed one, and the
// mint write deletes the superseded row rather than letting rows accumulate.
func TestTokenMintSupersedesEarlierToken(t *testing.T) {
	store, path, clock := openClockStore(t)
	first := mustMint(t, store, mintDefaults("m4", tokenEpoch))
	clock.advance(time.Second)
	second := mustMint(t, store, mintDefaults("m4", clock.now()))
	if second.Value == first.Value {
		t.Fatal("a second mint reused the earlier token's value")
	}

	if _, err := store.ValidateToken("m4", first.Value); !errors.Is(err, ErrTokenSuperseded) {
		t.Fatalf("err = %v, want %v", err, ErrTokenSuperseded)
	}
	if _, err := store.ConsumeToken("m4", first.Value); !errors.Is(err, ErrTokenSuperseded) {
		t.Fatalf("err = %v, want %v", err, ErrTokenSuperseded)
	}
	if row, err := store.ValidateToken("m4", second.Value); err != nil || row.Value != second.Value {
		t.Fatalf("the current token failed after a supersede: %+v/%v", row, err)
	}
	rows := tokenRows(t, path)
	if len(rows) != 1 || rows[0]["value"] != second.Value {
		t.Fatalf("store file rows = %+v, want exactly the superseding row", rows)
	}
}

// TestTokenMissingAndMismatched splits the two fail-fast refusals §6 step 2
// names: a well-formed value no row of the store matches is token-missing, while
// a value that is not a token of this store at all — not in the wire's token
// shape, or a live token of another host — is token-mismatched.
func TestTokenMissingAndMismatched(t *testing.T) {
	store, _, _ := openClockStore(t)
	other := mustMint(t, store, mintDefaults("other", tokenEpoch))

	wellFormed := strings.Repeat("A", minTokenValueChars)
	if _, err := store.ValidateToken("m4", wellFormed); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
	for _, value := range []string{"", "not-a-token", strings.Repeat("A", minTokenValueChars-1), "!@#$%^&*" + strings.Repeat("A", 24)} {
		if _, err := store.ConsumeToken("m4", value); !errors.Is(err, ErrTokenMismatched) {
			wantTokenError(t, err, ErrTokenMismatched)
		}
	}
	// A live token is bound to its host: presenting it for another host is a
	// mismatch, never a supersede of the other host's row or a silent success.
	if _, err := store.ValidateToken("m4", other.Value); !errors.Is(err, ErrTokenMismatched) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMismatched)
	}
	if _, err := store.ConsumeToken("m4", other.Value); !errors.Is(err, ErrTokenMismatched) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMismatched)
	}
	if row, ok := store.OutstandingToken("other"); !ok || row.Value != other.Value {
		t.Fatalf("the other host's token was disturbed: %+v/%v", row, ok)
	}
}

// TestTokenExpiryRefusalAndLazyReap pins §6 step 4's clock rule ("an expiresAt
// at or before now is a token-expired refusal") and §3's lazy reaping: the pass
// that reads the expiry also drops the row, so the next presentation reads
// token-missing.
func TestTokenExpiryRefusalAndLazyReap(t *testing.T) {
	store, path, clock := openClockStore(t)
	req := mintDefaults("m4", tokenEpoch)
	req.TTL = time.Minute
	req.FreshnessBound = 10 * time.Minute
	minted := mustMint(t, store, req)

	clock.advance(59 * time.Second)
	if _, err := store.ValidateToken("m4", minted.Value); err != nil {
		t.Fatalf("a token inside its TTL was refused: %v", err)
	}
	clock.set(tokenEpoch.Add(time.Minute))
	if _, err := store.ValidateToken("m4", minted.Value); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want %v", err, ErrTokenExpired)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("the expired row was not reaped by the pass that read it")
	}
	if rows := tokenRows(t, path); len(rows) != 0 {
		t.Fatalf("store file rows after the expiry pass = %+v, want none", rows)
	}
	if _, err := store.ValidateToken("m4", minted.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
}

// TestTokenExpiresAtClampsToFreshnessBound pins §3's formula: expiresAt is the
// earlier of mintTime+TTL and factsCapturedAt+bound, so a token minted from
// facts captured before the mint already carries the refresh-to-mint interval.
func TestTokenExpiresAtClampsToFreshnessBound(t *testing.T) {
	store, _, _ := openClockStore(t)
	req := mintDefaults("m4", tokenEpoch.Add(-4*time.Minute))
	minted := mustMint(t, store, req)
	if want := tokenEpoch.Add(time.Minute); !minted.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want the freshness term %s", minted.ExpiresAt, want)
	}

	// An owner-set TTL above the bound clamps to the bound at mint.
	longer := mintDefaults("m5", tokenEpoch)
	longer.TTL = time.Hour
	clamped := mustMint(t, store, longer)
	if want := tokenEpoch.Add(DefaultFreshnessBound); !clamped.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want the clamped %s", clamped.ExpiresAt, want)
	}
	if clamped.FreshnessBoundSec != int64(DefaultFreshnessBound/time.Second) {
		t.Fatalf("freshnessBoundSec = %d, want the effective bound in force at mint", clamped.FreshnessBoundSec)
	}

	// Freshness is immutable for the token's lifetime: minting a later token
	// under a shorter bound leaves this one's deadline alone.
	short := mintDefaults("m4", tokenEpoch)
	short.FreshnessBound = 30 * time.Second
	short.TTL = 30 * time.Second
	mustMint(t, store, short)
	if row, err := store.ValidateToken("m5", clamped.Value); err != nil || !row.ExpiresAt.Equal(clamped.ExpiresAt) {
		t.Fatalf("an outstanding token's deadline moved: %+v/%v", row, err)
	}
}

// TestTokenMintRefusesStaleFacts pins §3's mint-time freshness refusal: facts
// whose bound is already exhausted refuse without minting — never an
// already-expired token.
func TestTokenMintRefusesStaleFacts(t *testing.T) {
	store, path, _ := openClockStore(t)
	req := mintDefaults("m4", tokenEpoch.Add(-DefaultFreshnessBound))
	if _, err := store.MintToken(req); !errors.Is(err, ErrFactsStale) {
		t.Fatalf("err = %v, want %v", err, ErrFactsStale)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("a stale-facts mint stored a token")
	}
	if _, err := os.Stat(path); err == nil {
		if rows := tokenRows(t, path); len(rows) != 0 {
			t.Fatalf("store file rows = %+v, want no write", rows)
		}
	}
}

// TestTokenPersistsAcrossReloadAndStaysConsumed is §3's storage bullet: a minted
// row is durable under the same atomic write as every other store write, an
// unexpired binding-intact token survives a restart, and a consumed row does
// not come back with it.
func TestTokenPersistsAcrossReloadAndStaysConsumed(t *testing.T) {
	store, path, clock := openClockStore(t)
	larger := mintDefaults("m4", tokenEpoch)
	minted := mustMint(t, store, larger)

	reloaded := reopenClockStore(t, path, clock)
	// The reload prunes nothing: the row is still exactly the minted bindings.
	if row, err := reloaded.ValidateToken("m4", minted.Value); err != nil || row.Generation != 7 ||
		row.IncarnationID != "inc-m4" || row.TargetPath != "/opt/evener" {
		t.Fatalf("reloaded token = %+v/%v, want the minted bindings", row, err)
	}
	if _, err := reloaded.ConsumeToken("m4", minted.Value); err != nil {
		t.Fatalf("ConsumeToken after reload: %v", err)
	}
	restarted := reopenClockStore(t, path, clock)
	if _, err := restarted.ValidateToken("m4", minted.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
}

// TestTokenRollbackGuardKeepsMintedLifetime is §3's wall-clock rollback guard: a
// rollback past the tolerance invalidates nothing by itself — a pre-rollback
// token keeps the real-time lifetime its persisted expiresAt granted — while a
// capture taken during the rollback anchors at max(now, mark), and an expiry the
// store already observed never comes back to life.
func TestTokenRollbackGuardKeepsMintedLifetime(t *testing.T) {
	store, path, clock := openClockStore(t)
	minted := mustMint(t, store, mintDefaults("m4", tokenEpoch))
	if want := tokenEpoch.Add(DefaultTokenTTL); !minted.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want %s", minted.ExpiresAt, want)
	}

	// A rollback two minutes behind the mark (far past the 30-second tolerance)
	// invalidates nothing: the token keeps its minted deadline.
	clock.set(tokenEpoch.Add(-2 * time.Minute))
	if _, err := store.ValidateToken("m4", minted.Value); err != nil {
		t.Fatalf("a pre-rollback token was refused during the rollback: %v", err)
	}
	// A capture taken during the rollback anchors at max(now, mark), so its
	// deadline is measured from the mark — never from the rewound clock, which
	// would shorten a fresh token's life. (The facts here were captured at the
	// mark, before the rollback, so the TTL term is the one the anchor decides.)
	anchored := mustMint(t, store, mintDefaults("m5", tokenEpoch))
	if want := tokenEpoch.Add(DefaultTokenTTL); !anchored.ExpiresAt.Equal(want) {
		t.Fatalf("post-rollback capture expiresAt = %s, want the mark-anchored %s", anchored.ExpiresAt, want)
	}
	if !anchored.MintedAt.Equal(tokenEpoch) {
		t.Fatalf("post-rollback capture mintedAt = %s, want the mark %s", anchored.MintedAt, tokenEpoch)
	}
	var doc struct {
		Mark string `json:"wallClockHighWaterMark"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode store: %v", err)
	}
	if doc.Mark != tokenEpoch.Format(time.RFC3339) {
		t.Fatalf("durable mark = %s, want it never to move backward from %s", doc.Mark, tokenEpoch.Format(time.RFC3339))
	}

	// The pre-rollback token still expires on the wall clock reaching its minted
	// deadline — the rollback two minutes back never shortens it — and an expiry
	// the store has observed stays expired.
	clock.set(tokenEpoch.Add(DefaultTokenTTL - time.Second))
	if _, err := store.ValidateToken("m4", minted.Value); err != nil {
		t.Fatalf("token refused before its deadline: %v", err)
	}
	// A short-lived token minted while the clock sits behind the mark anchors at
	// the mark (mintedAt = +4m59s here), so its own deadline is +5m59s. A later
	// rollback must neither shorten that deadline — at +2m the effective time is
	// still the mark, so the token is inside its life — nor revive it once the
	// wall clock has passed it.
	expiring := mintDefaults("m6", tokenEpoch)
	expiring.TTL = time.Minute
	expiring.FreshnessBound = 10 * time.Minute
	shortLived := mustMint(t, store, expiring)
	if want := tokenEpoch.Add(DefaultTokenTTL + time.Minute - time.Second); !shortLived.ExpiresAt.Equal(want) {
		t.Fatalf("short-lived token expiresAt = %s, want the mark-anchored %s", shortLived.ExpiresAt, want)
	}
	clock.set(tokenEpoch.Add(2 * time.Minute))
	if _, err := store.ValidateToken("m6", shortLived.Value); err != nil {
		t.Fatalf("a rollback shortened a token's deadline: %v", err)
	}
	clock.set(tokenEpoch.Add(DefaultTokenTTL + 2*time.Minute))
	if _, err := store.ConsumeToken("m6", shortLived.Value); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want %v", err, ErrTokenExpired)
	}
	// A rollback after that expiry cannot revive it: the row is gone, and even a
	// still-present row whose deadline the store has observed stays expired.
	clock.set(tokenEpoch.Add(30 * time.Second))
	if _, err := store.ConsumeToken("m6", shortLived.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
}

// TestTokenRollbackWithinToleranceIsJitter pins the other half of the rollback
// rule: a within-tolerance step behind the mark is jitter and invalidates
// nothing — not even a rewrite of the store file.
func TestTokenRollbackWithinToleranceIsJitter(t *testing.T) {
	store, path, clock := openClockStore(t)
	minted := mustMint(t, store, mintDefaults("m4", tokenEpoch))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	clock.set(tokenEpoch.Add(-10 * time.Second))
	if _, err := store.ValidateToken("m4", minted.Value); err != nil {
		t.Fatalf("a within-tolerance step refused the token: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a jitter pass rewrote the store:\nbefore %s\nafter  %s", before, after)
	}
}

// TestTokenCorruptRowPostdatingTheMarkReadsExpired pins the fail-closed arm §3
// states once for every path: a record invalidates when its own timestamp
// postdates the mark, which is impossible for an honest capture — so a row
// carrying one reads token-expired rather than being trusted, and the store is
// not served as corrupt.
func TestTokenCorruptRowPostdatingTheMarkReadsExpired(t *testing.T) {
	path := StorePath(t.TempDir())
	future := tokenEpoch.Add(time.Hour)
	writeRawStore(t, path, 0o600, tokenStoreJSON(tokenEpoch,
		tokenRowJSON("m4", strings.Repeat("B", minTokenValueChars), future, future.Add(time.Minute), tokenEpoch)))

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open refused a file carrying a post-mark row: %v", err)
	}
	if _, err := store.ValidateToken("m4", strings.Repeat("B", minTokenValueChars)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want %v", err, ErrTokenExpired)
	}
}

// TestTokenCorruptFactsPostdatingTheMarkReadStale is the facts half of the same
// arm: a facts capture that postdates the mark cannot be honest, so mint refuses
// it as stale rather than minting from it.
func TestTokenCorruptFactsPostdatingTheMarkReadStale(t *testing.T) {
	store, _, _ := openClockStore(t)
	mustMint(t, store, mintDefaults("m4", tokenEpoch))
	future := mintDefaults("m5", tokenEpoch.Add(time.Hour))
	if _, err := store.MintToken(future); !errors.Is(err, ErrFactsStale) {
		t.Fatalf("err = %v, want %v", err, ErrFactsStale)
	}
	if _, ok := store.OutstandingToken("m5"); ok {
		t.Fatal("a mint from post-mark facts stored a token")
	}
}

// TestTokenMintRefusesUnusableRequests holds the schema refuse-always rule: a
// request outside the writers' schema persists nothing.
func TestTokenMintRefusesUnusableRequests(t *testing.T) {
	store, _, _ := openClockStore(t)
	cases := map[string]func(*MintRequest){
		"no host":             func(r *MintRequest) { r.Host = "" },
		"no generation":       func(r *MintRequest) { r.Generation = 0 },
		"no incarnation":      func(r *MintRequest) { r.IncarnationID = "" },
		"no entry hash":       func(r *MintRequest) { r.EntryHash = "" },
		"bad entry hash":      func(r *MintRequest) { r.EntryHash = "deadbeef" },
		"no toml fingerprint": func(r *MintRequest) { r.HubTOMLFingerprint = "" },
		"no facts revision":   func(r *MintRequest) { r.FactsRevision = "" },
		"no facts capture":    func(r *MintRequest) { r.FactsCapturedAt = time.Time{} },
		"no target path":      func(r *MintRequest) { r.TargetPath = "" },
		"no controller rev":   func(r *MintRequest) { r.ControllerRevision = "" },
		// The store's freshness unit is the whole second: a fractional bound is
		// refused rather than truncated, so the persisted field can never claim
		// a bound its deadline does not honour.
		"a fractional bound": func(r *MintRequest) { r.FreshnessBound = 1500 * time.Millisecond },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := mintDefaults("m4", tokenEpoch)
			mutate(&req)
			_, err := store.MintToken(req)
			wantTokenError(t, err, ErrInvalidToken)
			if _, ok := store.OutstandingToken("m4"); ok {
				t.Fatal("an unusable mint request stored a token")
			}
		})
	}
	if _, err := store.ValidateToken("", strings.Repeat("A", minTokenValueChars)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want %v", err, ErrInvalidToken)
	}
}

// TestOpenQuarantinesTokenRowsOutsideTheSchema pins the loader's half: a
// hand-edited row the writers never produce is a corrupt store, never a served
// one. §4's custody-first quarantine takes it — the replacement store serves
// zero outstanding tokens, and the malformed bytes are renamed aside, never
// deleted.
func TestOpenQuarantinesTokenRowsOutsideTheSchema(t *testing.T) {
	row := tokenRowJSON("m4", strings.Repeat("B", minTokenValueChars), tokenEpoch, tokenEpoch.Add(time.Minute), tokenEpoch)
	cases := map[string]string{
		"no running-health flag": strings.Replace(tokenStoreJSON(tokenEpoch, row), `"runningHealthy":true,`, "", 1),
		"no freshness bound":     strings.Replace(tokenStoreJSON(tokenEpoch, row), `"freshnessBoundSec":300,`, `"freshnessBoundSec":0,`, 1),
		"bad entry hash":         strings.Replace(tokenStoreJSON(tokenEpoch, row), testTokenHash("entry-m4"), "not-a-hash", 1),
		"two rows for one host": tokenStoreJSON(tokenEpoch, row,
			tokenRowJSON("m4", strings.Repeat("C", minTokenValueChars), tokenEpoch, tokenEpoch.Add(time.Minute), tokenEpoch)),
		"deadline before the mint": strings.Replace(tokenStoreJSON(tokenEpoch, row),
			`"expiresAt":"`+tokenEpoch.Add(time.Minute).Format(time.RFC3339)+`"`, `"expiresAt":"`+tokenEpoch.Add(-time.Minute).Format(time.RFC3339)+`"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			store, err := Open(path)
			if err != nil {
				t.Fatalf("Open on %s: %v", name, err)
			}
			wantQuarantined(t, store, path, body)
			if tokens := storeSnapshotForTest(store).Tokens; len(tokens) != 0 {
				t.Fatalf("the replacement store serves %d outstanding tokens, want zero", len(tokens))
			}
		})
	}
}

// TestTokenMintWriteDiscipline pins §3's storage rule at the write's own failure
// seam: a mint whose rename never landed wrote nothing, and a mint whose rename
// landed is the token's durable row even when the directory sync behind it
// failed (RenameLanded).
func TestTokenMintWriteDiscipline(t *testing.T) {
	t.Run("refusal before the rename writes nothing", func(t *testing.T) {
		path := StorePath(t.TempDir())
		store, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
			return errors.New("injected refusal")
		}})
		if err != nil {
			t.Fatalf("openFS: %v", err)
		}
		store.clock = (&testClock{at: tokenEpoch}).now
		if _, err := store.MintToken(mintDefaults("m4", tokenEpoch)); err == nil {
			t.Fatal("MintToken succeeded under a refusing write seam")
		}
		if _, ok := store.OutstandingToken("m4"); ok {
			t.Fatal("a refused mint left a row behind")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a refused mint created the store file: %v", err)
		}
	})
	t.Run("post-rename failure lands the token", func(t *testing.T) {
		path := StorePath(t.TempDir())
		// Only the store directory's own sync fails — the one saveFS runs after
		// the rename — so the fault lands behind the write's commit point.
		store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
			if dir != filepath.Dir(path) {
				return nil
			}
			return errors.New("injected sync failure")
		}})
		if err != nil {
			t.Fatalf("openFS: %v", err)
		}
		store.clock = (&testClock{at: tokenEpoch}).now
		minted, err := store.MintToken(mintDefaults("m4", tokenEpoch))
		if !RenameLanded(err) {
			t.Fatalf("MintToken error = %v, want a landed rename", err)
		}
		if minted.Value == "" {
			t.Fatal("a landed mint returned no token")
		}
		if rows := tokenRows(t, path); len(rows) != 1 || rows[0]["value"] != minted.Value {
			t.Fatalf("store file rows = %+v, want the landed row", rows)
		}
		if _, err := store.ValidateToken("m4", minted.Value); err != nil {
			t.Fatalf("the landed token does not validate: %v", err)
		}
	})
}

// TestTokenRevokeDropsOutstandingTokens is the durable half of live remove: a
// revoked name holds no outstanding token, and other names are untouched.
func TestTokenRevokeDropsOutstandingTokens(t *testing.T) {
	store, path, _ := openClockStore(t)
	removed := mustMint(t, store, mintDefaults("m4", tokenEpoch))
	kept := mustMint(t, store, mintDefaults("m5", tokenEpoch))
	if err := store.RevokeTokens("m4"); err != nil {
		t.Fatalf("RevokeTokens: %v", err)
	}
	if _, err := store.ValidateToken("m4", removed.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
	if _, err := store.ValidateToken("m5", kept.Value); err != nil {
		t.Fatalf("revoking one host dropped another's token: %v", err)
	}
	rows := tokenRows(t, path)
	if len(rows) != 1 || rows[0]["host"] != "m5" {
		t.Fatalf("store file rows = %+v, want only the kept host's row", rows)
	}
	// A re-added host starts with zero valid tokens: nothing to revive.
	if err := store.RevokeTokens("m4"); err != nil {
		t.Fatalf("RevokeTokens on a name with no row: %v", err)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("a revoked name still holds a token")
	}
}

// TestTokenReapExpiredTokens is §3's boot reap: expiry reaps lazily and at boot,
// and the pass drops exactly the rows whose deadlines the clock has passed.
func TestTokenReapExpiredTokens(t *testing.T) {
	store, _, clock := openClockStore(t)
	short := mintDefaults("m4", tokenEpoch)
	short.TTL = time.Minute
	short.FreshnessBound = 10 * time.Minute
	expired := mustMint(t, store, short)
	live := mustMint(t, store, mintDefaults("m5", tokenEpoch))

	clock.advance(2 * time.Minute)
	reaped, err := store.ReapExpiredTokens()
	if err != nil {
		t.Fatalf("ReapExpiredTokens: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("ReapExpiredTokens = %d, want 1", reaped)
	}
	if _, err := store.ValidateToken("m4", expired.Value); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("err = %v, want %v", err, ErrTokenMissing)
	}
	if _, err := store.ValidateToken("m5", live.Value); err != nil {
		t.Fatalf("the reap dropped an unexpired token: %v", err)
	}
	if again, err := store.ReapExpiredTokens(); err != nil || again != 0 {
		t.Fatalf("second reap = %d/%v, want 0/nil", again, err)
	}
}

// TestTokenBindingsRefuseDrift pins the bindings §3 says deploy refuses on: a
// token minted under one registry pair, entry fingerprint, target or facts
// revision is refused once the caller's resolved values drift, with the refusal
// naming the binding §11 spells.
func TestTokenBindingsRefuseDrift(t *testing.T) {
	store, _, _ := openClockStore(t)
	minted := mustMint(t, store, mintDefaults("m4", tokenEpoch))

	current := TokenExpectation{
		Generation:         7,
		IncarnationID:      "inc-m4",
		EntryHash:          testTokenHash("entry-m4"),
		HubTOMLFingerprint: testTokenHash("toml-m4"),
		TargetPath:         "/opt/evener",
	}
	if err := CheckTokenBindings(minted, current); err != nil {
		t.Fatalf("CheckTokenBindings on the minted bindings: %v", err)
	}
	// A caller that resolved nothing checks nothing.
	if err := CheckTokenBindings(minted, TokenExpectation{}); err != nil {
		t.Fatalf("CheckTokenBindings with no expectations: %v", err)
	}
	cases := map[string]struct {
		expect  TokenExpectation
		binding StaleBinding
	}{
		"generation advanced": {
			TokenExpectation{Generation: 8, IncarnationID: "inc-m4"},
			StaleBindingGeneration,
		},
		"incarnation replaced": {
			TokenExpectation{Generation: 7, IncarnationID: "inc-other"},
			StaleBindingGeneration,
		},
		"entry drifted": {
			TokenExpectation{Generation: 7, EntryHash: testTokenHash("entry-other")},
			StaleBindingEntry,
		},
		"hub.toml fingerprint drifted": {
			TokenExpectation{Generation: 7, HubTOMLFingerprint: testTokenHash("toml-other")},
			StaleBindingHubTOMLFingerprint,
		},
		"target drifted": {
			TokenExpectation{Generation: 7, TargetPath: "/srv/evener"},
			StaleBindingTarget,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stale *StaleEntryError
			if err := CheckTokenBindings(minted, tc.expect); !errors.As(err, &stale) {
				t.Fatalf("CheckTokenBindings = %v, want a StaleEntryError", err)
			} else if stale.Binding != tc.binding {
				t.Fatalf("refusal names %q, want %q", stale.Binding, tc.binding)
			}
		})
	}
	// The refusal is a check, not a consumption: the token stays consumable.
	if _, err := store.ConsumeToken("m4", minted.Value); err != nil {
		t.Fatalf("ConsumeToken after binding checks: %v", err)
	}
}

// TestTokenRunningStateAndFactsAgeRefuseDrift pins §6 step 3's remaining
// rejections, the ones the running-state and freshness bindings exist for: a
// re-probed revision or health flag that differs from the token-bound one, a
// re-probed process start time that no longer matches (or is gone) when the
// token bound one, and facts whose token-bound age has reached the token-bound
// bound.
func TestTokenRunningStateAndFactsAgeRefuseDrift(t *testing.T) {
	store, _, clock := openClockStore(t)
	probeStart := tokenEpoch.Add(-time.Hour)
	req := mintDefaults("m4", tokenEpoch)
	req.TTL = 30 * time.Minute
	req.FreshnessBound = 30 * time.Minute
	req.ProcessStartTime = &probeStart
	minted := mustMint(t, store, req)

	matching := TokenRunningState{Version: minted.RunningVersion, Healthy: minted.RunningHealthy, ProcessStartTime: &probeStart}
	if err := CheckTokenRunningState(minted, matching); err != nil {
		t.Fatalf("CheckTokenRunningState on the minted state: %v", err)
	}
	if err := CheckTokenFactsAge(minted, clock.now()); err != nil {
		t.Fatalf("CheckTokenFactsAge on fresh facts: %v", err)
	}

	cases := map[string]struct {
		check   func() error
		binding StaleBinding
	}{
		"running revision differs": {
			check: func() error {
				changed := matching
				changed.Version = "v0.0.1"
				return CheckTokenRunningState(minted, changed)
			},
			binding: StaleBindingRunningVersion,
		},
		"health flag differs": {
			check: func() error {
				changed := matching
				changed.Healthy = !minted.RunningHealthy
				return CheckTokenRunningState(minted, changed)
			},
			binding: StaleBindingRunningHealth,
		},
		"process start time differs": {
			check: func() error {
				other := probeStart.Add(time.Minute)
				changed := matching
				changed.ProcessStartTime = &other
				return CheckTokenRunningState(minted, changed)
			},
			binding: StaleBindingRunningVersion,
		},
		"process start time lost": {
			check: func() error {
				changed := matching
				changed.ProcessStartTime = nil
				return CheckTokenRunningState(minted, changed)
			},
			binding: StaleBindingRunningVersion,
		},
		"facts reached their bound": {
			// The bound is measured from the token's own capture, never from a
			// re-read knob: exactly at the bound is already stale.
			check:   func() error { return CheckTokenFactsAge(minted, tokenEpoch.Add(30*time.Minute)) },
			binding: StaleBindingFactsAge,
		},
		"facts past their bound": {
			check:   func() error { return CheckTokenFactsAge(minted, tokenEpoch.Add(time.Hour)) },
			binding: StaleBindingFactsAge,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stale *StaleEntryError
			if err := tc.check(); !errors.As(err, &stale) {
				t.Fatalf("check = %v, want a StaleEntryError", err)
			} else if stale.Binding != tc.binding {
				t.Fatalf("refusal names %q, want %q", stale.Binding, tc.binding)
			}
		})
	}

	// A token minted from a probe that carried no process start time compares
	// none, and a run of checks consumes nothing.
	noStart := mintDefaults("m5", tokenEpoch)
	noStart.ProcessStartTime = nil
	plain := mustMint(t, store, noStart)
	if err := CheckTokenRunningState(plain, TokenRunningState{
		Version: plain.RunningVersion, Healthy: plain.RunningHealthy, ProcessStartTime: &probeStart,
	}); err != nil {
		t.Fatalf("a token that bound no process start time compared one: %v", err)
	}
	if _, err := store.ConsumeToken("m4", minted.Value); err != nil {
		t.Fatalf("ConsumeToken after the running-state checks: %v", err)
	}
}

// TestTokenMintIfQuiescentRefusesAConcurrentTerminalOperation pins §6 step 2's
// concurrent-terminal-op guard where it is atomic: the check rides the mint's
// own locked read, so an operation that reached a terminal state after the
// caller's pre-read position refuses the mint with the record named, writes
// nothing, and leaves the host plannable once the caller re-reads the position.
func TestTokenMintIfQuiescentRefusesAConcurrentTerminalOperation(t *testing.T) {
	store, _, _ := openClockStore(t)
	record := createTestRecord(t, store, "m4")
	position := store.Sequence()
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}

	_, err := store.MintTokenIfQuiescent(mintDefaults("m4", tokenEpoch), position)
	if !errors.Is(err, ErrConcurrentTerminalOp) {
		t.Fatalf("MintTokenIfQuiescent = %v, want %v", err, ErrConcurrentTerminalOp)
	}
	var concurrent *ConcurrentTerminalOpError
	if !errors.As(err, &concurrent) || concurrent.ID != record.ID {
		t.Fatalf("refusal = %v, want the record %q named", err, record.ID)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("a mint refused by a concurrent terminal operation stored a token")
	}

	// The same request against the store's current position mints: the guard
	// refuses the stale plan, never the host.
	minted, err := store.MintTokenIfQuiescent(mintDefaults("m4", tokenEpoch), store.Sequence())
	if err != nil {
		t.Fatalf("MintTokenIfQuiescent at the current position: %v", err)
	}
	if _, err := store.ValidateToken("m4", minted.Value); err != nil {
		t.Fatalf("the quiescent mint's token does not validate: %v", err)
	}
}

// TestTokenValidatePassIsARead pins §6 step 2's contract that validate is a
// fail-fast readability check: a pass with nothing to reap and nothing to
// consume compares against max(now, mark) without rewriting the store file.
// Advancing the mark is a writing pass's act — a mint, a consume, a reap — so a
// read never turns into a durable whole-file write just because the clock moved.
func TestTokenValidatePassIsARead(t *testing.T) {
	store, path, clock := openClockStore(t)
	minted := mustMint(t, store, mintDefaults("m4", tokenEpoch))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Real time moves past the mark; the token is unexpired, so nothing is reaped.
	clock.advance(30 * time.Second)
	if _, err := store.ValidateToken("m4", minted.Value); err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a read-only validate pass rewrote the store file:\nbefore %s\nafter  %s", before, after)
	}

	// A consuming pass writes, and records the clock it observed.
	if _, err := store.ConsumeToken("m4", minted.Value); err != nil {
		t.Fatalf("ConsumeToken: %v", err)
	}
	var doc struct {
		Mark string `json:"wallClockHighWaterMark"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode store: %v", err)
	}
	if want := tokenEpoch.Add(30 * time.Second).Format(time.RFC3339); doc.Mark != want {
		t.Fatalf("durable mark = %s, want the consume's %s", doc.Mark, want)
	}
}
