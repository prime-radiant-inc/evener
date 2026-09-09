// Wait registry: kinds, leases, validation, idempotency, atomic fire-consume.
//
// Spec: docs/superpowers/specs/2026-09-07-goal-wait-redesign-design.md §§1-2.
// Slice 1 (this task): UntilTime registration + registry mechanics + fail-closed
// rejection of substrate kinds until Wave B wires the Substrate consultation +
// terminal catch-up routing. Later slices add timer re-arm (Task 4), gate claim
// wiring (Task 2), and the ledger (Task 6).
package goal

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind is a wait-lease predicate kind. The six registry kinds (spec §2).
type Kind string

const (
	WaitUntilTime     Kind = "until_time"
	WaitUntilJob      Kind = "until_job"
	WaitUntilDelegate Kind = "until_delegate"
	WaitUntilApproval Kind = "until_approval"
	WaitUntilEvent    Kind = "until_event"
	WaitUntilChild    Kind = "until_child"
)

// EventSubtype refines WaitUntilEvent (spec §2).
type EventSubtype string

const (
	EventFileModified  EventSubtype = "file_modified"
	EventHTTPMatch     EventSubtype = "http_match"
	EventExternalLabel EventSubtype = "external_label"
)

// Wait registration + lease bounds (spec §2 defaults table).
const (
	// DefaultWaitTimeout applies when the registrant omits Timeout.
	DefaultWaitTimeout = 10 * time.Minute
	// MaxWaitTimeoutCap is the largest registrable wait timeout.
	MaxWaitTimeoutCap = 24 * time.Hour
	// MaxLiveWaitsPerGoal caps live leases per goal.
	MaxLiveWaitsPerGoal = 8
	// Model-controlled predicate/label size caps (spec §2).
	MaxMatcherBytes = 1024
	MaxURLBytes     = 2048
	MaxLabelRunes   = 256
)

// WaitKind is a wait registration descriptor: the kind plus the full predicate
// payload the lease persists (spec §2 lease fields). Timeout is required but
// defaults to DefaultWaitTimeout when zero; above MaxWaitTimeoutCap rejects.
type WaitKind struct {
	Kind Kind
	// Target carries the kind's target identity: job id, delegate id, child
	// session id, file path, URL, or approval content key.
	Target string
	// Timeout is the lease time-to-live from registration.
	Timeout time.Duration
	// Label is the chip-rendered short label (defaults per kind when empty).
	Label string
	// Matcher is the http_match/event matcher body (size-capped).
	Matcher string
	// EventSubtype selects the UntilEvent flavor.
	EventSubtype EventSubtype
	// Baseline is the file sha/mtime baseline for file_modified predicates.
	Baseline string
	// AskGeneration binds an UntilApproval lease to the stable turn/ask-call ID
	// of the ask_user call (never the positional transcript index), so a
	// dangling wait from an earlier same-text ask cannot validate against a
	// later generation (spec §2).
	AskGeneration string
}

// Lease is the persisted wait lease (spec §2 lease fields, §7 snapshot
// waits[]). FiredEpoch is 0 while live; the atomic claim step sets it before
// the lease leaves the registry, so it dedupes firing. IdempotencyKey is
// (kind, target identity, canonical predicate, deadline) and dedupes
// registration.
type Lease struct {
	WaitID         string
	Kind           Kind
	Predicate      WaitKind
	Label          string
	Deadline       time.Time
	RegisteredAt   time.Time
	IdempotencyKey string
	FiredEpoch     uint64
}

// Wait is a live lease in the registry. Live-only runtime state (timer
// handles, if any) attaches here in later slices; the store itself stays
// timer-free in slice 1.
type Wait struct {
	Lease Lease
}

// ID returns the lease's wait_id.
func (w Wait) ID() string { return w.Lease.WaitID }

// Live reports whether the lease is unfired and unexpired-claimed.
func (w Wait) Live() bool { return w.Lease.FiredEpoch == 0 }

// PendingWake is one consumed-but-undelivered fire (spec §1): the claim step
// moves leases out of waits[] into this persisted list, which the wake turn
// consumes. Superseded marks a wake whose goal was retargeted between claim
// and kick (spec §3). Kind records the fired lease's kind at claim time so
// consumers key on structure, never on trigger-prefix sniffing (Task-7
// Minor-5: the "child … terminal" prefix heuristic is replaced by this
// field; triggers stay human-readable excerpts).
type PendingWake struct {
	WaitID     string
	Trigger    string
	FiredAt    time.Time
	Superseded bool
	Kind       Kind
}

