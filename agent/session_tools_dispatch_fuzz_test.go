package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/fuzz/fault"
	"primeradiant.com/serf/llm"
)

// This file fuzzes the session's tool-DISPATCH surface — the plumbing that turns
// a model's tool calls into executed results — plus the web_fetch tool, which is
// too IO-tangled to reach through the ordinary registry-execution harness:
//
//   - FuzzStoolDispatch drives execTool (session_tools.go) and execToolBatch
//     (session_tool_round.go) against a real Session with fuzzed tool-call names
//     and argument bytes: unknown tools, malformed JSON, wrong-typed args, and
//     multi-call batches that exercise the parallel read-only grouping path. It
//     also reaches registerTaskTools' task_list handler (session_tools_task.go)
//     by including task_list among the driven names with fuzzed view/append/update
//     batches. Oracles: never-panic; dispatch preserves each call's identity
//     positionally (result ToolName/CallID track the call); an unknown tool yields
//     a clean structured tool error (IsError with an "unknown tool" message), never
//     a panic; the dispatch decision is deterministic (an unknown call rendered
//     twice agrees byte-for-byte). execTool's PreToolUse/PostToolUse hook blocks
//     are NOT reached here: the only hook mechanism is external command execution,
//     which a fuzzer must not spawn, so those branches stay owned by the unit
//     suite (session_config_test.go) and this target plateaus below 100% for that
//     structural reason, not for want of seeds.
//
//   - FuzzStoolWebFetch drives webFetch (tool_web_fetch.go) through an INJECTED
//     http transport that serves fuzzed response bytes — status, content-type,
//     body — with a fuzzer-derived fault schedule so the transport-error branch
//     runs too. No real network or DNS is touched; disk writes are redirected into
//     the test's temp dir via XDG_CACHE_HOME. Oracles: never-panic; the clean
//     result contract (an error returns a nil value, a success returns a
//     well-formed result map carrying every documented key).
//
// The httpDoer seam webFetch resolves through (Session.webFetchClient) was added
// to production solely to make this fuzzable; it is byte-identical for callers
// (nil → http.DefaultClient) and every existing web_fetch test is unchanged. The
// harness covers webFetch's happy path plus its read-body-error, content-
// truncation, transport-fault, and cheap-model-error branches; the residual
// uncovered lines are the os.MkdirAll/os.WriteFile error returns (webFetch writes
// the cache via os.* directly, so faulting them would need an afero seam not worth
// adding for three error-return lines) and the essentially-unreachable
// NewRequest-error branch.
//
// The byte reader (jobtools_reader) and the adversarial value alphabet
// (jobtools_strings) are reused from session_tools_jobs_fuzz_test.go — same
// package — so a short input decodes deterministically and the corpus meaning
// stays stable across edits.

// stool_knownTools is the curated set of registered tools this harness is willing
// to EXECUTE: each is contained by the session's local temp-dir env or reads
// session state only, so driving it under a fuzzer has no effect that escapes the
// sandbox. task_list is included specifically to cover registerTaskTools; the
// others give the read-only parallel-batch path real work to group.
var stool_knownTools = []string{
	"task_list",
	"job_list",
	"job_status",
	"glob",
	"grep",
	"read_file",
	"list_dir",
}

// stool_unknownNames are names that must never resolve to a registered tool, so
// the dispatch-miss / clean-error branch is exercised deterministically. Trailing
// space and case variants are deliberate: tool lookup is exact-match.
var stool_unknownNames = []string{
	"",
	"nope",
	"stool_bogus",
	"\x00",
	"delegate\n",
	"TASK_LIST",
	"job_list ",
	"../escape",
}

// stool_taskActions draws the task_list action across valid + invalid values so
// both the accept and the reject arms of registerTaskTools run.
var stool_taskActions = []string{"view", "append", "update", "", "bogus"}

