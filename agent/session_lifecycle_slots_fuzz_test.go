//go:build serffuzz

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provenance"
)

// This file fuzzes four session-lifecycle + subagent-slot cores:
//
//   - Session.DrainAsSteerWithInput (session_queue.go): the atomic force-steer
//     path that appends an optional composer payload, drains the whole input
//     queue, and injects it as ONE steering message. Fuzzed with a program of
//     enqueue/state-flip/drain/pop/close ops.
//   - Session.discardRestoredCandidate (session_lifecycle.go): the restored-
//     candidate teardown twin of Close (closeOnce-guarded, recursive over live
//     children). Fuzzed with an interleaving of discard/Close teardown calls on a
//     parent that may own spawned children.
//   - subagentManager.reserveSlot (subagent_manager.go): the retained-terminal
//     cap GC that evicts reclaimable records before a spawn. Fuzzed with an
//     adversarial population of child records + cap + repeat count.
//   - Session.initPlugins (session_init.go): the plugin-load + merge step. Fuzzed
//     against real plugin dirs materialized under t.TempDir with fuzz-selected
//     manifests, skills, agents, hooks, and mcp configs.
//
// ORACLES (lcyc_):
//   - never-panic: no decoded program panics any target.
//   - drain order-consistency: a successful drain injects exactly one steering
//     message whose text is the FIFO "\n\n"-join of the non-empty queued texts (an
//     all-empty drain injects none), the input queue is left empty, and image
//     attachments carry through in order. An error path leaves the queue unmutated.
//   - slot accounting: every reserveSlot eviction removes a distinct, still-present,
//     reclaimable terminal record; a successful reserve leaves the counted-terminal
//     population below the cap; an error evicts nothing.
//   - teardown post-state: after any teardown interleaving the session is Closed and
//     re-teardown is a no-op.
//   - determinism: the same program replayed on a fresh session/manager yields a
//     byte-identical projection (drain trace, reserveSlot eviction sequence, plugin
//     merge result).
//
// Anti-collision: every top-level identifier is prefixed lcyc. The byte->spec
// decode reuses the in-package seqReader cursor and the safzNewParent /
// newSession scaffolding rather than redefining them.

// --- shared vocab ---

var lcycDrainTexts = []string{"", "  ", "hello", "line1\nline2", "  padded  ", "résumé", "step 2"}

// lcycImages builds n deterministic image attachments (distinct data bytes so an
// ordering bug in the carry-through is observable).
func lcycImages(n int) []ImageAttachment {
	if n <= 0 {
		return nil
	}
	out := make([]ImageAttachment, n)
	for i := range out {
		out[i] = ImageAttachment{MediaType: "image/png", Data: []byte{byte(i)}, Name: "img"}
	}
	return out
}

// lcycProv returns a fixed non-empty causal provenance so the drain's
// provenance.Union fold is exercised deterministically.
func lcycProv() *provenance.Causal {
	return &provenance.Causal{WatchKeys: []provenance.WatchKey{{WatchID: "lcyc_w", WatchGeneration: "1"}}}
}

// ============================================================================
// DrainAsSteerWithInput
// ============================================================================

// lcycDrainOp is one decoded queue/steer instruction.
type lcycDrainOp struct {
	code   int // 0 enqueue, 1 -> Processing, 2 -> Idle, 3 drain, 4 pop-head, 5 close
	text   string
	images int
	prov   bool
}

// lcycDecodeDrain maps bytes to a stable op program (append fields, never
// reorder, so the persisted corpus keeps its meaning).
func lcycDecodeDrain(data []byte) []lcycDrainOp {
	r := &seqReader{data: data}
	n := r.intn(12) + 1
	ops := make([]lcycDrainOp, 0, n)
	for i := 0; i < n; i++ {
		op := lcycDrainOp{code: r.intn(6)}
		op.text = lcycDrainTexts[r.intn(len(lcycDrainTexts))]
		op.images = r.intn(3)
		op.prov = r.intn(2) == 1
		ops = append(ops, op)
	}
	return ops
}

