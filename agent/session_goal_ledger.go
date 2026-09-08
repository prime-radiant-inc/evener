package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Slice-2 ledger wiring (spec §4): the per-turn evidence accumulator, the
// pre-scoped TurnOutcome builder, the gate-fold entry point, and the child
// terminal forward (§8).

// goalTurnCallEvidence is one tool call's ledger evidence: the raw material
// the gate folds into a TurnOutcome. Fingerprint construction normalizes
// tool + args here; the ledger canonicalizer refines it at fold time.
type goalTurnCallEvidence struct {
	tool string
	args string
	// class is the observation class (ok | empty | error | timeout |
	// approval-pending | external-unchanged); hash is the sha256 of the raw
	// observation output ("" = class-only fallback). The fold canonicalizes
	// the hash input for timestamp/id noise before novelty comparison.
	class string
	hash  string
	// mutated mirrors callsMadeProgress for this call (real mutating action).
	mutated bool
}

// Per-tool fingerprint normalization table (spec §4 appendix, grown at the
// fingerprint-construction site): refining args stay (a refining grep stays
// distinct from the broad one that preceded it), volatile flags redact to a
// stable token so identical retries compare identical.
var goalFingerprintVolatileArgs = map[string]map[string]bool{
	// Pagination / windowing flags: the flag VALUE is volatile across retries.
	"list_dir":   {"offset": true, "limit": true},
	"glob":       {"offset": true, "limit": true},
	"grep":       {"offset": true, "limit": true},
	"job_status": {"offset": true, "limit": true},
	"watch_list": {"offset": true, "limit": true},
	// Identity / addressing flags: the value names which run/retry this is.
	"delegate_send":   {"attempt_id": true, "nonce": true, "request_id": true},
	"job_start":       {"attempt_id": true, "nonce": true},
	"communicate":     {"nonce": true},
	"ask_user":        {"nonce": true},
	"goal_wait":       {"nonce": true},
	"update_goal":     {"nonce": true},
	"task_list":       {"nonce": true},
	"read_file":       {"nonce": true},
	"web_fetch":       {"nonce": true},
	"exec":            {"nonce": true, "attempt_id": true},
	"run_command":     {"nonce": true, "attempt_id": true},
	"shell":           {"nonce": true, "attempt_id": true},
	"apply_patch":     {"nonce": true},
	"manage_worktree": {"nonce": true},
}

// goalFingerprintForCall builds the raw action fingerprint for one tool call:
// tool name + normalized args. Refining args are preserved verbatim (so a
// narrowing grep never collides with the broad one); per-tool volatile flags
// redact to <id>; timestamps/nonces inside values redact via the ledger
// canonicalizer at fold time. Args arrive as the raw JSON object the provider
// sent; unparseable args degrade to the tool name alone (never an error —
// the ledger is syntactic evidence, not validation).
func goalFingerprintForCall(name, rawArgs string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.TrimSpace(rawArgs) == "" || strings.TrimSpace(rawArgs) == "{}" {
		return name
	}
	flat := flattenGoalArgs(rawArgs)
	if len(flat) == 0 {
		return name
	}
	volatile := goalFingerprintVolatileArgs[name]
	var parts []string
	for _, kv := range flat {
		if volatile[kv.key] {
			parts = append(parts, kv.key+"=<id>")
			continue
		}
		parts = append(parts, kv.key+"="+kv.value)
	}
	sort.Strings(parts)
	return name + " " + strings.Join(parts, " ")
}

// goalFlatArg is one sorted key=value pair from the call's JSON args.
type goalFlatArg struct {
	key   string
	value string
}

