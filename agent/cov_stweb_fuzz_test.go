//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

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