// FuzzLcyc_DrainAsSteer drives DrainAsSteerWithInput across its closed / no-turn
// / empty-queue / success branches with a fuzzed op program, asserting the
// order-consistency oracle after each drain and the determinism oracle across a
// replay on a fresh session.
func FuzzLcyc_DrainAsSteer(f *testing.F) {
	// Byte layout: data[0]->op count (%12+1); then 4 bytes per op
	// (code%6, text%7, images%3, prov%2).
	seeds := [][]byte{
		{},
		// enqueue "hello", enqueue "step 2", enqueue image+prov, processing,
		// drain with "résumé"+image, pop -> success join of 3 texts + 3 images.
		{5, 0, 2, 0, 0, 0, 6, 0, 0, 0, 0, 2, 1, 1, 0, 0, 0, 3, 5, 1, 0, 4, 0, 0, 0},
		{2, 0, 2, 0, 0, 5, 0, 0, 0, 3, 0, 0, 0}, // enqueue, close, drain -> closed branch
		{0, 3, 0, 0, 0},                         // drain on idle -> no-turn branch
		{1, 1, 0, 0, 0, 3, 0, 0, 0},             // processing, drain empty -> queue-empty branch
		{2, 0, 1, 0, 0, 1, 0, 0, 0, 3, 0, 0, 0}, // blank entry, processing, drain -> success, no steer
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		ops := lcycDecodeDrain(data)
		d1 := lcycRunDrain(t, lcycNewDrainSession(t), ops)
		d2 := lcycRunDrain(t, lcycNewDrainSession(t), ops)
		if d1 != d2 {
			t.Fatalf("determinism: drain trace differs:\n first: %s\nsecond: %s", d1, d2)
		}
	})
}

func lcycNewDrainSession(t *testing.T) *Session {
	t.Helper()
	// emit() is non-blocking (drop-on-full), so no events drain goroutine is
	// needed; newSession registers Close via t.Cleanup.
	return newSession(t)
}

// lcycRunDrain executes the op program, asserting the per-drain order-consistency
// oracle, and returns a deterministic trace of the drain outcomes.
func lcycRunDrain(t *testing.T, sess *Session, ops []lcycDrainOp) string {
	t.Helper()
	var sb strings.Builder
	for idx, op := range ops {
		switch op.code {
		case 0: // enqueue directly so empty-text / image-only / provenance entries are reachable
			entry := queuedInput{Text: op.text, Images: lcycImages(op.images)}
			if op.prov {
				entry.Provenance = lcycProv()
			}
			sess.mu.Lock()
			if !sess.closingOrClosedLocked() {
				sess.inputQueue = append(sess.inputQueue, entry)
			}
			sess.mu.Unlock()
		case 1:
			sess.mu.Lock()
			sess.setStateIfOpenLocked(SessionProcessing)
			sess.mu.Unlock()
		case 2:
			sess.mu.Lock()
			sess.setStateIfOpenLocked(SessionIdle)
			sess.mu.Unlock()
		case 3:
			lcycCheckDrain(t, sess, op, idx, &sb)
		case 4:
			sess.popQueueHead()
		case 5:
			sess.Close()
		}
	}
	return sb.String()
}

