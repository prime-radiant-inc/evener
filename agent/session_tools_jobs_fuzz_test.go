//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/fuzz/fault"
	"primeradiant.com/serf/fuzz/oracle"
)

// This file fuzzes the job-tool surface in session_tools_jobs.go — the read /
// list / stop / watch tools an agent calls to inspect and control its jobs, plus
// the pure formatters those tools render through. Two targets split the surface
// by what property is truthful to assert:
//
//   - FuzzJobtoolsExec drives the STATEFUL tool handlers (jobReadOutputTool,
//     jobListTool, jobStopTool, jobWatchTool) against a real Session whose
//     jobstore is populated from fuzzed job/watch events, with fuzzed tool-call
//     args and a fuzzer-derived FAULT PLAN wired onto the store's append seams so
//     the persist-error branches (which adversarial input alone cannot reach) run.
//     Oracles: never-panic; the clean-error contract (a rejected call returns the
//     empty string and a non-nil error, never a partial value or a panic); a
//     success result is a well-formed StateResult whose State marshals to JSON;
//     and read-only tools are deterministic across back-to-back calls on an
//     unchanged store.
//
//   - FuzzJobtoolsFormat drives the PURE formatters (formatJobList, formatJobWatch,
//     formatJobStop, formatJobReadOutput) on fuzzed result structs, asserting they
//     are deterministic and never panic on adversarial field content that the exec
//     path rarely reaches (deep watch/delegate/recent-watch rows, every optional
//     pointer field set).
//
// The jobstore's afero-fs fault path (open/read/write/sync failures at the file
// layer) is owned by internal/jobstore/store_fault_fuzz_test.go, which can reach
// the unexported openFs seam. From the agent package the reachable effect seam is
// the appendEvent/appendEvents closure the jobManager persists through, so this
// harness injects fault.ErrInjected there — the same clean-error contract a real
// disk-write failure surfaces.

// jobtools_reader is a stable cursor over the fuzzer's bytes: out of bytes -> 0,
// so a short input decodes deterministically and a longer one is a strict
// superset (append new draws, never reorder, to keep the corpus meaning stable).
type jobtools_reader struct {
	data []byte
	pos  int
}

