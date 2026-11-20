package agent

// Wire-level prefix tests for the provider replay prefix. Across turns, a cold
// restore, an env swap, a model switch-back and a root fork, the launch prompt,
// the tools and every earlier item must reach the provider in the same bytes.
//
// These drive the real llm Responses and Chat Completions clients against a
// loopback httptest.Server, so nothing about request shaping is mocked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// pxArgsWire is the probe call's arguments as the wire carries them: spaced, and
// escaped the way the body encoder writes <, > and &. Each probe below pairs
// that call with reasoning that must replay verbatim: untrimmed plaintext parts
// for Responses, scalar text beside a non-alphabetical encrypted detail for Chat.
const pxArgsWire = `"arguments":"{ \"b\" : 2, \"a\" : \"\u003cx\u0026y\u003e\" }"`

const pxResponsesProbe = `[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":" sum "}],"content":[{"type":"reasoning_text","text":"  step 1  "}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"probe","arguments":"{ \"b\" : 2, \"a\" : \"<x&y>\" }"}]`

const pxChatProbe = `{"role":"assistant","reasoning_content":"  why  ","reasoning_details":[{"z":1,"type":"reasoning.encrypted","id":"r1","data":"blob"}],"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"probe","arguments":"{ \"b\" : 2, \"a\" : \"<x&y>\" }"}}]}`

const pxDoneArgs = `{"message":"done","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`

const pxResponsesFrame = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":%s}}\n\n"

const pxChatFrame = "data: {\"id\":%q,\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":%s}]}\n\ndata: [DONE]\n\n"

type pxWire struct {
	mu     sync.Mutex
	got    [][]byte
	url    string
	answer func(n int, rw http.ResponseWriter)
}

// pxServe starts a server that records every provider body and answers by
// arrival index. A GET answers the model listing a model switch validates.
func pxServe(t *testing.T, answer func(n int, rw http.ResponseWriter)) *pxWire {
	w := &pxWire{answer: answer}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(rw, `{"data":[{"id":"gpt-5.4"},{"id":"gpt-5.5"},{"id":"dial-model"}]}`)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		w.mu.Lock()
		w.got = append(w.got, body)
		n := len(w.got)
		w.mu.Unlock()
		answer(n, rw)
	}))
	t.Cleanup(srv.Close)
	w.url = srv.URL
	return w
}

// reqs returns the recorded bodies, insisting there are want of them.
func (w *pxWire) reqs(t *testing.T, want int) [][]byte {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.got) != want {
		t.Fatalf("provider request count = %d, want %d", len(w.got), want)
	}
	return w.got
}

func pxMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// pxReq is one decoded body. items is the list it replayed: input items for
// Responses, messages for Chat Completions.
type pxReq struct {
	Instructions json.RawMessage   `json:"instructions"`
	Tools        json.RawMessage   `json:"tools"`
	Input        []json.RawMessage `json:"input"`
	Messages     []json.RawMessage `json:"messages"`
	Previous     string            `json:"previous_response_id"`
	items        []json.RawMessage
}

func pxDecode(t *testing.T, body []byte) pxReq {
	t.Helper()
	out := pxReq{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode provider request: %v\n%s", err, body)
	}
	if out.items = out.Input; out.Messages != nil {
		out.items = out.Messages
	}
	return out
}

// pxPrefix fails unless cur reopens with every item of prev, byte for byte.
func pxPrefix(t *testing.T, label string, prev, cur []json.RawMessage) {
	t.Helper()
	if len(cur) < len(prev) {
		t.Fatalf("%s replayed %d items, want at least %d", label, len(cur), len(prev))
	}
	for j := range prev {
		if !bytes.Equal(prev[j], cur[j]) {
			t.Fatalf("%s item %d changed:\n%s\n--- vs ---\n%s", label, j, prev[j], cur[j])
		}
	}
}

// pxStable fails unless each request resends the first one's launch prompt and
// tools and reopens with the whole prefix it sent before, verbatim. It is the
// assertion every test in this file rests on.
func pxStable(t *testing.T, bodies [][]byte) {
	t.Helper()
	first := pxDecode(t, bodies[0])
	prev := first.items
	for i, body := range bodies[1:] {
		cur := pxDecode(t, body)
		if !bytes.Equal(cur.Tools, first.Tools) || !bytes.Equal(cur.Instructions, first.Instructions) {
			t.Fatalf("request %d moved the launch prompt or the tools", i+2)
		}
		pxPrefix(t, fmt.Sprintf("request %d", i+2), prev, cur.items)
		prev = cur.items
	}
}

