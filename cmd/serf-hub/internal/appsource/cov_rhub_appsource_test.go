package appsource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
)

func newTestCodexSource() *CodexSource {
	return NewCodexSource(CodexSourceConfig{ID: "codex"}, nil)
}

func TestMapCodexThreadStatus(t *testing.T) {
	cases := []struct {
		in   codexThreadStatus
		want string
	}{
		{codexThreadStatus{Type: "active", ActiveFlags: []string{"streaming"}}, appwire.ThreadStatusActive},
		{codexThreadStatus{Type: "idle"}, appwire.ThreadStatusIdle},
		{codexThreadStatus{Type: "systemError"}, appwire.ThreadStatusSystemError},
		{codexThreadStatus{Type: "notLoaded"}, appwire.ThreadStatusNotLoaded},
		{codexThreadStatus{Type: ""}, appwire.ThreadStatusNotLoaded},
		{codexThreadStatus{Type: "custom", ActiveFlags: []string{"x"}}, "custom"},
	}
	for _, tc := range cases {
		got := mapCodexThreadStatus(tc.in)
		if got.Type != tc.want {
			t.Errorf("mapCodexThreadStatus(%q).Type=%q, want %q", tc.in.Type, got.Type, tc.want)
		}
	}
	// The active case forwards ActiveFlags; the default case does too.
	if got := mapCodexThreadStatus(codexThreadStatus{Type: "active", ActiveFlags: []string{"streaming"}}); len(got.ActiveFlags) != 1 {
		t.Fatalf("active flags dropped: %+v", got)
	}
}

func TestCodexInput(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4e, 0x47}

	got, err := codexInput("hi", []appwire.InputItem{
		{Type: "text", Text: "world"},
		{Type: "input_image", URL: " https://x/y.png "},
		{Type: "input_image", Data: pngData, MediaType: "image/jpeg"},
		{Type: "input_image", Data: pngData},
		{Type: "local_image", Path: "/tmp/a.png"},
		{Type: "localImage", Name: "b.png"},
		{Type: "skill", Name: "s", Path: "/p"},
		{Type: "mention", Name: "m", Path: "/q"},
		{Type: "text", Text: ""}, // dropped
	})
	if err != nil {
		t.Fatalf("codexInput: %v", err)
	}
	if got[0]["type"] != "text" || got[0]["text"] != "hi" {
		t.Fatalf("prompt not prepended: %+v", got[0])
	}
	if got[2]["url"] != "https://x/y.png" {
		t.Fatalf("image url not trimmed: %+v", got[2])
	}
	wantData := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(pngData)
	if got[3]["url"] != wantData {
		t.Fatalf("image data url=%v, want %v", got[3]["url"], wantData)
	}
	wantDefault := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)
	if got[4]["url"] != wantDefault {
		t.Fatalf("image default media type=%v", got[4]["url"])
	}
	if got[5]["path"] != "/tmp/a.png" || got[5]["type"] != "localImage" {
		t.Fatalf("local_image=%+v", got[5])
	}
	if got[6]["path"] != "b.png" {
		t.Fatalf("localImage from name=%+v", got[6])
	}
	if got[7]["type"] != "skill" || got[8]["type"] != "mention" {
		t.Fatalf("skill/mention=%+v %+v", got[7], got[8])
	}
	if len(got) != 9 {
		t.Fatalf("empty text item should be dropped, got %d items", len(got))
	}
}