func (r *jobtools_reader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

// intn returns next() reduced into [0,n). n<=0 yields 0.
func (r *jobtools_reader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

// booln draws a bool.
func (r *jobtools_reader) booln() bool { return r.next()%2 == 0 }

// str draws a short string from a fixed adversarial alphabet, so ids, reasons,
// commands, and grep patterns stay legible while still exercising escaping,
// unicode, and empty values.
func (r *jobtools_reader) str() string {
	n := r.intn(len(jobtools_strings))
	return jobtools_strings[n]
}

// bytesN draws up to n raw bytes for a job's output log.
func (r *jobtools_reader) bytesN(n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, r.intn(n))
	for i := range out {
		out[i] = r.next()
	}
	return out
}

// jobtools_strings is the adversarial value alphabet: empty, plain, whitespace,
// a path-traversal shape, unicode, a lone-surrogate-ish escape, a regex
// metacharacter run, and long-ish content, plus real job-id prefixes the tools
// special-case (dlg_/job_).
var jobtools_strings = []string{
	"",
	"job_a",
	"job_",
	"dlg_x",
	"self",
	"parent",
	"caller",
	"*",
	"  spaced  ",
	"../escape",
	"ünïçødé",
	"line1\nline2\nline3\n",
	"a(b|c)*d",
	"[",
	`"quoted"`,
	"stop_pending",
}

// jobtools_jobTypes / jobtools_statuses are the enum draws for seeded records.
var jobtools_jobTypes = []jobstore.JobType{jobstore.JobShell, jobstore.JobDelegate, "bogus"}
var jobtools_statuses = []jobstore.Status{
	jobstore.StatusRunning,
	jobstore.StatusCompleted,
	jobstore.StatusFailed,
	jobstore.StatusCancelled,
	jobstore.StatusStopped,
	"weird",
}

// jobtools_faultPlan trips sparse, deterministic persist faults from fuzzer
// bytes, mirroring fault.Schedule's ~1-in-4 cadence (whose trip() is unexported)
// so the success paths still run and the fuzzer explores interleavings of
// failure and progress. It returns fault.ErrInjected — the same generic disk
// fault the fs-level rig injects — honoring the append seam's contract (return an
// error OR delegate to the real store, never both).
type jobtools_faultPlan struct {
	plan []byte
	n    atomic.Uint64
}

func (p *jobtools_faultPlan) trip() error {
	if p == nil || len(p.plan) == 0 {
		return nil
	}
	i := p.n.Add(1) - 1
	if p.plan[int(i%uint64(len(p.plan)))]%4 != 0 {
		return nil
	}
	return fault.ErrInjected
}

// FuzzJobtoolsExec fuzzes the stateful job-tool handlers against a real Session
// seeded from fuzzed events, with fuzzed args and an injected persist-fault plan.
func FuzzJobtoolsExec(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 1, 0, 0, 0, 0, 2, 3, 5})
	f.Add([]byte{2, 0, 1, 3, 4, 1, 0, 0, 7, 9, 2, 5, 6, 8, 1, 1, 1})
	f.Add([]byte{3, 4, 2, 1, 0, 2, 5, 7, 9, 11, 13, 3, 3, 3, 3, 3, 0, 0, 0, 0, 200})
	f.Add([]byte{1, 3, 3, 0, 12, 5, 1, 1, 1, 1, 255, 255, 255, 255})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jobtools_reader{data: data}
		sess := newSession(t)
		jm := sess.jobManager

		// Deterministic clock so seeded timestamps and any now() reads are stable.
		freezeClock(jm)
		now := frozenTestTime

		// Seed 0..4 jobs from fuzzed events. Each is a started (+ maybe finished)
		// record owned by and visible to this session, with an optional on-disk
		// output log so the read/grep/line-slice windows have bytes to slice.
		nJobs := r.intn(5)
		seededIDs := make([]string, 0, nJobs)
		for i := 0; i < nJobs; i++ {
			jobID := "job_seed" + string(rune('a'+i))
			started := now
			startEv := jobstore.Event{
				Kind:             jobstore.EventJobStarted,
				TS:               now,
				JobID:            jobID,
				Type:             jobtools_jobTypes[r.intn(len(jobtools_jobTypes))],
				OwnerSessionID:   sess.ID(),
				VisibleToSession: sess.ID(),
				StartedAt:        &started,
				Command:          r.str(),
				Description:      r.str(),
			}
			if r.booln() {
				startEv.DelegateID = "dlg_seed" + string(rune('a'+i))
			}
			if err := jm.appendEvent(startEv); err != nil {
				continue // a marshal/write hiccup on seeding is not the target
			}
			seededIDs = append(seededIDs, jobID)

			// Optional on-disk output within the temp-dir sandbox. The byte count
			// is recorded on the finished event below so the terminal
			// output-metadata check accepts it and the read/grep/line-slice
			// windows actually read content instead of short-circuiting on a
			// mismatch error.
			var outBytes int64
			if out := r.bytesN(96); len(out) > 0 {
				logPath := filepath.Join(jm.dir, "jobs", jobID+".log")
				if os.WriteFile(logPath, out, 0o644) == nil {
					outBytes = int64(len(out))
				}
			}

			if r.booln() {
				ended := now
				exit := r.intn(4) - 1
				finEv := jobstore.Event{
					Kind:        jobstore.EventJobFinished,
					TS:          now,
					JobID:       jobID,
					Status:      jobtools_statuses[r.intn(len(jobtools_statuses))],
					Reason:      r.str(),
					EndedAt:     &ended,
					ExitCode:    &exit,
					OutputBytes: outBytes,
					TerminalGen: jobstore.NewTerminalGeneration(),
				}
				_ = jm.appendEvent(finEv)
			}
		}

		ctx := context.Background()
		const maxChars = jobToolResultDefaultMaxChar

		// A job id the tools will operate on: a seeded one, or a fuzzed value that
		// drives the not-found / bad-prefix rejection branches.
		targetID := r.str()
		if len(seededIDs) > 0 && r.booln() {
			targetID = seededIDs[r.intn(len(seededIDs))]
		}

		// Determinism oracle (PRE-fault, read-only): job_list and job_read_output
		// must render byte-identically across back-to-back calls on an unchanged
		// store. Faults are armed only afterward, so this stays a pure read.
		listArgs := jobtools_listArgs(r)
		jobtools_assertReadDeterministic(t, func() (any, error) {
			return jobListTool(sess, jobtools_cloneArgs(listArgs), maxChars)
		})
		readArgs := jobtools_readArgs(r, targetID)
		jobtools_assertReadDeterministic(t, func() (any, error) {
			return jobReadOutputTool(ctx, sess, jobtools_cloneArgs(readArgs), maxChars)
		})

		// Arm the persist-fault plan on the append seams.
		plan := &jobtools_faultPlan{plan: r.bytesN(16)}
		origAppend := jm.appendEvent
		jm.appendEvent = func(e jobstore.Event) error {
			if err := plan.trip(); err != nil {
				return err
			}
			return origAppend(e)
		}
		origAppends := jm.appendEvents
		jm.appendEvents = func(es []jobstore.Event) error {
			if err := plan.trip(); err != nil {
				return err
			}
			return origAppends(es)
		}

		// Never-panic + clean-error + well-formed across the whole family, now
		// with faults live. A rejected call must return ("", non-nil err).
		res, err := jobListTool(sess, jobtools_cloneArgs(listArgs), maxChars)
		jobtools_assertClean(t, "job_list", res, err)

		res, err = jobReadOutputTool(ctx, sess, jobtools_cloneArgs(readArgs), maxChars)
		jobtools_assertClean(t, "job_read_output", res, err)

		res, err = jobWatchTool(sess, jobtools_watchArgs(r, targetID), maxChars)
		jobtools_assertClean(t, "job_watch", res, err)

		res, err = jobStopTool(ctx, sess, jobtools_stopArgs(r, targetID), maxChars)
		jobtools_assertClean(t, "job_stop", res, err)
	})
}