// pxHas fails unless body carries every fragment verbatim.
func pxHas(t *testing.T, body []byte, want ...string) {
	t.Helper()
	for _, s := range want {
		if !bytes.Contains(body, []byte(s)) {
			t.Fatalf("request body lacks %q:\n%s", s, body)
		}
	}
}

// pxEnvLines finds the appended <environment_context> message item in items
// and parses its "key: value" lines.
func pxEnvLines(t *testing.T, items []json.RawMessage) map[string]string {
	t.Helper()
	for _, raw := range items {
		var msg struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "message" || msg.Role != "user" {
			continue
		}
		for _, part := range msg.Content {
			body, ok := strings.CutPrefix(strings.TrimSpace(part.Text), "<environment_context>")
			if !ok {
				continue
			}
			body, ok = strings.CutSuffix(body, "</environment_context>")
			if !ok {
				continue
			}
			lines := make(map[string]string)
			for line := range strings.SplitSeq(body, "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(line), ": "); ok {
					lines[k] = v
				}
			}
			return lines
		}
	}
	t.Fatalf("tail lacks an <environment_context> message item")
	return nil
}

// pxSSE writes one streamed frame; pxDone ends a turn with communicate/end_turn
// in the shape the named protocol expects.
func pxSSE(rw http.ResponseWriter, format string, a ...any) {
	rw.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(rw, format, a...)
}

func pxDone(n int, chat bool) string {
	if chat {
		return fmt.Sprintf(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_%d","type":"function","function":{"name":"communicate","arguments":%q}}]}`, n, pxDoneArgs)
	}
	return fmt.Sprintf(`[{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":"communicate","arguments":%q}]`, n, n, pxDoneArgs)
}

// pxRegisterProbe registers the probe tool once on a constructed or restored
// session (after initSessionState has already run) so rebuildToolDefsCache
// runs once at registration rather than before each turn.
func pxRegisterProbe(sess *Session) {
	sess.registerTool("probe", "records the call", map[string]any{"type": "object"}, func(context.Context, any) (any, error) { return "probe ok", nil })
}

// pxTurn runs one input to the end of its turn.
func pxTurn(t *testing.T, sess *Session, input string) {
	t.Helper()
	if _, err := sess.ProcessInput(tripwireCtx(t), input, nil); err != nil {
		t.Fatalf("ProcessInput(%q): %v", input, err)
	}
}

// pxDial builds a session that dispatches to the recorder.
func pxDial(t *testing.T, instance, ref string, instances map[string]registry.Provider, opts ...sessionOpt) (*Session, *llm.Client, *provider.Profile, string) {
	t.Helper()
	dir := t.TempDir()
	client := registryClientAt(t, dir, instances, []string{instance})
	profile := withTestSessionNamer(client, resolveClientProfile(t, client, ref))
	sess := newSession(t, append([]sessionOpt{withClient(client), withProfile(profile), withStateDir(dir)}, opts...)...)
	pxRegisterProbe(sess)
	drainSessionEvents(sess)
	return sess, client, profile, dir
}

// pxRestore restores id cold from disk. withEnv re-probes the environment, so
// only the restores that must report where they landed ask for it. support
// replaces the continuation support registry.
func pxRestore(t *testing.T, dir, envDir string, client *llm.Client, profile *provider.Profile, id string, withEnv bool, support map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(dir, id)
	pxMust(t, err)
	cfg := RestoreSessionConfig{StateDir: dir, testOnly: testConfig{
		skipGitSnapshot:                      !withEnv,
		responsesContinuationSupportRegistry: support,
		minimalSystemPrompt:                  true,
		noSyncJobStore:                       true,
	}}
	restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(envDir), meta, cfg)
	pxMust(t, err)
	t.Cleanup(restored.Close)
	pxRegisterProbe(restored)
	drainSessionEvents(restored)
	return restored
}

// pxRun dials a session, drives two live turns, restores it cold for a third,
// and returns the want recorded provider bodies.
func pxRun(t *testing.T, w *pxWire, instance, ref string, instances map[string]registry.Provider, support map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport, want int, opts ...sessionOpt) [][]byte {
	t.Helper()
	sess, client, profile, dir := pxDial(t, instance, ref, instances, opts...)
	pxTurn(t, sess, "first ask")
	pxTurn(t, sess, "second ask")
	id := sess.ID()
	sess.Close()
	pxTurn(t, pxRestore(t, dir, t.TempDir(), client, profile, id, false, support), "third ask")
	return w.reqs(t, want)
}