// Substrate is the session-owned predicate substrate that registration
// validation queries (spec §2, fail-closed). Every method is a read-only
// lookup: the second result distinguishes "live, wake-capable" from
// "retained-terminal inside the record-retention window" (which routes to
// terminal catch-up instead of parking); ok=false (or live=false) means the
// target is hallucinated or unowned and the registration is rejected with the
// reason named. Consulted in Wave B; nil in Wave A (fail-closed reject of all
// substrate kinds).
type Substrate interface {
	// LookupJob resolves a supervised-job target: live (running), or
	// retainedTerminal with the terminal outcome excerpt for catch-up.
	LookupJob(id string) (live, retainedTerminal bool, excerpt string, ok bool)
	// LookupDelegate resolves a delegate target: live (running/settling/
	// stopping), or retainedTerminal with the terminal report excerpt.
	LookupDelegate(id string) (live, retainedTerminal bool, excerpt string, ok bool)
	// StatFile resolves a file_modified target inside the session sandbox,
	// returning the current baseline for the lease.
	StatFile(path string) (baseline string, ok bool)
	// LookupApproval reports whether the (content key, ask generation) pair
	// matches a live ask. Consumed answers never match (no catch-up by
	// design, spec §2).
	LookupApproval(contentKey, generation string) (live bool)
	// LookupChild reports whether id is a known descendant session.
	LookupChild(id string) (known bool)
	// Note: the http_match CheckURL leg was removed with the subtype
	// (issue #1061). Reintroduce a fetch-backed check here when the
	// fetch-based watch type lands.
}

// PredicateEvaluator is the evaluation seam (spec §1): predicate truth is
// computed outside the pure decider (and outside the store lock — the
// hasWakePendingDependents discipline) and fed to DecideGoalStep as a
// per-wait truth array in GoalSnapshot.Waits order. Wired by the gate in
// Task 2; declared here so the signature is frozen in slice 1.
type PredicateEvaluator func(waits []Wait, now time.Time) []bool

// validKind reports whether k is one of the six registry kinds.
func validKind(k Kind) bool {
	switch k {
	case WaitUntilTime, WaitUntilJob, WaitUntilDelegate,
		WaitUntilApproval, WaitUntilEvent, WaitUntilChild:
		return true
	}
	return false
}

// checkSizeCaps enforces the spec §2 model-controlled field caps. Over-cap or
// non-printable-label predicates reject fail-closed at validation.
func checkSizeCaps(req WaitKind) bool {
	if len(req.Matcher) > MaxMatcherBytes {
		return false
	}
	if len(req.Target) > MaxURLBytes {
		return false
	}
	if utf8.RuneCountInString(req.Label) > MaxLabelRunes {
		return false
	}
	for _, r := range req.Label {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// canonicalPredicate renders the stable predicate identity for the
// idempotency key: same target re-registered with a different
// deadline/predicate must replace, while an identical re-register dedupes
// (spec §2). The chip label is presentation-only and excluded.
func canonicalPredicate(req WaitKind) string {
	var b strings.Builder
	b.WriteString(string(req.Kind))
	b.WriteByte(0)
	b.WriteString(req.Target)
	b.WriteByte(0)
	b.WriteString(string(req.EventSubtype))
	b.WriteByte(0)
	b.WriteString(req.Baseline)
	b.WriteByte(0)
	b.WriteString(req.Matcher)
	b.WriteByte(0)
	b.WriteString(req.AskGeneration)
	return b.String()
}

// idempotencyKey is (kind, target identity, canonical predicate, deadline).
func idempotencyKey(req WaitKind, deadline time.Time) string {
	return fmt.Sprintf("%s\x00%d", canonicalPredicate(req), deadline.UnixNano())
}

// defaultLabelFor derives the chip label when the registrant omits one.
func defaultLabelFor(req WaitKind) string {
	if req.Label != "" {
		return req.Label
	}
	if req.Target != "" {
		return string(req.Kind) + ":" + req.Target
	}
	return string(req.Kind)
}

// Note: the http_match URL helpers (ValidHTTPURL, the CheckURL egress
// gate, DNS/legacy-numeric parsing) were removed with the subtype
// (issue #1061). The fetch-based watch type reintroduces them alongside
// the fetch leg.