// jobtools_listArgs builds a fuzzed job_list arg map. status/type arrays draw
// from valid + invalid values so both the accept and the reject branches run.
func jobtools_listArgs(r *jobtools_reader) map[string]any {
	args := map[string]any{}
	if r.booln() {
		args["limit"] = float64(r.intn(220) - 10) // spans <=0, valid, and over-cap
	}
	if r.booln() {
		args["include_nested"] = r.booln()
	}
	if r.booln() {
		args["include_descendants"] = r.booln()
	}
	if r.booln() {
		args["status"] = []any{jobtools_statusArg(r)}
	}
	if r.booln() {
		args["type"] = []any{string(jobtools_jobTypes[r.intn(len(jobtools_jobTypes))])}
	}
	return args
}

func jobtools_statusArg(r *jobtools_reader) any {
	return string(jobtools_statuses[r.intn(len(jobtools_statuses))])
}

// jobtools_readArgs builds a fuzzed job_read_output arg map exercising the
// head/tail/from_line/grep/max_wait window selectors, including the mutually
// exclusive and negative-value rejection branches.
func jobtools_readArgs(r *jobtools_reader, jobID string) map[string]any {
	args := map[string]any{"job_id": jobID}
	if r.booln() {
		args["head_lines"] = float64(r.intn(300) - 20)
	}
	if r.booln() {
		args["tail_lines"] = float64(r.intn(300) - 20)
	}
	if r.booln() {
		args["from_line"] = float64(r.intn(300) - 20)
	}
	if r.booln() {
		args["line_count"] = float64(r.intn(300) - 20)
	}
	if r.booln() {
		args["grep"] = r.str()
	}
	return args
}

// jobtools_watchArgs builds a fuzzed job_watch arg map across all operations.
func jobtools_watchArgs(r *jobtools_reader, jobID string) map[string]any {
	ops := []string{"create", "clear", "list", "inspect", "", "bogus"}
	args := map[string]any{"operation": ops[r.intn(len(ops))]}
	if r.booln() {
		args["source"] = r.str()
	}
	if r.booln() {
		args["watch_id"] = r.str()
	}
	if r.booln() {
		args["output_match"] = r.str()
	}
	if r.booln() {
		args["events"] = []any{r.str()}
	}
	if r.booln() {
		args["progress_interval_ms"] = float64(r.intn(120000) - 1000)
	}
	if r.booln() {
		args["event_filter"] = map[string]any{"tool_name": r.str(), "status": r.str()}
	}
	return args
}