func TestCodexInputErrors(t *testing.T) {
	cases := []struct {
		name string
		item appwire.InputItem
	}{
		{"image without data or url", appwire.InputItem{Type: "input_image"}},
		{"localImage without path", appwire.InputItem{Type: "local_image"}},
		{"skill missing path", appwire.InputItem{Type: "skill", Name: "s"}},
		{"mention missing name", appwire.InputItem{Type: "mention", Path: "/p"}},
		{"unsupported type", appwire.InputItem{Type: "bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := codexInput("", []appwire.InputItem{tc.item}); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestDecodeDataImageURL(t *testing.T) {
	data := []byte("hello")
	valid := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	gotData, mediaType, ok := decodeDataImageURL(valid)
	if !ok || string(gotData) != "hello" || mediaType != "image/png" {
		t.Fatalf("decode valid=%q,%q,%v", gotData, mediaType, ok)
	}

	for name, url := range map[string]string{
		"not a data url": "https://x/y.png",
		"no comma":       "data:image/png;base64",
		"not base64":     "data:image/png,plaintext",
		"bad base64":     "data:image/png;base64,!!!notbase64!!!",
	} {
		if _, _, ok := decodeDataImageURL(url); ok {
			t.Errorf("%s: expected ok=false", name)
		}
	}
}

func TestMapThread(t *testing.T) {
	s := newTestCodexSource()
	thread := codexThread{
		ID:            "th_1",
		Preview:       "hello",
		ModelProvider: "gpt-5",
		Status:        codexThreadStatus{Type: "idle"},
		Turns:         []codexTurn{{ID: "tn_1", Status: "inProgress"}},
	}
	out := s.mapThread(thread)
	if out.ID != "th_1" || out.Source != "codex" {
		t.Fatalf("mapThread base=%+v", out)
	}
	if out.SessionID != "th_1" {
		t.Fatalf("SessionID should fall back to ID: %q", out.SessionID)
	}
	if out.Serf.Ref != "codex:th_1" {
		t.Fatalf("Serf.Ref=%q, want codex:th_1", out.Serf.Ref)
	}
	// idle threads support steer/interrupt.
	if !out.Serf.Capabilities.Steer || !out.Serf.Capabilities.Interrupt {
		t.Fatalf("idle thread must advertise steer/interrupt: %+v", out.Serf.Capabilities)
	}
	if len(out.Turns) != 1 || out.Turns[0].ID != "tn_1" {
		t.Fatalf("turns not mapped: %+v", out.Turns)
	}

	// A notLoaded thread must NOT advertise steer/interrupt.
	cold := s.mapThread(codexThread{ID: "th_2", Status: codexThreadStatus{Type: "notLoaded"}})
	if cold.Serf.Capabilities.Steer || cold.Serf.Capabilities.Interrupt {
		t.Fatalf("cold thread must not advertise steer/interrupt: %+v", cold.Serf.Capabilities)
	}
}

func TestThreadID(t *testing.T) {
	s := newTestCodexSource()

	got, err := s.threadID("codex:th_1", "")
	if err != nil || got != "th_1" {
		t.Fatalf("threadID from ref=%q err=%v", got, err)
	}
	got, err = s.threadID("", "fallback_id")
	if err != nil || got != "fallback_id" {
		t.Fatalf("threadID fallback=%q err=%v", got, err)
	}
	if _, err := s.threadID("other:th_1", ""); err == nil {
		t.Fatal("expected source-mismatch error")
	}
	if _, err := s.threadID("not-a-ref", ""); err == nil {
		t.Fatal("expected parse error for malformed ref")
	}
	if _, err := s.threadID("", ""); err == nil {
		t.Fatal("expected error when neither ref nor fallback provided")
	}
}

func TestAuthHeader(t *testing.T) {
	// Inline bearer token.
	s := NewCodexSource(CodexSourceConfig{ID: "codex", BearerToken: " tok123 "}, nil)
	h, err := s.authHeader()
	if err != nil {
		t.Fatalf("authHeader: %v", err)
	}
	if h.Get("Authorization") != "Bearer tok123" {
		t.Fatalf("Authorization=%q", h.Get("Authorization"))
	}

	// No token at all → nil header, nil error.
	empty := newTestCodexSource()
	h, err = empty.authHeader()
	if err != nil || h != nil {
		t.Fatalf("empty authHeader=%v,%v, want nil,nil", h, err)
	}

	// Token from file.
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("  filetoken\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fileSrc := NewCodexSource(CodexSourceConfig{ID: "codex", BearerTokenFile: tokenFile}, nil)
	h, err = fileSrc.authHeader()
	if err != nil {
		t.Fatalf("file authHeader: %v", err)
	}
	if h.Get("Authorization") != "Bearer filetoken" {
		t.Fatalf("file Authorization=%q", h.Get("Authorization"))
	}

	// Missing file → error.
	missing := NewCodexSource(CodexSourceConfig{ID: "codex", BearerTokenFile: filepath.Join(t.TempDir(), "nope")}, nil)
	if _, err := missing.authHeader(); err == nil {
		t.Fatal("expected error reading missing token file")
	}
}

func TestCodexThreadListStatuses(t *testing.T) {
	if got := codexThreadListStatuses(nil); got != nil {
		t.Fatalf("nil input should yield nil, got %v", got)
	}
	got := codexThreadListStatuses([]string{" active ", "notLoaded", "systemError", "weird"})
	want := []string{"active", "notLoaded", "systemError", "weird"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestCodexThreadSupportsTurnActions(t *testing.T) {
	for _, s := range []string{"active", "idle"} {
		if !codexThreadSupportsTurnActions(s) {
			t.Errorf("%q should support turn actions", s)
		}
	}
	for _, s := range []string{"notLoaded", "systemError", ""} {
		if codexThreadSupportsTurnActions(s) {
			t.Errorf("%q should not support turn actions", s)
		}
	}
}

func TestCodexForkHasEditAtTurnMetadata(t *testing.T) {
	if codexForkHasEditAtTurnMetadata(appwire.ThreadForkParams{}) {
		t.Fatal("empty fork params should have no edit-at-turn metadata")
	}
	if !codexForkHasEditAtTurnMetadata(appwire.ThreadForkParams{SourceTurnID: "tn"}) {
		t.Fatal("SourceTurnID counts as edit-at-turn metadata")
	}
	if !codexForkHasEditAtTurnMetadata(appwire.ThreadForkParams{Label: "l"}) {
		t.Fatal("Label counts as edit-at-turn metadata")
	}
}

func TestMapNotificationRoutes(t *testing.T) {
	s := newTestCodexSource()

	outputDelta := appwire.Notification{
		Method: "item/commandExecution/outputDelta",
		Params: json.RawMessage(`{"turnId":"tn_1","itemId":"it_1","delta":"chunk"}`),
	}
	got := s.mapNotification("th_1", outputDelta)
	if got.Method != appwire.NotifyToolOutputDelta {
		t.Fatalf("outputDelta method=%q", got.Method)
	}
	var od map[string]any
	if err := json.Unmarshal(got.Params, &od); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if od["ref"] != "codex:th_1" || od["delta"] != "chunk" {
		t.Fatalf("outputDelta params=%+v", od)
	}

	status := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: json.RawMessage(`{"status":{"type":"active"}}`),
	}
	got = s.mapNotification("th_9", status)
	var sc appwire.ThreadStatusChangedParams
	if err := json.Unmarshal(got.Params, &sc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sc.ThreadID != "th_9" || sc.Ref != "codex:th_9" || sc.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("threadStatusChanged params=%+v", sc)
	}

	turnCompleted := appwire.Notification{
		Method: appwire.NotifyTurnCompleted,
		Params: json.RawMessage(`{"turn":{"id":"tn_5","status":"interrupted"}}`),
	}
	got = s.mapNotification("th_1", turnCompleted)
	var tc map[string]any
	if err := json.Unmarshal(got.Params, &tc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tc["ref"] != "codex:th_1" {
		t.Fatalf("turnCompleted ref=%v", tc["ref"])
	}

	itemStarted := appwire.Notification{
		Method: appwire.NotifyItemStarted,
		Params: json.RawMessage(`{"turnId":"tn_1","item":{"type":"agentMessage","text":"hi"}}`),
	}
	got = s.mapNotification("th_1", itemStarted)
	if got.Method != appwire.NotifyItemStarted {
		t.Fatalf("itemStarted method=%q", got.Method)
	}

	// Unknown method passes through unchanged.
	unknown := appwire.Notification{Method: "some/other", Params: json.RawMessage(`{}`)}
	if got := s.mapNotification("th_1", unknown); got.Method != "some/other" {
		t.Fatalf("unknown method mutated: %q", got.Method)
	}

	// Malformed params for a known method fall through to the original.
	malformed := appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: json.RawMessage(`not json`)}
	if got := s.mapNotification("th_1", malformed); got.Method != appwire.NotifyAgentMessageDelta || string(got.Params) != "not json" {
		t.Fatalf("malformed params should pass through: %+v", got)
	}
}

func TestNotificationMessageMarshalFallback(t *testing.T) {
	// A value json.Marshal cannot encode (a channel) falls back to "{}".
	got := notificationMessage("m", make(chan int))
	if string(got.Params) != "{}" {
		t.Fatalf("params=%q, want {}", got.Params)
	}
	// A normal value marshals through.
	got = notificationMessage("m", map[string]string{"a": "b"})
	if string(got.Params) != `{"a":"b"}` {
		t.Fatalf("params=%q", got.Params)
	}
}

func TestJSONStringAndHelpers(t *testing.T) {
	if got := jsonString(map[string]int{"x": 1}); got != `{"x":1}` {
		t.Fatalf("jsonString=%q", got)
	}
	if got := jsonString(make(chan int)); got != "" {
		t.Fatalf("jsonString(unmarshalable)=%q, want empty", got)
	}
	if got := firstNonEmpty("", "", "third"); got != "third" {
		t.Fatalf("firstNonEmpty=%q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty all empty=%q", got)
	}
	if emptyNil("") != nil {
		t.Fatal("emptyNil(empty) should be nil")
	}
	if emptyNil("v") != "v" {
		t.Fatal("emptyNil(v) should be v")
	}
}

func TestCodexSourceMetadata(t *testing.T) {
	s := newTestCodexSource()
	if s.ID() != "codex" {
		t.Fatalf("ID()=%q", s.ID())
	}
	if s.RelayOnThreadRead() {
		t.Fatal("codex source must not relay on thread read")
	}
	// An empty config id defaults to "codex".
	if NewCodexSource(CodexSourceConfig{}, nil).ID() != "codex" {
		t.Fatal("empty id should default to codex")
	}
}

// The codex source deliberately does not support several serf-only actions;
// each must fail with an Unavailable error rather than connecting.
func TestCodexSourceUnavailableMethods(t *testing.T) {
	s := newTestCodexSource()
	ctx := context.Background()

	if err := s.ShutdownThread(ctx, appwire.ThreadShutdownParams{}); err == nil {
		t.Error("ShutdownThread should be unavailable")
	}
	if err := s.QueueTurn(ctx, appwire.TurnQueueParams{}); err == nil {
		t.Error("QueueTurn should be unavailable")
	}
	if err := s.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{}); err == nil {
		t.Error("DrainAsSteer should be unavailable")
	}
	if err := s.SetThreadModel(ctx, appwire.ThreadModelSetParams{}); err == nil {
		t.Error("SetThreadModel should be unavailable")
	}
	if err := s.SetThreadReasoningEffort(ctx, appwire.ThreadReasoningEffortSetParams{}); err == nil {
		t.Error("SetThreadReasoningEffort should be unavailable")
	}
	if _, err := s.GoalSet(ctx, appwire.GoalSetParams{}); err == nil {
		t.Error("GoalSet should be unavailable")
	}
	if _, err := s.ClearThread(ctx, appwire.ThreadClearParams{}); err == nil {
		t.Error("ClearThread should be unavailable")
	}
	if _, err := s.ListTasks(ctx, appwire.TaskListParams{}); err == nil {
		t.Error("ListTasks should be unavailable")
	}
}

// Turn actions validate their arguments before dialing the daemon, so bad input
// fails fast without a live connection.
func TestCodexSourceTurnActionPreConnectValidation(t *testing.T) {
	s := newTestCodexSource()
	ctx := context.Background()

	// Bad ref (wrong source) fails before any dial.
	if err := s.SteerTurn(ctx, appwire.TurnSteerParams{Ref: "other:th_1", ExpectedTurnID: "tn_1"}); err == nil {
		t.Error("SteerTurn with foreign ref should error")
	}
	// Missing expectedTurnId is rejected even with a valid ref.
	if err := s.SteerTurn(ctx, appwire.TurnSteerParams{Ref: "codex:th_1"}); err == nil {
		t.Error("SteerTurn without expectedTurnId should error")
	}
	if err := s.InterruptTurn(ctx, appwire.TurnInterruptParams{Ref: "codex:th_1"}); err == nil {
		t.Error("InterruptTurn without expectedTurnId should error")
	}
	if err := s.CompactThread(ctx, appwire.ThreadCompactStartParams{Ref: "other:th_1"}); err == nil {
		t.Error("CompactThread with foreign ref should error")
	}
}

func TestRegistryRemove(t *testing.T) {
	r := NewRegistry()
	src := newTestCodexSource()
	r.Add(src)
	if _, ok := r.Source("codex"); !ok {
		t.Fatal("source not added")
	}
	r.Remove("codex")
	if _, ok := r.Source("codex"); ok {
		t.Fatal("source not removed")
	}
	// Removing a missing id is a no-op.
	r.Remove("nonexistent")
}

func TestMapCodexTurnStatus(t *testing.T) {
	cases := map[string]string{
		"inProgress":  appwire.TurnStatusInProgress,
		"interrupted": appwire.TurnStatusInterrupted,
		"":            appwire.TurnStatusCompleted,
		"weird":       "weird",
	}
	for in, want := range cases {
		if got := mapCodexTurnStatus(in); got != want {
			t.Errorf("mapCodexTurnStatus(%q)=%q, want %q", in, got, want)
		}
	}
}