// lcycCheckDrain snapshots the pre-drain state, computes the expected disposition,
// runs the drain, and asserts the order-consistency oracle.
func lcycCheckDrain(t *testing.T, sess *Session, op lcycDrainOp, idx int, sb *strings.Builder) {
	t.Helper()
	images := lcycImages(op.images)

	sess.mu.Lock()
	closed := sess.closingOrClosedLocked()
	processing := sess.state == SessionProcessing
	queueSnap := append([]queuedInput{}, sess.inputQueue...)
	preSteer := len(sess.steeringQueue)
	sess.mu.Unlock()

	// The drive payload is appended to the queue only when it carries real content.
	driveAdded := strings.TrimSpace(op.text) != "" || len(images) > 0
	effective := append([]queuedInput{}, queueSnap...)
	if driveAdded {
		effective = append(effective, queuedInput{Text: op.text, Images: images})
	}

	var wantErr string
	switch {
	case closed:
		wantErr = "drain: session is closed"
	case !processing:
		wantErr = "drain: no active turn to steer"
	case len(effective) == 0:
		wantErr = "drain: queue is empty"
	}

	texts := make([]string, 0, len(effective))
	imgCount := 0
	for _, e := range effective {
		if strings.TrimSpace(e.Text) != "" {
			texts = append(texts, e.Text)
		}
		imgCount += len(e.Images)
	}
	combined := strings.Join(texts, "\n\n")
	// A steering message is injected only when the drained payload is non-blank
	// (trySteer rejects a blank text with no images).
	steerAdded := wantErr == "" && (strings.TrimSpace(combined) != "" || imgCount > 0)

	err := sess.DrainAsSteerWithInput(context.Background(), op.text, images)

	if wantErr == "" {
		if err != nil {
			t.Fatalf("drain op %d: unexpected error %v", idx, err)
		}
		sess.mu.Lock()
		queueLen := len(sess.inputQueue)
		postSteer := len(sess.steeringQueue)
		var last steeringMessage
		if postSteer > 0 {
			last = sess.steeringQueue[postSteer-1]
		}
		sess.mu.Unlock()
		if queueLen != 0 {
			t.Fatalf("drain op %d: input queue not drained: %d left", idx, queueLen)
		}
		if steerAdded {
			if postSteer != preSteer+1 {
				t.Fatalf("drain op %d: steering count %d -> %d, want +1", idx, preSteer, postSteer)
			}
			if last.Text != combined {
				t.Fatalf("drain op %d: steering text %q != expected FIFO join %q", idx, last.Text, combined)
			}
			if len(last.Images) != imgCount {
				t.Fatalf("drain op %d: steering images %d != expected %d", idx, len(last.Images), imgCount)
			}
		} else if postSteer != preSteer {
			t.Fatalf("drain op %d: blank drain injected steering (%d -> %d)", idx, preSteer, postSteer)
		}
		fmt.Fprintf(sb, "d%d:ok,steer=%v,text=%q,img=%d;", idx, steerAdded, combined, imgCount)
	} else {
		if err == nil || err.Error() != wantErr {
			t.Fatalf("drain op %d: err %v, want %q", idx, err, wantErr)
		}
		sess.mu.Lock()
		queueLen := len(sess.inputQueue)
		sess.mu.Unlock()
		if queueLen != len(queueSnap) {
			t.Fatalf("drain op %d: error path mutated queue %d -> %d", idx, len(queueSnap), queueLen)
		}
		fmt.Fprintf(sb, "d%d:err=%q;", idx, wantErr)
	}
}

// ============================================================================
// discardRestoredCandidate
// ============================================================================

// FuzzLcyc_DiscardRestoredCandidate spawns a fuzzed number of children, then runs
// a fuzzed interleaving of discardRestoredCandidate/Close teardown calls,
// asserting the never-panic + closed-post-state + idempotence oracles.
func FuzzLcyc_DiscardRestoredCandidate(f *testing.F) {
	seeds := [][]byte{
		{},
		{0, 0},          // no children, single discard
		{1, 0, 1, 1},    // one child, discard-then-close
		{2, 1, 0, 1, 0}, // two children, close-then-discard
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &seqReader{data: data}
		nChildren := r.intn(3)
		nTeardown := r.intn(4) + 1
		teardown := make([]int, nTeardown)
		for i := range teardown {
			teardown[i] = r.intn(2) // 0 discard, 1 close
		}

		clk := agenttest.NewFakeClock()
		parent := safzNewParent(t, clk, 2, nil, &agenttest.DenyEnv{WorkDir: t.TempDir()})

		for i := 0; i < nChildren; i++ {
			if _, err := parent.spawnAgent(context.Background(), "child task", "", "", 0, "", "", nil, nil); err != nil {
				break // allowance exhausted; the teardown oracle still runs
			}
		}

		for _, code := range teardown {
			if code == 0 {
				parent.discardRestoredCandidate()
			} else {
				parent.Close()
			}
			if st := parent.State(); st != SessionClosed {
				t.Fatalf("after teardown the session is %q, want %q", st, SessionClosed)
			}
		}
		// Idempotence: a final round of both must remain a no-op.
		parent.discardRestoredCandidate()
		parent.Close()
		if st := parent.State(); st != SessionClosed {
			t.Fatalf("post-idempotence state %q, want %q", st, SessionClosed)
		}
	})
}