// jobtools_stopArgs builds a fuzzed job_stop arg map.
func jobtools_stopArgs(r *jobtools_reader, jobID string) map[string]any {
	args := map[string]any{"job_id": jobID}
	if r.booln() {
		args["max_wait_ms"] = float64(r.intn(120000) - 1000)
	}
	if r.booln() {
		args["include_children"] = r.booln()
	}
	return args
}

func jobtools_cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// jobtools_assertClean enforces the tool contract: never panic (a panic aborts
// the test), and either a non-nil error with the empty-string sentinel result,
// or a nil error with a well-formed StateResult / string whose State marshals to
// JSON and whose text output is valid UTF-8.
func jobtools_assertClean(t *testing.T, name string, res any, err error) {
	t.Helper()
	if err != nil {
		if s, ok := res.(string); !ok || s != "" {
			t.Fatalf("%s: error result must be the empty string, got %T %#v (err=%v)", name, res, res, err)
		}
		return
	}
	switch v := res.(type) {
	case string:
		if !utf8.ValidString(v) {
			t.Fatalf("%s: success string is not valid UTF-8", name)
		}
	case tool.StateResult:
		if !utf8.ValidString(v.Output) {
			t.Fatalf("%s: StateResult.Output is not valid UTF-8", name)
		}
		if _, mErr := json.Marshal(v.State); mErr != nil {
			t.Fatalf("%s: StateResult.State is not JSON-marshalable: %v", name, mErr)
		}
	default:
		t.Fatalf("%s: unexpected success result type %T", name, res)
	}
}

// jobtools_assertReadDeterministic calls a read-only tool twice and requires the
// rendered (error, output) to agree — a hidden clock read, map-iteration order,
// or global-state dependency reddens the target.
func jobtools_assertReadDeterministic(t *testing.T, call func() (any, error)) {
	t.Helper()
	oracle.Deterministic(t, func(_ struct{}) string {
		res, err := call()
		return jobtools_render(res, err)
	}, struct{}{}, func(a, b string) bool { return a == b })
}

// jobtools_render canonicalizes a tool result for comparison.
func jobtools_render(res any, err error) string {
	if err != nil {
		return "ERR:" + err.Error()
	}
	switch v := res.(type) {
	case string:
		return "S:" + v
	case tool.StateResult:
		state, _ := json.Marshal(v.State)
		return "OUT:" + v.Output + "|STATE:" + string(state)
	default:
		return "?"
	}
}

// FuzzJobtoolsFormat fuzzes the pure job-tool formatters on adversarial result
// structs: deterministic output, never-panic, valid UTF-8.
func FuzzJobtoolsFormat(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 1, 1, 1, 1, 1, 1, 1})
	f.Add([]byte{2, 3, 0, 5, 7, 9, 11, 1, 1, 0, 0, 4, 6})
	f.Add([]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jobtools_reader{data: data}

		list := jobtools_buildListResult(r)
		oracle.Deterministic(t, formatJobList, list, func(a, b string) bool { return a == b })
		if s := formatJobList(list); !utf8.ValidString(s) {
			t.Fatalf("formatJobList produced invalid UTF-8")
		}

		watch := jobtools_buildWatchResult(r)
		oracle.Deterministic(t, formatJobWatch, watch, func(a, b string) bool { return a == b })
		if s := formatJobWatch(watch); !utf8.ValidString(s) {
			t.Fatalf("formatJobWatch produced invalid UTF-8")
		}

		stop := jobStopResult{
			JobID:          r.str(),
			Status:         r.str(),
			Reason:         jobtools_optStr(r),
			PreviousStatus: r.str(),
			Outcome:        r.str(),
		}
		oracle.Deterministic(t, formatJobStop, stop, func(a, b string) bool { return a == b })

		read := jobtools_buildReadResult(r)
		header := r.str()
		oracle.Deterministic(t, func(in jobReadOutputResult) string {
			return formatJobReadOutput(&in, header, jobToolResultDefaultMaxChar)
		}, read, func(a, b string) bool { return a == b })
	})
}

func jobtools_optStr(r *jobtools_reader) *string {
	if r.booln() {
		return nil
	}
	s := r.str()
	return &s
}

func jobtools_optInt(r *jobtools_reader) *int {
	if r.booln() {
		return nil
	}
	n := r.intn(512) - 128
	return &n
}