func pxOpenAI(w *pxWire) map[string]registry.Provider {
	return map[string]registry.Provider{"openai": openaiInstance(w.url)}
}

// pxForkTurn is the 1-based parent entry index of the second user turn.
func pxForkTurn(t *testing.T, sess *Session) int {
	t.Helper()
	users := 0
	for i, turn := range transcriptTurnsOf(t, sess) {
		if turn.Kind == schema.TurnUserInput {
			users++
			if users == 2 {
				return i + 1
			}
		}
	}
	t.Fatalf("parent transcript has no second user turn")
	return 0
}

func TestResponsesWirePrefixByteIdenticalAcrossTurnsAndColdRestore(t *testing.T) {
	t.Parallel()
	w := pxServe(t, func(n int, rw http.ResponseWriter) {
		out := pxResponsesProbe
		if n > 1 {
			out = pxDone(n, false)
		}
		pxSSE(rw, pxResponsesFrame, fmt.Sprintf("resp_%d", n), out)
	})
	bodies := pxRun(t, w, "openai", "openai/gpt-5.4", pxOpenAI(w), nil, 4)
	pxStable(t, bodies)
	// The cold-restored request must still carry the reasoning and the call in
	// the bytes the provider sent them.
	pxHas(t, bodies[3], `"text":"  step 1  "`, `"text":" sum "`, `"id":"fc_1"`, `"call_id":"call_1"`, pxArgsWire)
}

func TestChatCompletionsWirePrefixByteIdenticalAcrossTurnsAndColdRestore(t *testing.T) {
	t.Parallel()
	w := pxServe(t, func(n int, rw http.ResponseWriter) {
		out := pxChatProbe
		if n > 1 {
			out = pxDone(n, true)
		}
		pxSSE(rw, pxChatFrame, fmt.Sprintf("resp_%d", n), out)
	})
	instances := map[string]registry.Provider{"tooldial": {
		Base: "openai-compatible", APIKey: "test", DefaultModel: "dial-model", InheritModels: new(false),
		Transport: registry.Transport{BaseURL: w.url},
		Models:    map[string]registry.Model{"dial-model": {Caps: registry.Caps{ReasoningField: new("reasoning_details")}}},
	}}
	bodies := pxRun(t, w, "tooldial", "tooldial/dial-model", instances, nil, 4)
	pxStable(t, bodies)
	pxHas(t, bodies[0], `"role":"system"`) // messages[0] is the launch prompt
	// The encrypted detail keeps the model's own key order on the way back out:
	// z before type, so nothing canonicalized it.
	pxHas(t, bodies[3], pxArgsWire, `"text":"  why  "`, `{"z":1,"type":"reasoning.encrypted","id":"r1","data":"blob"}`)
}