// FuzzStoolDispatch fuzzes execTool / execToolBatch / the task_list handler.
func FuzzStoolDispatch(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 1, 0})                                  // task_list view-ish
	f.Add([]byte{0, 1, 3, 1, 0, 0, 1, 1})                      // task_list append
	f.Add([]byte{0, 2, 3, 2, 0, 1, 2, 3})                      // task_list update
	f.Add([]byte{2, 3, 4, 5, 6, 1, 2, 3, 4, 5, 6, 7, 8, 9})    // multi-call read batch
	f.Add([]byte{9, 9, 9, 9, 200, 200, 1, 2, 3, 0, 0, 0})      // unknown / garbage names
	f.Add([]byte{3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5, 8, 9, 7, 9}) // mixed

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jobtools_reader{data: data}
		sess := newSession(t)

		// The curated known tools must actually be registered with an executor;
		// otherwise the target would silently stop exercising them (and lose the
		// registerTaskTools coverage) if a tool were renamed.
		for _, name := range []string{"task_list", "job_list"} {
			if rt := sess.reg.Get(name); rt == nil || rt.Exec == nil {
				t.Fatalf("expected tool %q to be registered with an executor", name)
			}
		}

		ctx := context.Background()

		// Determinism oracle for the dispatch decision: a guaranteed-unknown call
		// must render identically across back-to-back execTool calls and land on
		// the clean-error contract. execTool's only side effects here are dropped
		// events, so this stays a pure, repeatable read of the miss path.
		detCall := llm.ToolCallData{
			ID:        "stool-det",
			Name:      "stool_unknown_" + r.str(),
			Arguments: json.RawMessage(stool_argsFor(r, "")),
			Type:      "function",
		}
		d1 := sess.execTool(ctx, detCall)
		d2 := sess.execTool(ctx, detCall)
		if d1.FullOutput != d2.FullOutput || d1.IsError != d2.IsError {
			t.Fatalf("dispatch nondeterministic for unknown tool: %#v vs %#v", d1, d2)
		}
		if !d1.IsError || !strings.Contains(d1.FullOutput, "unknown tool") {
			t.Fatalf("unknown tool did not yield a clean error: IsError=%v out=%q", d1.IsError, d1.FullOutput)
		}

		// Guaranteed task_list drive. task_list's Append/Update batches are atomic —
		// one invalid entry rejects the whole batch — so the success/auto-advance/
		// steer arms of registerTaskTools are only reachable through CLEAN batches.
		// These fixed calls walk that happy path (append two tasks, manually start
		// one, complete it to trigger auto-advance, complete the rest to hit the
		// all-done steer), then the fuzzed append/update calls exercise the reject
		// and view arms.
		stool_execOne(ctx, t, sess, "task_list", []byte(`{"action":"append","tasks":[{"type":"implement","description":"a","prompt":"do a"},{"type":"verify","description":"b","prompt":"do b"}]}`))
		stool_execOne(ctx, t, sess, "task_list", []byte(`{"action":"update","updates":[{"id":1,"status":"in_progress"}]}`))
		stool_execOne(ctx, t, sess, "task_list", []byte(`{"action":"update","updates":[{"id":1,"status":"done"}]}`))
		stool_execOne(ctx, t, sess, "task_list", []byte(`{"action":"update","updates":[{"id":2,"status":"done"}]}`))
		stool_execOne(ctx, t, sess, "task_list", stool_taskListArgs(r, "append"))
		stool_execOne(ctx, t, sess, "task_list", stool_taskListArgs(r, "update"))
		stool_execOne(ctx, t, sess, "task_list", stool_taskListArgs(r, "view"))

		// Entry-cancellation path: execTool must short-circuit to a skipped result
		// (never a panic) when handed an already-canceled context.
		cctx, ccancel := context.WithCancel(ctx)
		ccancel()
		if skipped := sess.execTool(cctx, llm.ToolCallData{ID: "stool-skip", Name: "job_list"}); !skipped.IsError {
			t.Fatalf("execTool under canceled context did not report an error: %#v", skipped)
		}

		// A call whose args carry a bare "description" (no "purpose") exercises the
		// event-description fallback branch.
		descArgs, _ := json.Marshal(map[string]any{"path": r.str(), "description": r.str()})
		stool_execOne(ctx, t, sess, "list_dir", descArgs)

		// Build a fuzzed batch of 1..4 calls with a mix of known, unknown, and
		// garbage names, then dispatch it through the real batch executor. Some
		// iterations run under an already-canceled context so the abort/append-
		// canceled branches of execTool and execToolBatch execute too.
		nCalls := r.intn(4) + 1
		calls := make([]llm.ToolCallData, 0, nCalls)
		for i := 0; i < nCalls; i++ {
			name := stool_drawName(r)
			calls = append(calls, llm.ToolCallData{
				ID:        "stool-c" + string(rune('a'+i)),
				Name:      name,
				Arguments: json.RawMessage(stool_argsFor(r, name)),
				Type:      "function",
			})
		}

		batchCtx := ctx
		canceled := r.intn(4) == 0
		if canceled {
			c, cancel := context.WithCancel(ctx)
			cancel()
			batchCtx = c
		}

		results, err := sess.execToolBatch(batchCtx, calls, sess.currentProfile())
		if err != nil {
			// execToolBatch errors ONLY on cancellation/abort — never on ordinary
			// adversarial tool input. Confirm the context is what stopped it.
			if batchCtx.Err() == nil {
				t.Fatalf("execToolBatch errored without a canceled context: %v", err)
			}
			return
		}
		if len(results) != len(calls) {
			t.Fatalf("execToolBatch returned %d results for %d calls", len(results), len(calls))
		}
		for i, res := range results {
			call := calls[i]
			// Dispatch preserves call identity positionally.
			if res.ToolName != call.Name {
				t.Fatalf("result %d ToolName = %q, want %q", i, res.ToolName, call.Name)
			}
			if res.CallID != call.ID {
				t.Fatalf("result %d CallID = %q, want %q", i, res.CallID, call.ID)
			}
			if !utf8.ValidString(res.FullOutput) || !utf8.ValidString(res.Output) {
				t.Fatalf("result %d output is not valid UTF-8", i)
			}
			// Clean-error contract: a name that resolves to no registered tool
			// must surface a structured "unknown tool" error, never a panic or a
			// success value.
			if sess.reg.Get(call.Name) == nil {
				if !res.IsError || !strings.Contains(res.FullOutput, "unknown tool") {
					t.Fatalf("unknown tool %q: IsError=%v out=%q", call.Name, res.IsError, res.FullOutput)
				}
			}
		}
	})
}