func jobtools_buildListResult(r *jobtools_reader) jobListResult {
	nJobs := r.intn(4)
	jobs := make([]jobListEntry, 0, nJobs)
	for i := 0; i < nJobs; i++ {
		e := jobListEntry{
			JobID:          r.str(),
			DelegateID:     r.str(),
			Kind:           r.str(),
			Type:           r.str(),
			Status:         r.str(),
			Reason:         jobtools_optStr(r),
			Description:    r.str(),
			OwnerSessionID: r.str(),
			StartedAt:      r.str(),
			ExitCode:       jobtools_optInt(r),
			TotalBytes:     int64(r.intn(1 << 20)),
			Depth:          r.intn(4),
		}
		if r.booln() {
			cmd := r.str()
			e.Command = &cmd
		}
		if r.booln() {
			b := r.booln()
			e.Resumable = &b
		}
		jobs = append(jobs, e)
	}
	res := jobListResult{Jobs: jobs, Count: len(jobs), DelegationAllowance: r.intn(6)}
	nDel := r.intn(3)
	for i := 0; i < nDel; i++ {
		res.Delegates = append(res.Delegates, delegateListEntry{
			DelegateID:       r.str(),
			Status:           r.str(),
			CurrentJobID:     r.str(),
			LatestJobID:      r.str(),
			TranscriptRef:    r.str(),
			Resumable:        r.booln(),
			NotResumableWhy:  r.str(),
			ParentDelegateID: r.str(),
		})
	}
	nW := r.intn(3)
	for i := 0; i < nW; i++ {
		res.Watches = append(res.Watches, watchListEntry{
			ID:         r.str(),
			Source:     r.str(),
			Condition:  r.str(),
			Deliveries: r.intn(10),
			CreatedAt:  r.str(),
		})
	}
	nR := r.intn(3)
	for i := 0; i < nR; i++ {
		res.RecentWatches = append(res.RecentWatches, recentWatchEntry{
			ID:         r.str(),
			Source:     r.str(),
			Condition:  r.str(),
			Deliveries: r.intn(10),
			EndReason:  r.str(),
			EndedAt:    r.str(),
		})
	}
	return res
}

func jobtools_buildWatchResult(r *jobtools_reader) jobWatchToolResult {
	out := jobWatchToolResult{
		WatchID:            r.str(),
		Source:             r.str(),
		Watching:           r.booln(),
		OutputMatch:        r.str(),
		ProgressIntervalMS: r.intn(120000),
		ReplacedExisting:   r.booln(),
		Fired:              r.booln(),
		TerminalCatchup:    r.booln(),
		Status:             r.str(),
	}
	nE := r.intn(3)
	for i := 0; i < nE; i++ {
		out.Events = append(out.Events, r.str())
	}
	if r.booln() {
		out.EventFilter = &jobWatchToolEventFilter{ToolName: r.str(), Status: r.str()}
	}
	if r.booln() {
		out.Send = &jobWatchToolSendArgs{To: r.str(), Message: r.str(), IncludeExcerpt: r.booln()}
	}
	return out
}

func jobtools_buildReadResult(r *jobtools_reader) jobReadOutputResult {
	out := jobReadOutputResult{
		JobID:        r.str(),
		Type:         r.str(),
		Status:       r.str(),
		Reason:       jobtools_optStr(r),
		Content:      r.str(),
		TotalBytes:   int64(r.intn(1 << 20)),
		DroppedBytes: int64(r.intn(1 << 16)),
		OutputStatus: r.str(),
		Truncated:    r.booln(),
		ExitCode:     jobtools_optInt(r),
	}
	if r.booln() {
		g := r.str()
		out.Grep = &g
		matches := []jobOutputMatch{{ByteOffset: int64(r.intn(1024)), Line: r.str()}}
		out.Matches = &matches
	}
	if r.booln() {
		out.StructuredResult = map[string]any{"k": r.str(), "n": r.intn(100)}
		valid := r.booln()
		out.StructuredResultValid = &valid
	}
	if r.booln() {
		la := time.Unix(int64(r.intn(1<<20)), 0).UTC().Format(time.RFC3339Nano)
		out.LastActivity = &la
	}
	return out
}