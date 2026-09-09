// Progress ledger: fingerprints, novelty, tiers, backstop (spec §4).
//
// FoldLedger maps (prev summary, TurnOutcome, waits-subgoal evidence) to the
// next summary, and the stall predicates read the summary. Slice 2 wires the
// fold into the gate (DecideGoalStep rules 6-7 + RecordContinuation); the
// interim v1 judge is retired.
//
// Stall rule (exact): K consecutive turns with identical
// (actionFingerprint, observationClass) AND no state-digest delta, with K=3
// after first mutation-or-subgoal evidence and K=6 while never-advanced.
// B=12 total backstop: B consecutive non-advancing turns (no novelty, no
// digest delta, no subgoal evidence — regardless of alternation) with B=12
// closes rotation periods ≤ N=8. Longer rotations (period > 8,
// novelty-every-turn) fall back to the maxContinuations outer bound (default
// 200) — same disclosure pattern as junk-write alternation. Mutating-but-
// advancing loops are bounded by maxContinuations, not by B: the ledger is
// honestly syntactic+digest-only per Non-goals.
//
//   - mutated-with-delta resets the run (rep=1) and tightens the tier to K=3;
//     mutated-no-delta (junk write) still accrues under the current tier.
//   - waits-only advancement: only waits' predicate flips feed the ledger
//     (the waitAdvanced param). goal_expect conditions are check-on-claim and
//     never feed it, so expect polling cannot launder the re-park counter.
//   - canonicalized hashing: timestamps/ids/RNG redacted, class-only fallback
//     for unhashable observations, never novel-by-default.
//   - fixed digest scope (job/delegate/watch/wait/file-listing
//     names+sizes+mtimes; history forbidden): the digest arrives
//     pre-scoped from the caller — this unit compares digests for delta and
//     never inspects history.
//   - migrated fingerprint + residual: seeded "migrated" entries are distinct
//     from all real fingerprints (the canonicalizer escapes a real
//     "migrated" action), so a post-migration different first turn resets the
//     run to 1 — granting at most K−1 extra turns once per migration. The
//     B=12 backstop still bites longer evasions.
package goal

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// Repetition tiers and the backstop/novelty bounds (spec §4).
const (
	// RepetitionThresholdAdvanced is K=3: the stall run length once the goal
	// has produced mutation-or-subgoal evidence.
	RepetitionThresholdAdvanced = 3
	// RepetitionThresholdFresh is K=6: the stall run length while
	// never-advanced (read-heavy openings are never penalized).
	RepetitionThresholdFresh = 6
	// BackstopThreshold is B=12: consecutive non-advancing turns (any
	// rotation period ≤ N) that graduate to block.
	BackstopThreshold = 12
	// LedgerNoveltyWindow is N=8: observation hashes/classes unseen in the
	// last N entries count as novel (advancing).
	LedgerNoveltyWindow = 8
)

