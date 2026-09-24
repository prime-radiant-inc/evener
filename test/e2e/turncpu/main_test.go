package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// answer is the tool call the scripted provider made in one reply.
type answer struct {
	name, args string
}

// ask sends one tool-bearing streaming request for session and decodes the
// tool call from the SSE reply.
func ask(t *testing.T, p *provider, session string) answer {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{}],"tools":[{}]}`))
	req.Header.Set("session_id", session)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	var got answer
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", data, err)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				got.name += call.Function.Name
				got.args += call.Function.Arguments
			}
		}
	}
	return got
}

func newTestProvider(o options) *provider {
	return &provider{o: o, workDir: "/work", children: map[string]int{}}
}

// TestProviderEndsLastTurnOnlyAfterEveryDelegateReports pins the round
// script: the root spawns its delegates in its first rounds, runs at least
// --rounds rounds, and keeps working until every delegate has reported; each
// delegate ends after exactly --child-rounds tool rounds.
func TestProviderEndsLastTurnOnlyAfterEveryDelegateReports(t *testing.T) {
	p := newTestProvider(options{turns: 1, rounds: 2, delegates: 1, childRounds: 2})

	if got := ask(t, p, "root"); got.name != "delegate" {
		t.Fatalf("root round 0 = %q, want delegate", got.name)
	}
	if got := ask(t, p, "root"); got.name != "shell" {
		t.Fatalf("root round 1 = %q, want shell", got.name)
	}
	if got := ask(t, p, "root"); got.name != "read_file" {
		t.Fatalf("root round 2 with a delegate outstanding = %q, want another tool round", got.name)
	}
	for i, want := range []string{"read_file", "shell", "communicate"} {
		if got := ask(t, p, "child"); got.name != want {
			t.Fatalf("child round %d = %q, want %s", i, got.name, want)
		}
	}
	got := ask(t, p, "root")
	if got.name != "communicate" || got.args != endTurnArgs {
		t.Fatalf("root after the delegate reported = %+v, want communicate ending the turn", got)
	}
	if p.turn != 1 || p.childDone != 1 || p.allRounds != 7 {
		t.Fatalf("turn=%d childDone=%d allRounds=%d, want 1, 1, 7", p.turn, p.childDone, p.allRounds)
	}
	if want := [][]int{{1, 1, 1}}; !slices.EqualFunc(p.messages, want, slices.Equal) {
		t.Fatalf("messages = %v, want one turn of three measured rounds", p.messages)
	}
}

// TestProviderAnswersTheNamerInTheShapeItAskedFor pins that a request
// without tools (the session namer) gets a JSON completion when it did not
// ask to stream, and SSE when it did.
func TestProviderAnswersTheNamerInTheShapeItAskedFor(t *testing.T) {
	p := newTestProvider(options{turns: 1, rounds: 1})
	namer := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		return rec
	}

	rec := namer(`{"messages":[{}]}`)
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("non-streaming namer Content-Type = %q, want application/json", got)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != `{"name":"CPU Harness"}` {
		t.Fatalf("non-streaming namer reply %q (err %v), want one choice carrying the canned name", rec.Body.String(), err)
	}

	rec = namer(`{"stream":true,"messages":[{}]}`)
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" || !strings.Contains(rec.Body.String(), "CPU Harness") {
		t.Fatalf("streaming namer reply %q (Content-Type %q), want SSE carrying the canned name", rec.Body.String(), got)
	}
	if p.allRounds != 0 {
		t.Fatalf("namer requests counted as %d rounds, want 0", p.allRounds)
	}
}

func TestOptionsValidate(t *testing.T) {
	valid := options{evener: "/bin/evener", turns: 1, rounds: 5, childRounds: 3}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	for name, mutate := range map[string]func(*options){
		"no evener":                  func(o *options) { o.evener = "" },
		"no turns":                   func(o *options) { o.turns = 0 },
		"no rounds":                  func(o *options) { o.rounds = 0 },
		"turns without serve":        func(o *options) { o.turns = 2 },
		"negative payload":           func(o *options) { o.payloadKB = -1 },
		"negative stream bytes":      func(o *options) { o.streamBytes = -1 },
		"negative delegates":         func(o *options) { o.delegates = -1 },
		"negative child rounds":      func(o *options) { o.childRounds = -1 },
		"negative context window":    func(o *options) { o.window = -1 },
		"negative probes":            func(o *options) { o.probes = -1 },
		"more delegates than rounds": func(o *options) { o.delegates = 6 },
	} {
		o := valid
		mutate(&o)
		if err := o.validate(); err == nil {
			t.Errorf("%s: validate accepted %+v", name, o)
		}
	}
}

// TestChildEnvIsolatesEvenerSettings pins that the child never inherits the
// developer's evener settings: an inherited providers.toml path, model or
// state dir would point the run away from the scripted provider and --out.
func TestChildEnvIsolatesEvenerSettings(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin",
		envvars.EVENERProvidersConfig.Name + "=/home/dev/.config/evener/providers.toml",
		envvars.EVENERModel.Name + "=real/model",
		envvars.EVENERHostTempBases.Name + "=/tmp",
	}
	env := childEnv(inherited, "/out/home", "/out/home/config", "/out/tmp")
	for _, kv := range inherited[1:] {
		if slices.Contains(env, kv) {
			t.Errorf("child env carries the inherited %q", kv)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/out/home", "XDG_CONFIG_HOME=/out/home/config", "TMPDIR=/out/tmp", envvars.EVENERHostTempBases.Assignment("/out/tmp"), envvars.EVENEROffline.Assignment("1")} {
		if !slices.Contains(env, want) {
			t.Errorf("child env lacks %q: %v", want, env)
		}
	}
}