func TestWirePrefixSurvivesEnvSwapModelSwitchBackAndRootFork(t *testing.T) {
	t.Parallel()
	repoA, repoB := gittest.NewLinkedWorktree(t)
	gittest.Run(t, repoB, "checkout", "-B", "beta")
	w := pxServe(t, func(n int, rw http.ResponseWriter) {
		pxSSE(rw, pxResponsesFrame, fmt.Sprintf("resp_%d", n), pxDone(n, false))
	})
	sess, client, profile, dir := pxDial(t, "openai", "openai/gpt-5.4", pxOpenAI(w), withEnvironmentSnapshot(), withDir(repoA))
	pxTurn(t, sess, "first ask")
	swRes, err := sess.worktreeSwitchByPath(tripwireCtx(t), repoB)
	pxMust(t, err)
	pxTurn(t, sess, "second ask")
	pxMust(t, sess.SetModel("openai/gpt-5.5"))
	pxTurn(t, sess, "third ask")
	pxMust(t, sess.SetModel("openai/gpt-5.4"))
	pxTurn(t, sess, "fourth ask") // a parent request after the switch back, so its own tools and instructions are observable
	childID, err := ForkSession(dir, sess.ID(), pxForkTurn(t, sess), "edited second ask", "")
	pxMust(t, err)
	pxTurn(t, pxRestore(t, dir, swRes.Path, client, profile, childID, true, nil), "child follow-up")

	bodies := w.reqs(t, 5)
	pxStable(t, bodies[:4]) // live turns 1-4: the post-switch-back turn is request 4

	// The launch prompt and tools go out unchanged after switching back to the
	// launch model (request 4) and on the fork child's first request (request 5).
	launch := pxDecode(t, bodies[0])
	for _, i := range []int{3, 4} {
		cur := pxDecode(t, bodies[i])
		if !bytes.Equal(cur.Tools, launch.Tools) || !bytes.Equal(cur.Instructions, launch.Instructions) {
			t.Fatalf("request %d did not resend the launch prompt and tools byte for byte", i+1)
		}
	}

	// The swap turn appends an environment context naming the new worktree and branch.
	swapped := pxDecode(t, bodies[1]).items
	envSwap := pxEnvLines(t, swapped[len(launch.items):])
	gotCwd, err := strconv.Unquote(envSwap["cwd"])
	pxMust(t, err)
	if gotCwd != swRes.Path {
		t.Fatalf("swap cwd = %q, want %q", gotCwd, swRes.Path)
	}
	if envSwap["git branch"] != "beta" {
		t.Fatalf("swap git branch = %q, want %q", envSwap["git branch"], "beta")
	}

	// The model turn appends the switch it made, naming the model asked for.
	envModel := pxEnvLines(t, pxDecode(t, bodies[2]).items[len(swapped):])
	if envModel["model"] != "gpt-5.5" {
		t.Fatalf("model line = %q, want %q", envModel["model"], "gpt-5.5")
	}

	// The child inherits everything the swap turn had sent before the edited
	// message replaced it, including the swapped environment item, and its own
	// appended environment context mints a commit trailer for the child session.
	child := pxDecode(t, bodies[4]).items
	pxPrefix(t, "fork child", swapped[:len(swapped)-1], child)
	envChild := pxEnvLines(t, child[len(swapped)-1:])
	if !strings.Contains(envChild["commit trailer"], "<"+childID+"@") {
		t.Fatalf("child commit trailer = %q, want <%s@...>", envChild["commit trailer"], childID)
	}
}

func TestResponsesContinuationAutoFallbackAndReAnchor(t *testing.T) {
	t.Parallel()
	w := pxServe(t, func(n int, rw http.ResponseWriter) {
		switch n {
		case 1:
			pxSSE(rw, pxResponsesFrame, "resp_1", `[{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"  step 1  "}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"probe","arguments":"{\"a\":1}"}]`)
		case 3: // the stale anchor is rejected, so the session must resend all of it
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(rw, `{"error":{"code":"previous_response_not_found","message":"Previous response with id not found.","type":"invalid_request_error"}}`)
		default:
			pxSSE(rw, pxResponsesFrame, fmt.Sprintf("resp_%d", n), pxDone(n, false))
		}
	})
	support := map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
		llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
	}
	cfg := SessionConfig{OpenAIResponsesContinuation: "auto", testOnly: testConfig{responsesContinuationSupportRegistry: support}}
	bodies := pxRun(t, w, "openai", "openai/gpt-5.4", pxOpenAI(w), support, 5, withConfig(cfg))
	// Requests 1 and 4 are the full-history sends; 3 is the rejected stale
	// anchor and 5 the clean re-anchor on the response 4 stored.
	for i, anchor := range []string{"", "resp_1", "resp_2", "", "resp_4"} {
		if got := pxDecode(t, bodies[i]).Previous; got != anchor {
			t.Fatalf("request %d anchor = %q, want %q", i+1, got, anchor)
		}
	}
	// The rejected-anchor resend is full history, so it reopens with the very
	// items request 1 sent, plaintext reasoning included, plus the linked probe
	// call, its function_call_output result, and the rejected turn's user text.
	fallback := pxDecode(t, bodies[3]).items
	pxPrefix(t, "fallback resend", pxDecode(t, bodies[0]).items, fallback)
	pxHas(t, bodies[3], "  step 1  ")
	var sawProbeCall, sawProbeOutput, sawRejectedAsk bool
	for _, raw := range fallback {
		var item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Output  string `json:"output"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		switch {
		case item.Type == "function_call" && item.CallID == "call_1" && item.Name == "probe":
			sawProbeCall = true
		case item.Type == "function_call_output" && item.CallID == "call_1" && item.Output == "probe ok":
			sawProbeOutput = true
		case item.Type == "message" && item.Role == "user":
			for _, part := range item.Content {
				if part.Text == "second ask" {
					sawRejectedAsk = true
				}
			}
		}
	}
	if !sawProbeCall || !sawProbeOutput || !sawRejectedAsk {
		t.Fatalf("fallback resend missing linked items: probeCall=%v probeOutput=%v rejectedAsk=%v\n%s",
			sawProbeCall, sawProbeOutput, sawRejectedAsk, bodies[3])
	}
}