// stool_execOne dispatches a single call through execTool and asserts the
// never-panic + structured-result contract (a returned ExecResult always carries
// the call's name and id, never partial garbage).
func stool_execOne(ctx context.Context, t *testing.T, sess *Session, name string, args []byte) {
	t.Helper()
	res := sess.execTool(ctx, llm.ToolCallData{
		ID:        "stool-one",
		Name:      name,
		Arguments: json.RawMessage(args),
		Type:      "function",
	})
	if res.ToolName != name {
		t.Fatalf("execTool(%s): ToolName = %q", name, res.ToolName)
	}
	if res.CallID == "" {
		t.Fatalf("execTool(%s): empty CallID", name)
	}
	if !utf8.ValidString(res.FullOutput) {
		t.Fatalf("execTool(%s): output not valid UTF-8", name)
	}
}

// stool_taskListArgs marshals a fuzzed task_list argument blob for a specific
// action, driving the malformed/reject arms (empty batches, non-object entries,
// bad dependency refs, invalid statuses) that the fixed happy-path calls skip.
func stool_taskListArgs(r *jobtools_reader, action string) []byte {
	m := map[string]any{"action": action}
	switch action {
	case "append":
		m["tasks"] = stool_taskItems(r)
	case "update":
		m["updates"] = stool_updateItems(r)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"action":"view"}`)
	}
	return b
}

// stool_drawName picks a call name: usually a known executable tool, sometimes an
// unknown or garbage name to drive the dispatch-miss branch.
func stool_drawName(r *jobtools_reader) string {
	switch r.intn(3) {
	case 0:
		return stool_unknownNames[r.intn(len(stool_unknownNames))]
	default:
		return stool_knownTools[r.intn(len(stool_knownTools))]
	}
}

// stool_argsFor builds argument bytes for a call. With some probability it emits
// raw fuzzer bytes (hitting ExecuteCall's invalid-JSON / non-object branches);
// otherwise it marshals a structured, tool-aware map that reaches schema
// validation and the handler.
func stool_argsFor(r *jobtools_reader, name string) []byte {
	if r.intn(4) == 0 {
		return r.bytesN(24)
	}
	m := stool_structuredArgs(r, name)
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func stool_structuredArgs(r *jobtools_reader, name string) map[string]any {
	m := map[string]any{}
	switch name {
	case "task_list":
		m["action"] = stool_taskActions[r.intn(len(stool_taskActions))]
		switch m["action"] {
		case "append":
			m["tasks"] = stool_taskItems(r)
		case "update":
			m["updates"] = stool_updateItems(r)
		}
	case "read_file":
		m["file_path"] = r.str()
		if r.booln() {
			m["offset"] = float64(r.intn(200) - 20)
		}
		if r.booln() {
			m["limit"] = float64(r.intn(200) - 20)
		}
	case "glob":
		m["pattern"] = r.str()
		if r.booln() {
			m["path"] = r.str()
		}
	case "grep":
		m["pattern"] = r.str()
		if r.booln() {
			m["path"] = r.str()
		}
		if r.booln() {
			m["output_mode"] = []string{"content", "files_with_matches", "count", "bogus"}[r.intn(4)]
		}
	case "list_dir":
		m["path"] = r.str()
	case "job_list":
		if r.booln() {
			m["limit"] = float64(r.intn(220) - 10)
		}
		if r.booln() {
			m["status"] = []any{r.str()}
		}
		if r.booln() {
			m["type"] = []any{r.str()}
		}
	case "job_status":
		m["job_id"] = r.str()
	default:
		m["k"] = r.str()
	}
	// Occasionally attach a stray purpose (the wire param) to exercise its strip.
	if r.booln() {
		m["purpose"] = r.str()
	}
	return m
}

// stool_taskItems builds a SCHEMA-VALID tasks array for task_list append (type
// from the enum, description, prompt, integer deps, valid reasoning_effort), so
// the entries pass validation and drive the append handler's parsing and the
// store's dependency check — including a bad self/forward reference that fails
// store.Append. Schema-invalid shapes (missing type, non-object) are covered
// separately by the raw-bytes arg path in stool_argsFor.
func stool_taskItems(r *jobtools_reader) []any {
	n := r.intn(3)
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		validTypes := []string{"research", "implement", "verify", "fix"}
		item := map[string]any{
			"type":        validTypes[r.intn(len(validTypes))],
			"description": r.str(),
			"prompt":      r.str(),
		}
		if r.booln() {
			item["reasoning_effort"] = []string{"low", "medium", "high"}[r.intn(3)]
		}
		if r.booln() {
			item["depends_on"] = []any{float64(r.intn(5))}
		}
		out = append(out, item)
	}
	return out
}

// stool_updateItems builds a SCHEMA-VALID updates array for task_list update
// (integer id, valid status enum, optional notes/deps/effort), so entries reach
// store.Update and exercise its notes/depends_on/reasoning_effort parsing and the
// unknown-id / invalid-transition rejections.
func stool_updateItems(r *jobtools_reader) []any {
	n := r.intn(3)
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		validStatuses := []string{"open", "in_progress", "done", "cancelled"}
		item := map[string]any{
			"id":     float64(r.intn(6)),
			"status": validStatuses[r.intn(len(validStatuses))],
		}
		if r.booln() {
			item["notes"] = r.str()
		}
		if r.booln() {
			item["reasoning_effort"] = []string{"low", "medium", "high"}[r.intn(3)]
		}
		if r.booln() {
			item["depends_on"] = []any{float64(r.intn(5))}
		}
		out = append(out, item)
	}
	return out
}

// --- web_fetch ---

// stool_contentTypes spans the extFromContentType branches plus empty and garbage
// values, so both the HTML→markdown path and the raw-content path run.
var stool_contentTypes = []string{
	"text/html; charset=utf-8",
	"application/json",
	"text/plain",
	"text/xml",
	"application/xml",
	"application/octet-stream",
	"",
	"garbage/\x00type",
}

// stool_bodies are response payloads: HTML (drives html-to-markdown), JSON/text,
// binary, empty, and a lone-surrogate escape.
var stool_bodies = []string{
	"<html><body><h1>Hi</h1><p>a <a href=\"/x\">link</a></p></body></html>",
	"<html><body>" + strings.Repeat("<p>x</p>", 50) + "</body></html>",
	`{"k":"v","n":1}`,
	"plain text body\nwith lines\n",
	"\x00\x01\x02binary\xff",
	"",
	"\xed\xa0\x80", // lone surrogate bytes
}

// stool_urls span the URL-validation branches: valid http/https, an unsupported
// scheme, a parse error, and empty.
var stool_urls = []string{
	"http://example.test/a",
	"https://example.test/a/b?c=d",
	"ftp://example.test/x",
	"://bad",
	"http://[::1]/x",
	"",
	"not a url",
}

// stool_fakeTransport serves a fabricated http.Response built from fuzzed bytes.
// It honors the RoundTripper contract: a non-nil response always carries a
// non-nil Body, and the request Body (there is none for GET) is left untouched.
// When bodyErr is set, the Body errors partway through Read so webFetch's
// read-body error branch runs.
type stool_fakeTransport struct {
	status      int
	contentType string
	body        []byte
	bodyErr     bool
}

func (rt stool_fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h := make(http.Header)
	if rt.contentType != "" {
		h.Set("Content-Type", rt.contentType)
	}
	var body io.ReadCloser
	if rt.bodyErr {
		body = io.NopCloser(&stool_errReader{data: rt.body})
	} else {
		body = io.NopCloser(bytes.NewReader(rt.body))
	}
	return &http.Response{
		StatusCode: rt.status,
		Status:     http.StatusText(rt.status),
		Header:     h,
		Body:       body,
		Request:    req,
	}, nil
}

// stool_errReader yields its data once, then a read error — modeling a connection
// that drops mid-body so io.ReadAll returns an error.
type stool_errReader struct {
	data []byte
	done bool
}

func (e *stool_errReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, io.ErrUnexpectedEOF
	}
	e.done = true
	n := copy(p, e.data)
	return n, io.ErrUnexpectedEOF
}

// FuzzStoolWebFetch fuzzes webFetch with an injected transport and fault schedule.
func FuzzStoolWebFetch(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{1, 1, 1, 2, 3, 4})
	f.Add([]byte{2, 2, 5, 0, 1, 7, 9})
	f.Add([]byte{3, 5, 6, 4, 2, 1, 0, 8, 8, 8, 8})
	f.Add([]byte{4, 4, 4, 4, 4, 4, 4, 4}) // coherent-HTML + cheap-fault mix
	// Draw-order-crafted seeds (see the f.Fuzz body): a large non-HTML success that
	// trips content truncation, and a valid 200 fetch whose body errors mid-read.
	f.Add([]byte{1, 1, 0, 0, 1, 2, 1, 0, 0}) // large=true, non-html, 200 -> truncation
	f.Add([]byte{1, 1, 1, 0, 1, 0, 0, 0, 0}) // bodyErr=true, 200 -> read-body error

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jobtools_reader{data: data}

		// Redirect web_fetch's cache writes into the test's temp dir so nothing
		// escapes the sandbox.
		t.Setenv("XDG_CACHE_HOME", t.TempDir())

		// The cheap-model side call routes through a scripted "openai" adapter; a
		// fuzzer-gated FaultResponder makes it fail so the cheap-model-error branch
		// runs. openai is the profile's cheap provider, so the call resolves here.
		cheapFault := r.booln()
		adapter := &agenttest.ScriptedAdapter{
			Provider: "openai",
			Responder: func(llm.Request) llm.Response {
				return agenttest.FinalResponse("answer")
			},
		}
		if cheapFault {
			adapter.FaultResponder = func(llm.Request) error { return fault.ErrInjected }
		}
		sess := newSession(t, withAdapter(adapter))

		// Build the fabricated response. A "coherent" draw forces a 200 + text/html
		// body so the html→markdown + rendered.md + markdown_file arms run; a "large"
		// draw pushes the readable content past the truncation threshold.
		coherent := r.booln()
		large := r.booln()
		status := []int{200, 204, 301, 404, 500}[r.intn(5)]
		contentType := stool_contentTypes[r.intn(len(stool_contentTypes))]
		body := []byte(stool_bodies[r.intn(len(stool_bodies))])
		if coherent {
			status = 200
			contentType = "text/html; charset=utf-8"
			html := "<html><body><h1>Hi</h1><p>content</p></body></html>"
			if large {
				html = "<html><body>" + strings.Repeat("<p>paragraph text</p>", 6000) + "</body></html>"
			}
			body = []byte(html)
		} else if large {
			body = bytes.Repeat([]byte("abcdefghij"), 12000) // > webFetchMaxContent
		}

		base := stool_fakeTransport{
			status:      status,
			contentType: contentType,
			body:        body,
			bodyErr:     r.intn(4) == 0,
		}
		var transport http.RoundTripper = base
		if plan := r.bytesN(8); len(plan) > 0 {
			transport = fault.RoundTripper(base, fault.FromBytes(plan))
		}
		sess.httpClient = &http.Client{Transport: transport}

		rawURL := stool_urls[r.intn(len(stool_urls))]
		question := r.str()

		res, err := sess.webFetch(context.Background(), rawURL, question)

		// Clean result contract: exactly one of (error, value) is populated.
		if err != nil {
			if res != nil {
				t.Fatalf("webFetch returned both a value and an error: res=%#v err=%v", res, err)
			}
			return
		}
		m, ok := res.(map[string]any)
		if !ok {
			t.Fatalf("webFetch success result is %T, want map[string]any", res)
		}
		for _, key := range []string{"answer", "raw_file", "url", "content_type", "size_bytes"} {
			if _, present := m[key]; !present {
				t.Fatalf("webFetch success result missing key %q: %#v", key, m)
			}
		}
	})
}