// ============================================================================
// reserveSlot
// ============================================================================

var lcycStatuses = []SubagentStatus{SubagentRunning, SubagentCompleted, SubagentFailed, SubagentCancelled, ""}

// lcycSubSpec is one decoded child record shape.
type lcycSubSpec struct {
	status        SubagentStatus
	consumed      bool
	closed        bool
	closeTimedOut bool
}

// lcycReserveSpec is the decoded reserveSlot program.
type lcycReserveSpec struct {
	cap    int
	subs   []lcycSubSpec
	rounds int
}

func lcycDecodeReserve(data []byte) lcycReserveSpec {
	r := &seqReader{data: data}
	spec := lcycReserveSpec{cap: r.intn(4) + 1}
	n := r.intn(10)
	for i := 0; i < n; i++ {
		spec.subs = append(spec.subs, lcycSubSpec{
			status:        lcycStatuses[r.intn(len(lcycStatuses))],
			consumed:      r.intn(2) == 1,
			closed:        r.intn(2) == 1,
			closeTimedOut: r.intn(2) == 1,
		})
	}
	spec.rounds = r.intn(3) + 1
	return spec
}

// lcycBuildManager materializes an identical manager from a spec (distinct
// endedAt per record so the eviction sort is a total order and thus
// deterministic).
func lcycBuildManager(spec lcycReserveSpec) *subagentManager {
	m := newSubagentManager(nil)
	m.maxRetainedTerminal = spec.cap
	base := time.Unix(1000, 0)
	for i, ss := range spec.subs {
		id := fmt.Sprintf("lcyc_%d", i)
		ended := base.Add(time.Duration(i) * time.Second)
		m.subs[id] = &subagent{
			id:             id,
			status:         ss.status,
			resultConsumed: ss.consumed,
			closed:         ss.closed,
			closeTimedOut:  ss.closeTimedOut,
			endedAt:        &ended,
		}
	}
	return m
}

// FuzzLcyc_ReserveSlot builds an adversarial child population + cap and runs
// reserveSlot repeatedly, asserting the slot-accounting oracle after each call
// and the determinism oracle across a fresh rebuild.
func FuzzLcyc_ReserveSlot(f *testing.F) {
	// Byte layout: data[0]->cap (%4+1); next->sub count (%10); then 4 bytes per
	// sub (status%5, consumed%2, closed%2, closeTimedOut%2); then rounds (%3+1).
	seeds := [][]byte{
		{},
		{0, 2, 1, 1, 0, 0, 1, 1, 0, 0, 1}, // cap1, two completed-consumed -> eviction, then below-cap
		{0, 2, 1, 0, 0, 0, 1, 0, 0, 0, 0}, // cap1, two completed-unconsumed -> reclaim-nothing error
		{0, 2, 1, 0, 1, 0, 1, 1, 0, 0, 1}, // cap1, a closed record evicted before a consumed one
		{2, 3, 0, 0, 0, 1, 0, 0, 0, 1, 1, 1, 1, 0, 2}, // running + closeTimedOut non-counting
		{3, 1, 1, 1, 0, 0, 1},                         // cap4, one terminal -> below-cap early return
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		spec := lcycDecodeReserve(data)
		trace1 := lcycRunReserve(t, lcycBuildManager(spec), spec.rounds)
		trace2 := lcycRunReserve(t, lcycBuildManager(spec), spec.rounds)
		if trace1 != trace2 {
			t.Fatalf("determinism: reserveSlot trace differs:\n first: %s\nsecond: %s", trace1, trace2)
		}
	})
}