// flattenGoalArgs flattens one level of the JSON args object into sorted
// key=value pairs. Nested objects/arrays render compactly so a refining
// nested filter still distinguishes the call. Values truncate at 512 chars:
// fingerprints are evidence keys, not transcripts. intent and
// thought_signature are transport, not action, and are excluded.
func flattenGoalArgs(raw string) []goalFlatArg {
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil
	}
	var out []goalFlatArg
	for k, v := range obj {
		if k == "intent" || k == "thought_signature" {
			continue
		}
		out = append(out, goalFlatArg{key: k, value: goalArgScalar(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// goalArgScalar renders one arg value compactly.
func goalArgScalar(v any) string {
	s := fmt.Sprint(v)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// goalObservationClass maps one tool result onto the §4 observation class.
func goalObservationClass(result tool.ExecResult) string {
	if result.IsError {
		return "error"
	}
	out := strings.TrimSpace(result.Output)
	if out == "" {
		return "empty"
	}
	lower := strings.ToLower(out)
	switch {
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline exceeded"):
		return "timeout"
	case strings.Contains(lower, "awaiting approval") || strings.Contains(lower, "approval-pending") || strings.Contains(lower, "approval pending"):
		return "approval-pending"
	case strings.Contains(lower, "unchanged") || strings.Contains(lower, "no new") || strings.Contains(lower, "not modified"):
		return "external-unchanged"
	default:
		return "ok"
	}
}

// recordGoalTurnEvidence appends one round's per-call evidence to the session
// accumulator. Called from the round loop after results land (persisted or
// not — evidence is about what the turn DID, not what survived a persist
// failure). Locking: takes s.mu only (resultToolName self-locks; read it
// before holding s.mu).
func (s *Session) recordGoalTurnEvidence(calls []llm.ToolCallData, results []tool.ExecResult) {
	if len(calls) == 0 {
		return
	}
	resultName := s.resultToolName()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, call := range calls {
		var rawArgs string
		if len(call.Arguments) > 0 {
			rawArgs = string(call.Arguments)
		}
		class, hash := "ok", ""
		if i < len(results) {
			class = goalObservationClass(results[i])
			if out := strings.TrimSpace(results[i].Output); out != "" {
				sum := sha256.Sum256([]byte(out))
				hash = hex.EncodeToString(sum[:])
			}
		}
		mutated := false
		if call.Name != resultName && call.Name != "task_list" {
			t := s.reg.Get(call.Name)
			if t == nil || !t.ReadOnly {
				mutated = true
			}
		}
		s.goalTurnEvidence = append(s.goalTurnEvidence, goalTurnCallEvidence{
			tool:    call.Name,
			args:    rawArgs,
			class:   class,
			hash:    hash,
			mutated: mutated,
		})
	}
}

// takeGoalTurnEvidence drains the session accumulator. Self-locking; call
// with no session locks held.
func (s *Session) takeGoalTurnEvidence() []goalTurnCallEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.goalTurnEvidence
	s.goalTurnEvidence = nil
	return ev
}

// buildGoalTurnOutcome folds one turn's evidence into a pre-scoped
// TurnOutcome (spec §4): the action fingerprint joins every call in the turn
// (multi-call turns read as one compound action — repetition means the whole
// turn repeated); the observation class/hash join likewise; mutated is the
// turn-level OR. The state digest arrives pre-scoped from goalStateDigest
// (job/delegate/watch/wait/file names+sizes+mtimes — never history).
func buildGoalTurnOutcome(evidence []goalTurnCallEvidence, digest string) goal.TurnOutcome {
	if len(evidence) == 0 {
		return goal.TurnOutcome{ObservationClass: "empty", StateDigest: digest}
	}
	var fps, classes, hashes []string
	mutated := false
	for _, ev := range evidence {
		fps = append(fps, goalFingerprintForCall(ev.tool, ev.args))
		classes = append(classes, ev.class)
		if ev.hash != "" {
			hashes = append(hashes, ev.hash)
		}
		mutated = mutated || ev.mutated
	}
	return goal.TurnOutcome{
		ActionFingerprint: strings.Join(fps, "\n"),
		ObservationClass:  strings.Join(classes, "+"),
		ObservationHash:   strings.Join(hashes, "\n"),
		StateDigest:       digest,
		Mutated:           mutated,
	}
}

// goalStateDigest computes the pre-scoped state digest (spec §4: job phases +
// delegate phases + watch conditions + goal wait predicates + worktree
// file-listing digest — names + sizes + mtimes, not full contents).
// History-covering digests are forbidden here by construction: no history is
// read. Controller/job-manager reads are §3-top pre-reads (acquire, read,
// release — never held across the claim). Fail-closed on read errors: an
// unreadable scope contributes its absence marker, never a fabricated digest.
func (s *Session) goalStateDigest() string {
	var b strings.Builder
	// Job phases: running job ids + statuses, sorted by job id (map
	// iteration order is nondeterministic — an unsorted render would emit
	// spurious digest deltas that reset repetition and deaden the breaker).
	if jm := s.jobManager; jm != nil {
		jm.mu.Lock()
		ids := make([]string, 0, len(jm.running))
		for id := range jm.running {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			status := ""
			if r := jm.running[id]; r != nil && r.rec != nil {
				status = string(r.rec.Status)
			}
			fmt.Fprintf(&b, "job %s %s\n", id, status)
		}
		keys := make([]string, 0, len(jm.watches))
		for key := range jm.watches {
			keys = append(keys, key.Target+"\x00"+key.VisibleSessionID+"\x00"+key.SendTo+"\x00"+key.ReceiverSessionID+"\x00"+key.ReceiverDelegateID+"\x00"+key.Slot)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "watch %s\n", k)
		}
		jm.mu.Unlock()
	}
	// Delegate phases: delegate id + phase, sorted by delegate id (same
	// map-order discipline as above).
	if c := s.delegateController; c != nil {
		c.mu.Lock()
		ids := make([]string, 0, len(c.durable))
		for id := range c.durable {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if agg := c.durable[id]; agg == nil {
				continue
			}
			fmt.Fprintf(&b, "delegate %s %s\n", id, c.durable[id].Phase)
		}
		c.mu.Unlock()
	}
	// Goal wait predicates: wait id + kind + target + deadline.
	if full, ok := s.getOrCreateGoalStore().GoalSnapshot(); ok {
		for _, w := range full.Waits {
			fmt.Fprintf(&b, "wait %s %s %s %d\n", w.Lease.WaitID, w.Lease.Kind, w.Lease.Predicate.Target, w.Lease.Deadline.UnixNano())
		}
	}
	// Worktree file-listing digest: names + sizes (+mtimes where known).
	b.WriteString(worktreeListingDigest(s.env.WorkingDirectory()))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// worktreeListingDigest renders the depth-1 worktree listing as
// name/size/mtime lines. A missing root (or a read error) contributes the
// absence marker — the digest still compares deterministically; a reappearing
// root then reads as a delta, which is the honest signal.
func worktreeListingDigest(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "worktree <unavailable>\n"
	}
	names := make([]string, 0, len(entries))
	byName := make(map[string]os.DirEntry, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		byName[e.Name()] = e
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		e := byName[name]
		var size int64
		var mtime string
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
			mtime = fi.ModTime().UTC().Format("2006-01-02T15:04:05")
		}
		fmt.Fprintf(&b, "file %s %d %s\n", filepath.Join(root, name), size, mtime)
	}
	return b.String()
}

// armGoalContinuationWithOutcome is the gate entry point with an explicit
// TurnOutcome (spec §4 fold wiring): the plain-drive path folds the outcome
// into the ledger. Tests pass explicit outcomes; the production turn tail
// (session_lifecycle.go) builds the outcome from the turn's recorded evidence
// plus a fresh pre-scoped digest and calls this same seam.
func (s *Session) armGoalContinuationWithOutcome(progressed, wasContinuation bool, outcome goal.TurnOutcome) (string, bool) {
	return s.armGoalContinuationInner(progressed, wasContinuation, &outcome)
}

// forwardChildTerminalToWaits implements the §8 parent→child forward: when a
// direct child reaches a terminal status, every until_child lease naming that
// child (on the parent or on a live sibling waiter) claims exactly once with
// the terminal report as the trigger excerpt. Terminal-only: intermediate
// chatter never matches. Gate-before-claim: stop-gated/fatal-gated waiter
// sessions are never claimed into — they get the honest loss notice on their
// next turn tail instead. Cross-session reads take the waiter's locks as
// §3-top pre-reads, never held across the claim.
func (s *Session) forwardChildTerminalToWaits(done *subagent) {
	if s == nil || done == nil || done.sess == nil {
		return
	}
	done.mu.Lock()
	terminal := terminalStatus(done.status)
	doneID := done.id
	doneResult := done.result
	done.mu.Unlock()
	if !terminal {
		return // terminal-only matching: intermediate chatter never fires
	}
	trigger := "child " + doneID + " terminal"
	if strings.TrimSpace(doneResult) != "" {
		trigger += ": " + strings.TrimSpace(doneResult)
	}
	// Parent's own waits (the common until_child-on-my-child shape).
	s.claimChildWaitsForTerminal(doneID, trigger)
	// Sibling waiters: live direct children holding until_child on doneID.
	// Gate checks use the production key domains (mirroring
	// driveChildIfNotStopGated): childStopGated/childFatalRunGated take the
	// child SESSION id. childStopGated matches stable-delegate descriptor
	// ChildSessionIDs; in-process children without a stable row read
	// ungated there (fail-open toward delivery), while childFatalRunGated
	// resolves the tracked record by subagent id — so the forward consults
	// BOTH domains: the session id for the stable stop gate and the
	// subagent id for the fatal-run gate.
	for _, sub := range s.subagents.directSubagents() {
		if sub == nil || sub.sess == nil || sub.sess == s {
			continue
		}
		if goalWaitSiblingStopGated(s, sub) || s.childFatalRunGated(sub.id) {
			// Gate-before-claim: never consume what cannot be driven. The
			// lease stays live; the honest loss notice routes to the
			// waiter's next turn tail.
			sub.sess.appendTurn(schema.TurnSteering, llm.User(goalWaitTerminalForwardNote(doneID)))
			continue
		}
		if !claimChildWaitsForTerminalOn(sub.sess, doneID, trigger, s.sclock().Now()) {
			continue
		}
		// Claimed: drive the waiter's wake turn through the delegate-drive
		// seam (spec §8: the forward drives the child's wake turn through
		// the existing delegate-drive path). Delegate children own no
		// kickFunc, so the waiter's coalesced timer alone would strand the
		// backlog — the drive is what delivers it. A refused drive (busy,
		// gated, at budget) leaves the claim persisted: the waiter's next
		// gate/settle drives the wake inline from the backlog, so the claim
		// is never lost, only deferred.
		s.driveSubagentNotificationTurn(sub)
	}
}

// goalWaitSiblingStopGated reports the stable stop gate for a sibling waiter,
// honoring the test hook like the production re-drive path
// (redriveChildIfAttentionRemains): childStopGated over the child session id,
// overridden by cfg.testOnly.subagentStopGated when handled.
func goalWaitSiblingStopGated(s *Session, sub *subagent) bool {
	gated := s.childStopGated(sub.sess.id)
	if hook := s.cfg.testOnly.subagentStopGated; hook != nil {
		if stopped, handled := hook(s, sub.sess.id); handled {
			gated = stopped
		}
	}
	return gated
}

// claimChildWaitsForTerminal claims s's own until_child leases on a terminal
// child. ClaimFire's atomicity is the exactly-once guarantee (double-claim
// collapses to one wake); the coalesced timer re-arms to the new state.
func (s *Session) claimChildWaitsForTerminal(childID, trigger string) {
	now := s.sclock().Now()
	s.goalUpdateMu.Lock()
	claimed := s.getOrCreateGoalStore().ClaimChildWaits(childID, trigger, now)
	s.goalUpdateMu.Unlock()
	if claimed {
		s.armGoalWaitTimer()
	}
}

// claimChildWaitsForTerminalOn claims one waiter session's until_child leases.
// The waiter's delivered-set mark is the cross-session single-wake collapse:
// a wait already marked delivered collapses instead of re-kicking. Reports
// whether any lease claimed (the caller drives the wake turn on true).
func claimChildWaitsForTerminalOn(waiter *Session, childID, trigger string, now time.Time) bool {
	waiter.goalUpdateMu.Lock()
	claimed := waiter.getOrCreateGoalStore().ClaimChildWaits(childID, trigger, now)
	waiter.goalUpdateMu.Unlock()
	if claimed {
		waiter.armGoalWaitTimer()
	}
	return claimed
}

// goalWaitTerminalForwardNote is the loss-notice text for a gated forward.
func goalWaitTerminalForwardNote(childID string) string {
	return goalWaitNoticePrefix + " Child terminal forward for " + childID + " withheld: the waiting session is stop-gated. Re-arm the wait or proceed without it."
}

// goalWaitForwardOwner resolves the session that owns the §8 forward for a
// terminal child run: the parent session tracking this session as a direct
// child. Resolution walks the parent's tracked set (never the child's own
// locks held across the forward). Nil when no parent tracks this session
// (root sessions, detached children, test-direct sessions).
func (s *Session) goalWaitForwardOwner() *Session {
	if s == nil || s.delegateController == nil {
		return nil
	}
	c := s.delegateController
	c.mu.Lock()
	root := c.rootRuntime
	c.mu.Unlock()
	// The child resolves its owner through the tracked parent sets: the
	// session whose subagentManager tracks this session as a direct child
	// owns the forward. The root is the common case; live non-root runtimes
	// are covered by the same scan.
	candidates := []*Session{root}
	c.mu.Lock()
	for _, live := range c.live {
		if live != nil && live.runtime != nil && live.runtime != s && live.runtime != root {
			candidates = append(candidates, live.runtime)
		}
	}
	c.mu.Unlock()
	for _, parent := range candidates {
		if parent == nil || parent.subagents == nil {
			continue
		}
		for _, sub := range parent.subagents.directSubagents() {
			if sub != nil && sub.sess == s {
				return parent
			}
		}
	}
	return nil
}

// forwardSnapshot captures the terminal identity the forward matches on: the
// subagent record itself (status + result read under its own lock inside the
// forward). The hook passes the live record; the forward re-reads terminality
// so a status that raced past terminal still gates correctly.
func (a *subagent) forwardSnapshot() *subagent { return a }