var (
	ledgerUUIDRe      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	ledgerTimestampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
	ledgerLongHexRe   = regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`)
	ledgerLongNumRe   = regexp.MustCompile(`\b\d{8,}\b`)
	ledgerVolatileKey = regexp.MustCompile(`(?i)\b(id|job_id|delegate_id|request_id|req_id|trace_id|span_id|run_id|nonce|timestamp|ts|attempt_id)\s*=\s*[^\s]+`)
)

// ledgerVolatileKeys names the keyed volatile arguments whose values redact
// wholesale (timestamps/nonces/ids travel as values here).
var ledgerVolatileKeys = map[string]bool{
	"id": true, "job_id": true, "delegate_id": true,
	"request_id": true, "req_id": true, "trace_id": true,
	"span_id": true, "run_id": true, "nonce": true,
	"timestamp": true, "ts": true, "attempt_id": true,
}

// CanonicalizeActionFingerprint normalizes tool + args: the tool name
// lowercases (args stay case-sensitive so refining greps stay distinct),
// whitespace collapses, absolute paths clean (volatile flags stripped via
// volatile-token redaction, timestamps/nonces excluded). Blank stays blank.
// A real action spelling "migrated" escapes to a suffixed form so seeded
// migration entries (raw MigratedFingerprint) stay distinct from all real
// fingerprints.
func CanonicalizeActionFingerprint(fp string) string {
	trimmed := strings.TrimSpace(fp)
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	fields[0] = strings.ToLower(fields[0])
	for i, f := range fields {
		if key, value, ok := strings.Cut(f, "="); ok {
			if ledgerVolatileKeys[strings.ToLower(key)] {
				fields[i] = key + "=<id>"
				continue
			}
			if strings.HasPrefix(value, "/") {
				value = path.Clean(value)
			}
			fields[i] = key + "=" + redactVolatileToken(value)
			continue
		}
		if strings.HasPrefix(f, "/") {
			f = path.Clean(f)
		}
		fields[i] = redactVolatileToken(f)
	}
	norm := strings.Join(fields, " ")
	if norm == MigratedFingerprint {
		return MigratedFingerprint + ":tool"
	}
	return norm
}

// redactVolatileToken redacts bare volatile tokens (UUIDs, timestamps, long
// hex/numeric nonces) inside one whitespace-separated field.
func redactVolatileToken(f string) string {
	f = ledgerUUIDRe.ReplaceAllString(f, "<id>")
	f = ledgerTimestampRe.ReplaceAllString(f, "<ts>")
	f = ledgerLongHexRe.ReplaceAllString(f, "<id>")
	f = ledgerLongNumRe.ReplaceAllString(f, "<n>")
	return f
}

// CanonicalizeObservationOutput redacts volatile observation content
// (timestamps, request ids, UUIDs) and collapses whitespace, so
// timestamp-noise-only outputs canonicalize equal while genuinely different
// content stays distinct. Applied BEFORE hashing at the evidence seam:
// hashing the raw output then canonicalizing the hex digest would redact
// the digest itself (a 64-char hex token) to "<id>", collapsing every
// non-empty output to one hash and suppressing novelty entirely.
func CanonicalizeObservationOutput(out string) string {
	if out == "" {
		return ""
	}
	out = ledgerTimestampRe.ReplaceAllString(out, "<ts>")
	out = ledgerVolatileKey.ReplaceAllString(out, "$1=<id>")
	out = ledgerUUIDRe.ReplaceAllString(out, "<id>")
	out = strings.Join(strings.Fields(out), " ")
	return out
}

// CanonicalizeObservationHash redacts volatile observation content
// (timestamps, request ids, RNG) over the stated scope and collapses
// whitespace. Timestamp-noise-only outputs canonicalize equal; genuinely
// different content stays distinct. Empty stays empty — the class-only
// fallback signal (never novel by default). Shared with
// CanonicalizeObservationOutput (which additionally redacts long hex: raw
// outputs may carry hashes, while the evidence seam hashes canonicalized
// output and must never redact the digest it is about to compare).
func CanonicalizeObservationHash(h string) string {
	if h == "" {
		return ""
	}
	return ledgerLongHexRe.ReplaceAllString(CanonicalizeObservationOutput(h), "<id>")
}

// FoldLedger folds one finished turn into the next ledger summary. It is
// pure: prev is never mutated, the window is bounded to LedgerWindowSize, and
// the graduation stage passes through untouched (its owner is the Task-8
// stage machine — a restart after a nudge graduates, never re-nudges).
//
// Advancement for this turn = observation-novelty (canonicalized hash unseen
// in the last N entries; class-only fallback when the hash is empty) OR
// state-digest delta (differs from the previous entry) OR waits-predicate
// evidence (waitAdvanced — flips only, never goal_expect checks). Bare
// mutation without a delta is not advancement (junk write accrues).
func FoldLedger(prev LedgerSummary, outcome TurnOutcome, waitAdvanced bool) LedgerSummary {
	fp := CanonicalizeActionFingerprint(outcome.ActionFingerprint)
	class := outcome.ObservationClass
	hash := CanonicalizeObservationHash(outcome.ObservationHash)
	digest := outcome.StateDigest

	hashNovel := false
	if hash != "" {
		hashNovel = true
		for _, e := range ledgerTail(prev.Entries, LedgerNoveltyWindow) {
			if e.Hash == hash {
				hashNovel = false
				break
			}
		}
	}
	classNovel := false
	if hash == "" && class != "" {
		classNovel = true
		for _, e := range ledgerTail(prev.Entries, LedgerNoveltyWindow) {
			if e.Class == class {
				classNovel = false
				break
			}
		}
	}
	digestDelta := false
	if n := len(prev.Entries); n > 0 {
		digestDelta = digest != prev.Entries[n-1].Digest
	}
	advancing := hashNovel || classNovel || digestDelta || waitAdvanced

	tier := prev.Tier
	if tier != RepetitionThresholdAdvanced && tier != RepetitionThresholdFresh {
		tier = RepetitionThresholdFresh
	}
	if (outcome.Mutated && digestDelta) || waitAdvanced {
		tier = RepetitionThresholdAdvanced
	}

	rep := 1
	if n := len(prev.Entries); n > 0 {
		last := prev.Entries[n-1]
		if fp == last.Fingerprint && class == last.Class && !digestDelta {
			rep = prev.Repetition + 1
		}
	}

	kept := prev.Entries
	if len(kept) > LedgerWindowSize-1 {
		kept = kept[len(kept)-(LedgerWindowSize-1):]
	}
	entries := make([]LedgerEntry, 0, len(kept)+1)
	entries = append(entries, kept...)
	entries = append(entries, LedgerEntry{
		Fingerprint: fp,
		Class:       class,
		Hash:        hash,
		Digest:      digest,
		Advancement: advancing,
	})
	return LedgerSummary{
		Entries:    entries,
		Repetition: rep,
		Tier:       tier,
		Stage:      prev.Stage,
	}
}

// ledgerTail returns the last n entries (or all when fewer).
func ledgerTail(entries []LedgerEntry, n int) []LedgerEntry {
	if len(entries) <= n {
		return entries
	}
	return entries[len(entries)-n:]
}

// ledgerThreshold returns the active K for a summary: K=3 once advanced,
// K=6 otherwise (unknown/zero tiers read as fresh — never stricter).
func ledgerThreshold(s LedgerSummary) int {
	if s.Tier == RepetitionThresholdAdvanced {
		return RepetitionThresholdAdvanced
	}
	return RepetitionThresholdFresh
}

// RepetitionStalled reports whether the trailing identical-(fingerprint,
// class)-with-no-digest-delta run reached K for the summary's tier.
func RepetitionStalled(s LedgerSummary) bool {
	return s.Repetition >= ledgerThreshold(s)
}

// BackstopStalled reports whether the trailing run of non-advancing entries
// reached B=12 (no novelty, no digest delta, no subgoal evidence —
// regardless of alternation, so rotation periods ≤ N=8 cannot run unbounded).
func BackstopStalled(s LedgerSummary) bool {
	if len(s.Entries) < BackstopThreshold {
		return false
	}
	run := 0
	for _, e := range slices.Backward(s.Entries) {
		if e.Advancement {
			break
		}
		run++
	}
	return run >= BackstopThreshold
}

// LedgerStalled reports either stall signal: repetition-K or the total
// non-advancement backstop.
func LedgerStalled(s LedgerSummary) bool {
	return RepetitionStalled(s) || BackstopStalled(s)
}