// lcycRunReserve calls reserveSlot `rounds` times, asserting accounting after each.
func lcycRunReserve(t *testing.T, m *subagentManager, rounds int) string {
	t.Helper()
	var sb strings.Builder
	for round := 0; round < rounds; round++ {
		// Snapshot the pre-call population so we can verify evictions.
		pre := lcycSnapshotSubs(m)

		evicted, err := m.reserveSlot()

		post := lcycSnapshotSubs(m)
		seen := map[string]bool{}
		for _, sub := range evicted {
			if sub == nil {
				t.Fatalf("round %d: reserveSlot returned a nil evicted record", round)
			}
			id := sub.id
			if seen[id] {
				t.Fatalf("round %d: double-free — id %q evicted twice", round, id)
			}
			seen[id] = true
			ps, ok := pre[id]
			if !ok {
				t.Fatalf("round %d: evicted id %q was not present before the call", round, id)
			}
			if !terminalStatus(ps.status) || ps.closeTimedOut {
				t.Fatalf("round %d: evicted non-counting record %q (status=%q timedOut=%v)", round, id, ps.status, ps.closeTimedOut)
			}
			if !ps.closed && !ps.consumed {
				t.Fatalf("round %d: evicted unreclaimable record %q (closed=%v consumed=%v)", round, id, ps.closed, ps.consumed)
			}
			if _, still := post[id]; still {
				t.Fatalf("round %d: evicted id %q still present in the manager", round, id)
			}
		}

		countedAfter := 0
		for _, st := range post {
			if countsTowardCap(st.status, st.closeTimedOut) {
				countedAfter++
			}
		}
		if err != nil {
			if len(evicted) != 0 {
				t.Fatalf("round %d: reserveSlot errored but evicted %d records", round, len(evicted))
			}
			if len(post) != len(pre) {
				t.Fatalf("round %d: reserveSlot errored but mutated the population %d -> %d", round, len(pre), len(post))
			}
		} else if countedAfter >= m.maxRetainedTerminal {
			t.Fatalf("round %d: reserveSlot succeeded but counted-terminal %d still >= cap %d", round, countedAfter, m.maxRetainedTerminal)
		}

		ids := make([]string, 0, len(seen))
		for id := range seen {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Fprintf(&sb, "r%d:err=%v,ev=%v,counted=%d;", round, err != nil, ids, countedAfter)
	}
	return sb.String()
}

type lcycSubSnap struct {
	status        SubagentStatus
	consumed      bool
	closed        bool
	closeTimedOut bool
}

func lcycSnapshotSubs(m *subagentManager) map[string]lcycSubSnap {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]lcycSubSnap, len(m.subs))
	for id, sub := range m.subs {
		sub.mu.Lock()
		out[id] = lcycSubSnap{
			status:        sub.status,
			consumed:      sub.resultConsumed,
			closed:        sub.closed,
			closeTimedOut: sub.closeTimedOut,
		}
		sub.mu.Unlock()
	}
	return out
}

// ============================================================================
// initPlugins
// ============================================================================

// lcycPluginSpec is one decoded plugin dir shape.
type lcycPluginSpec struct {
	name         string
	skill        bool
	agent        bool
	hooks        bool
	badMatcher   bool
	reservedType bool
	mcp          bool
}

// lcycDecodeInit decodes the plugin-dir program plus the SessionStart kind.
func lcycDecodeInit(data []byte) (specs []lcycPluginSpec, kind plugin.SessionStartKind) {
	r := &seqReader{data: data}
	n := r.intn(3) + 1
	kinds := []plugin.SessionStartKind{
		plugin.SessionStartKindStartup, plugin.SessionStartKindResume,
		plugin.SessionStartKindClear, plugin.SessionStartKindCompact, "",
	}
	kind = kinds[r.intn(len(kinds))]
	// A collide bit forces two dirs to share a name so LoadAll's duplicate-name
	// error branch is reachable.
	collide := r.intn(3) == 0
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("lcyc-plugin-%d", i)
		if collide {
			name = "lcyc-plugin-dup"
		}
		specs = append(specs, lcycPluginSpec{
			name:         name,
			skill:        r.intn(2) == 1,
			agent:        r.intn(2) == 1,
			hooks:        r.intn(2) == 1,
			badMatcher:   r.intn(2) == 1,
			reservedType: r.intn(2) == 1,
			mcp:          r.intn(2) == 1,
		})
	}
	return specs, kind
}

