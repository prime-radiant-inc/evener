//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// FuzzStoolCommunicationDispatch drives communicate through the real registry
// executor and through its handler boundary. The handler cases deliberately
// cover inputs rejected before registry validation as well as successful
// terminal and non-terminal deliveries. All dependencies are in-memory
// recorders; no provider, filesystem, process, or network boundary is used.
func FuzzStoolCommunicationDispatch(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4},
		{255, 0, 128, 64, 32},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		first := stoolCommunicateRun(t, data)
		second := stoolCommunicateRun(t, data)
		if first != second {
			t.Fatalf("communicate dispatch was nondeterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type stoolCommunicateTrace struct {
	TerminalOutput string
	Inbox          string
	Reply          string
	Structured     string
	Callback       string
	Events         int
	Deferred       int
}

func stoolCommunicateRun(t *testing.T, data []byte) stoolCommunicateTrace {
	t.Helper()
	token := (&stweb_reader{data: data}).stweb_token()
	if token == "" {
		token = "empty"
	}
	abortErr := errors.New("fixture abort")

	// Both abort checks are observable: the first rejects before argument
	// parsing, while the second rejects a valid call before any side effects.
	for _, abortAt := range []int{1, 2} {
		calls := 0
		deps := stoolCommunicateDeps()
		deps.abort = func(context.Context) error {
			calls++
			if calls == abortAt {
				return abortErr
			}
			return nil
		}
		reg := tool.NewRegistry()
		registerCommunicateTool(reg, deps)
		_, err := reg.Get("communicate").Exec(context.Background(), nil, map[string]any{
			"message": "abort " + token, "end_turn": true,
		})
		if !errors.Is(err, abortErr) || calls != abortAt {
			t.Fatalf("abort %d: calls=%d err=%v", abortAt, calls, err)
		}
	}

	deps := stoolCommunicateDeps()
	committed := false
	deps.setCommunicateResult = func(string, string, string) { committed = true }
	reg := tool.NewRegistry()
	registerCommunicateTool(reg, deps)
	handler := reg.Get("communicate").Exec
	if _, err := handler(context.Background(), nil, map[string]any{"message": token}); err == nil || !strings.Contains(err.Error(), "end_turn") {
		t.Fatalf("missing end_turn error = %v", err)
	}
	if _, err := handler(context.Background(), nil, map[string]any{"end_turn": false}); err == nil || !strings.Contains(err.Error(), "message") {
		t.Fatalf("missing message error = %v", err)
	}

	// Nonterminal output may supply the message, but must not commit a result.
	nonterminal, err := handler(context.Background(), nil, map[string]any{
		"end_turn": false,
		"output": map[string]any{
			"message":   token,
			"data":      map[string]any{"iteration": len(data)},
			"artifacts": []any{"artifact-" + token},
		},
	})
	if err != nil || !strings.Contains(nonterminal.(string), `"accepted":true`) || committed {
		t.Fatalf("nonterminal communicate = %#v err=%v committed=%v", nonterminal, err, committed)
	}

	var resultMessage, resultReply, resultOutput string
	var structured any
	var callback string
	var emitted int
	var deferred []steeringMessage
	deps.emit = func(kind events.EventKind, data events.EventData) {
		if kind != events.EventCommunicate {
			t.Fatalf("communicate emitted unexpected event %q", kind)
		}
		emitted++
	}
	deps.drainSteering = func() []steeringMessage {
		return []steeringMessage{
			{Text: "steer " + token},
			{Images: []ImageAttachment{{Data: []byte("image-" + token), MediaType: "image/png"}}},
		}
	}
	deps.prependSteering = func(entries []steeringMessage) { deferred = append(deferred, entries...) }
	deps.setCommunicateResult = func(message, reply, output string) {
		resultMessage, resultReply, resultOutput = message, reply, output
	}
	deps.setCommunicateStructured = func(raw any) { structured = raw }
	deps.deliverWatchCallback = func(message string) { callback = message }

	args := map[string]any{
		"message":  " done " + token + " ",
		"end_turn": true,
		"output": map[string]any{
			"message":   token,
			"data":      map[string]any{"bytes": len(data)},
			"artifacts": []any{"artifact-" + token},
		},
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	terminal := reg.ExecuteCall(context.Background(), &agenttest.DenyEnv{}, llm.ToolCallData{
		ID: "communicate-terminal", Name: "communicate", Arguments: raw, Type: "function",
	})
	if terminal.IsError || terminal.Err != nil || resultMessage != "done "+token || resultReply != resultOutput {
		t.Fatalf("terminal communicate result=%#v message=%q reply=%q output=%q", terminal, resultMessage, resultReply, resultOutput)
	}
	if structured == nil || emitted != 1 || len(deferred) != 1 || len(deferred[0].Images) != 1 {
		t.Fatalf("terminal side effects: structured=%#v events=%d deferred=%#v", structured, emitted, deferred)
	}
	if !strings.Contains(callback, "Observer callback:") || !strings.Contains(callback, "done "+token) || !strings.Contains(callback, resultOutput) {
		t.Fatalf("watch callback = %q", callback)
	}
	var response struct {
		Inbox []string `json:"inbox"`
	}
	if err := json.Unmarshal([]byte(terminal.FullOutput), &response); err != nil || len(response.Inbox) != 1 || response.Inbox[0] != "steer "+token {
		t.Fatalf("terminal response=%q decoded=%#v err=%v", terminal.FullOutput, response, err)
	}

	// The small pure normalization surface is part of communicate dispatch:
	// keep its accepted dynamic wire forms and default/custom schema detection
	// under the same fuzz replay.
	for _, rawOutput := range []any{
		nil,
		nodeOutput{},
		nodeOutput{Data: nil, Artifacts: nil},
		nodeOutput{Decision: "continue", Message: token, Data: map[string]any{"ok": true}, Artifacts: []string{"a"}},
		map[string]any{"message": 42, "artifacts": []string{"a"}},
		map[string]any{"artifacts": []string{"a"}},
		map[string]any{"artifacts": []any{"a", 2}},
		map[string]any{"decision": " "},
		map[string]any{"decision": "continue"},
		map[string]any{"message": "message"},
		map[string]any{"data": map[string]any{}},
		map[string]any{"data": []any{}},
		map[string]any{"data": nil},
		map[string]any{"artifacts": []string{}},
		map[string]any{"artifacts": []any{}},
		map[string]any{"artifacts": 42},
		map[string]any{"extra": false},
		"custom-output",
	} {
		_ = hasMeaningfulNodeOutput(normalizeNodeOutput(rawOutput))
		_ = hasMeaningfulRawOutput(rawOutput)
		if text := canonicalNodeOutputText(rawOutput); !json.Valid([]byte(text)) {
			t.Fatalf("canonical output is not JSON: %q", text)
		}
	}
	if !usesDefaultCommunicateOutputEnvelope(tool.DefCommunicateNamed("communicate")) {
		t.Fatal("base communicate definition was not recognized")
	}
	for _, def := range []llm.ToolDefinition{
		{},
		{Parameters: map[string]any{"properties": map[string]any{"output": map[string]any{}}}},
		{Parameters: map[string]any{"properties": map[string]any{"output": map[string]any{"properties": map[string]any{"message": map[string]any{}}}}}},
	} {
		if usesDefaultCommunicateOutputEnvelope(def) {
			t.Fatalf("custom schema recognized as default: %#v", def)
		}
	}
	if got := communicateSchemaStringSlice([]any{"message", 1, "data"}); len(got) != 2 || !communicateSchemaContains(got, "data") {
		t.Fatalf("schema string normalization = %#v", got)
	}
	if got := communicateSchemaStringSlice(42); got != nil {
		t.Fatalf("unexpected schema strings = %#v", got)
	}
	if got := watchCommunicateCallbackText(" "+token+" ", " "); got != "Observer callback:\nmessage: "+token {
		t.Fatalf("empty-output callback = %q", got)
	}

	return stoolCommunicateTrace{
		TerminalOutput: terminal.FullOutput,
		Inbox:          strings.Join(response.Inbox, "|"),
		Reply:          resultReply,
		Structured:     resultOutput,
		Callback:       callback,
		Events:         emitted,
		Deferred:       len(deferred),
	}
}

func stoolCommunicateDeps() *toolDeps {
	return &toolDeps{
		emit:                     func(events.EventKind, events.EventData) {},
		abort:                    func(context.Context) error { return nil },
		drainSteering:            func() []steeringMessage { return nil },
		prependSteering:          func([]steeringMessage) {},
		resultToolName:           func() string { return "communicate" },
		setCommunicateResult:     func(string, string, string) {},
		setCommunicateStructured: func(any) {},
	}
}

// FuzzStwebRegistrationEgress drives the web-tool registration boundary through
// the real registry. Its dependency functions are inert recorders, so the
// positive paths cannot reach HTTP; DenyEnv and a literal sandbox wrapper keep
// every egress-policy case entirely local.
//
// It covers web_fetch's unconditional registration, Gemini-only web_search
// registration, and all egressDeniedByNet outcomes: an env without a wrapper,
// a nil wrapper, a net-on wrapper, and a net-off wrapper. The net-off invariant
// is particularly important: the dependency closure must never run.
func FuzzStwebRegistrationEgress(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0},                        // fetch only, env has no KernelWrapper
		{1, 0, 1, 2, 3},               // both tools, unsandboxed
		{1, 1, 4, 5, 6},               // both tools, nil wrapper
		{1, 2, 7, 8, 9},               // both tools, network allowed
		{1, 3, 10, 11, 12},            // both tools, network denied
		{255, 3, 255, 0, 128, 64, 32}, // long adversarial token, denied
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		first := stweb_run(t, data)
		second := stweb_run(t, data)
		if first != second {
			t.Fatalf("web tool program was nondeterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type stweb_egressState byte

const (
	stweb_plainEnv stweb_egressState = iota
	stweb_nilWrapperEnv
	stweb_netOnEnv
	stweb_netOffEnv
)

type stweb_wrapperEnv struct {
	*agenttest.DenyEnv
	stweb_wrapper *sandbox.Wrapper
}

// KernelWrapper is the optional execenv capability egressDeniedByNet probes.
func (e *stweb_wrapperEnv) KernelWrapper() *sandbox.Wrapper { return e.stweb_wrapper }

type stweb_trace struct {
	Names        string
	Fetch        stweb_result
	Search       stweb_result
	FetchCalls   int
	SearchCalls  int
	SearchActive bool
}

type stweb_result struct {
	Output  string
	IsError bool
	Denied  string
}

func stweb_run(t *testing.T, data []byte) stweb_trace {
	t.Helper()
	r := &stweb_reader{data: data}
	searchEnabled := r.stweb_bool()
	state := stweb_egressState(r.stweb_next() % 4)
	token := r.stweb_token()

	fetchURL := "https://fixture.invalid/" + token
	question := "question-" + token
	query := "query-" + token
	var fetchCalls, searchCalls int
	deps := &toolDeps{
		webSearchEnabled: searchEnabled,
		web: webDeps{
			fetch: func(_ context.Context, gotURL, gotQuestion string) (any, error) {
				fetchCalls++
				if gotURL != fetchURL || gotQuestion != question {
					t.Fatalf("web_fetch dependency args = (%q, %q), want (%q, %q)", gotURL, gotQuestion, fetchURL, question)
				}
				return "fetched:" + gotURL + ":" + gotQuestion, nil
			},
			search: func(_ context.Context, gotQuery string) (any, error) {
				searchCalls++
				if gotQuery != query {
					t.Fatalf("web_search dependency query = %q, want %q", gotQuery, query)
				}
				return "searched:" + gotQuery, nil
			},
		},
	}
	reg := tool.NewRegistry()
	registerWebTools(reg, deps)

	wantNames := "web_fetch"
	if searchEnabled {
		wantNames += ",web_search"
	}
	if got := strings.Join(reg.Names(), ","); got != wantNames {
		t.Fatalf("registered web tools = %q, want %q", got, wantNames)
	}
	if registered := reg.Get("web_fetch"); registered == nil || registered.Exec == nil {
		t.Fatal("web_fetch was not registered with an executor")
	}
	if registered := reg.Get("web_search"); searchEnabled && (registered == nil || registered.Exec == nil) {
		t.Fatal("web_search was not registered with an executor")
	} else if !searchEnabled && registered != nil {
		t.Fatalf("web_search registration = %v, want false", registered != nil)
	}

	env := stweb_env(t, state, stweb_seed(data))
	denied := state == stweb_netOffEnv
	fetch := stweb_execute(t, reg, env, "stweb-fetch", "web_fetch", map[string]any{
		"url":      fetchURL,
		"question": question,
		"purpose":  "inspect " + token,
	})
	stweb_assertResult(t, fetch, "web_fetch", "stweb-fetch", denied, "fetched:"+fetchURL+":"+question)
	if got, want := fetchCalls, stweb_callCount(denied); got != want {
		t.Fatalf("web_fetch dependency calls = %d, want %d", got, want)
	}

	trace := stweb_trace{
		Names:        wantNames,
		Fetch:        stweb_projectResult(fetch),
		FetchCalls:   fetchCalls,
		SearchCalls:  searchCalls,
		SearchActive: searchEnabled,
	}
	if !searchEnabled {
		return trace
	}

	search := stweb_execute(t, reg, env, "stweb-search", "web_search", map[string]any{
		"query":   query,
		"purpose": "search " + token,
	})
	stweb_assertResult(t, search, "web_search", "stweb-search", denied, "searched:"+query)
	if got, want := searchCalls, stweb_callCount(denied); got != want {
		t.Fatalf("web_search dependency calls = %d, want %d", got, want)
	}
	trace.Search = stweb_projectResult(search)
	trace.SearchCalls = searchCalls
	return trace
}

func stweb_env(t *testing.T, state stweb_egressState, seed uint64) execenv.ExecutionEnvironment {
	t.Helper()
	base := &agenttest.DenyEnv{WorkDir: "/stweb", Seed: seed}
	switch state {
	case stweb_plainEnv:
		return base
	case stweb_nilWrapperEnv:
		return &stweb_wrapperEnv{DenyEnv: base}
	case stweb_netOnEnv:
		return &stweb_wrapperEnv{DenyEnv: base, stweb_wrapper: stweb_newWrapper(t, true)}
	case stweb_netOffEnv:
		return &stweb_wrapperEnv{DenyEnv: base, stweb_wrapper: stweb_newWrapper(t, false)}
	default:
		t.Fatalf("unexpected egress state %d", state)
		return nil
	}
}

func stweb_newWrapper(t *testing.T, network bool) *sandbox.Wrapper {
	t.Helper()
	wrapper, err := sandbox.NewWrapper(sandbox.ResolvedPolicy{
		Mode:    sandbox.ModeRestricted,
		Network: network,
		Backend: sandbox.BackendBwrap,
	}, "/stweb/bwrap", "/stweb/session-tmp")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	return wrapper
}

func stweb_execute(t *testing.T, reg *tool.Registry, env execenv.ExecutionEnvironment, id, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s arguments: %v", name, err)
	}
	return reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        id,
		Name:      name,
		Arguments: raw,
		Type:      "function",
	})
}

func stweb_assertResult(t *testing.T, result tool.ExecResult, name, id string, denied bool, wantOutput string) {
	t.Helper()
	if result.ToolName != name || result.CallID != id {
		t.Fatalf("%s result identity = %#v, want name=%q id=%q", name, result, name, id)
	}
	if denied {
		denial, ok := sandbox.AsDenied(result.Err)
		if !result.IsError || !ok || denial.Tool != name || denial.Mode != sandbox.ModeRestricted {
			t.Fatalf("%s net-off result = %#v, denial=%#v ok=%v", name, result, denial, ok)
		}
		if !strings.Contains(denial.Reason, "network egress is disabled") || !strings.Contains(denial.Reason, "fixed for the session") {
			t.Fatalf("%s net-off denial reason = %q", name, denial.Reason)
		}
		return
	}
	if result.IsError || result.Err != nil || result.FullOutput != wantOutput {
		t.Fatalf("%s result = %#v, want success output %q", name, result, wantOutput)
	}
}

func stweb_projectResult(result tool.ExecResult) stweb_result {
	denial, _ := sandbox.AsDenied(result.Err)
	denied := ""
	if denial != nil {
		denied = denial.Tool + ":" + denial.Reason
	}
	return stweb_result{Output: result.FullOutput, IsError: result.IsError, Denied: denied}
}

func stweb_callCount(denied bool) int {
	if denied {
		return 0
	}
	return 1
}

type stweb_reader struct {
	data []byte
	pos  int
}

func (r *stweb_reader) stweb_next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	value := r.data[r.pos]
	r.pos++
	return value
}

func (r *stweb_reader) stweb_bool() bool { return r.stweb_next()&1 != 0 }

func (r *stweb_reader) stweb_token() string {
	var out strings.Builder
	for i, n := 0, int(r.stweb_next()%24)+1; i < n; i++ {
		out.WriteByte('a' + r.stweb_next()%26)
	}
	return out.String()
}

func stweb_seed(data []byte) uint64 {
	var seed uint64
	for i, value := range data {
		if i == 8 {
			break
		}
		seed |= uint64(value) << (8 * i)
	}
	return seed
}