// FuzzLcyc_InitPlugins materializes fuzz-selected plugin dirs under t.TempDir and
// drives initPlugins (hooks NEVER executed — runSessionStartHooks is false and the
// Resume kind defers), asserting never-panic and the determinism oracle across a
// fresh session pointed at the same dirs.
func FuzzLcyc_InitPlugins(f *testing.F) {
	seeds := [][]byte{
		{},
		{0, 1, 1, 1, 1, 0, 0, 0, 0},          // one plugin, skill
		{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, // full component set + hooks
		{2, 1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0}, // resume kind
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		specs, kind := lcycDecodeInit(data)
		root := t.TempDir()
		dirs := make([]string, 0, len(specs))
		for i, spec := range specs {
			dir := filepath.Join(root, fmt.Sprintf("p%d", i))
			lcycMaterializePlugin(t, dir, spec)
			dirs = append(dirs, dir)
		}

		res1 := lcycRunInit(t, dirs, kind)
		res2 := lcycRunInit(t, dirs, kind)
		if res1 != res2 {
			t.Fatalf("determinism: initPlugins result differs:\n first: %s\nsecond: %s", res1, res2)
		}
	})
}

// lcycRunInit builds a fresh session, points it at the dirs, runs initPlugins with
// hooks disabled, and returns a deterministic projection of the merge result.
func lcycRunInit(t *testing.T, dirs []string, kind plugin.SessionStartKind) string {
	t.Helper()
	sess := newSession(t)
	sess.cfg.PluginDirs = dirs
	// runSessionStartHooks=false keeps every hook unexecuted (no subprocess); the
	// Resume kind additionally exercises the defer branch.
	err := sess.initPlugins(kind, false)

	skillNames := make([]string, 0, len(sess.skills))
	for name := range sess.skills {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	agentNames := make([]string, 0, len(sess.pluginAgents))
	for name := range sess.pluginAgents {
		agentNames = append(agentNames, name)
	}
	sort.Strings(agentNames)

	errStr := "nil"
	if err != nil {
		errStr = err.Error()
	}
	return fmt.Sprintf("err=%s;plugins=%d;skills=%v;agents=%v;mcp=%d;warnings=%d",
		errStr, len(sess.plugins), skillNames, agentNames, len(sess.pluginMCPConfigs), len(sess.pendingHookWarnings))
}

// lcycMaterializePlugin writes a valid plugin tree selected by the spec. All path
// components are compile-time constants, so nothing is written outside dir.
func lcycMaterializePlugin(t *testing.T, dir string, spec lcycPluginSpec) {
	t.Helper()
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(filepath.Join(".claude-plugin", "plugin.json"),
		fmt.Sprintf(`{"name":%q,"version":"1.0.0"}`, spec.name))
	if spec.skill {
		write(filepath.Join("skills", "lcyc-skill", "SKILL.md"),
			"---\nname: lcyc-skill\ndescription: a lcyc fuzz skill\n---\nbody")
	}
	if spec.agent {
		write(filepath.Join("agents", "lcyc-agent.md"),
			"---\nname: lcyc-agent\ndescription: a lcyc fuzz agent\ntools:\n  - read_file\n---\nrole prompt")
	}
	if spec.hooks {
		write(filepath.Join("hooks", "hooks.json"), lcycHooksJSON(spec))
	}
	if spec.mcp {
		write(".mcp.json", `{"mcpServers":{"lcyc-server":{"command":"echo","args":["hi"]}}}`)
	}
}

// lcycHooksJSON renders a hooks.json that reaches the supported / unsupported /
// unknown / reserved-handler-type / invalid-matcher diagnostic loops per the spec.
func lcycHooksJSON(spec lcycPluginSpec) string {
	matcher := "*"
	if spec.badMatcher {
		matcher = "[" // invalid regex -> Runner.Validate diagnostic
	}
	handlerType := "command"
	if spec.reservedType {
		handlerType = "http" // reserved handler type -> unsupportedHandlerTypeWarnings
	}
	// SessionStart: supported event; "Setup": recognized-but-unsupported;
	// "LcycMadeUpEvent": unknown. The command is never executed by this harness.
	return fmt.Sprintf(`{
  "SessionStart": [{"matcher": %q, "hooks": [{"type": %q, "command": "echo lcyc"}]}],
  "Setup": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo reserved"}]}],
  "LcycMadeUpEvent": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo unknown"}]}]
}`, matcher, handlerType)
}