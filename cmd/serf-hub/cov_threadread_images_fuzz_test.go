package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

func covThreadreadServer(t *testing.T) (*WebServer, string, string) {
	t.Helper()
	root, cwd := t.TempDir(), t.TempDir()
	stateDir := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: "01COV", UpdatedAt: time.Unix(1, 0),
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return NewWebServer(hubcore.WebConfig{Past: idx}), cwd, "01COV"
}

func FuzzCovThreadreadImagesSeed100(f *testing.F) {
	for i := byte(0); i < 4; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, scenario byte) {
		switch scenario % 4 {
		case 0:
			covThreadReadSeed(t)
		case 1:
			covDocServeSeed(t)
		case 2:
			covOutputImagesSeed(t)
		case 3:
			covImageServeSeed(t)
		}
	})
}

func covThreadReadSeed(t *testing.T) {
	web, cwd, session := covThreadreadServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatal("missing session")
	}
	transcript := filepath.Join(entry.StateDir, "sessions", session+".transcript.jsonl")
	if err := os.WriteFile(transcript, []byte("bad\n"+`{"kind":"entry","turn":{"kind":"assistant","message":{"role":"assistant","content":[{"kind":"text","text":"ok"}]}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = pastThreadForRead(hubcore.WebConfig{}, appwire.ThreadReadParams{ThreadID: session})
	_, _ = pastThreadForRead(web.cfg, appwire.ThreadReadParams{ThreadID: "missing"})
	_, _ = pastThreadForRead(web.cfg, appwire.ThreadReadParams{ThreadID: session})
	past, _ := pastThreadForRead(web.cfg, appwire.ThreadReadParams{Ref: "local:" + session, IncludeTurns: true})
	_ = pastEntryThread(entry, false)
	_ = pastEntryThread(entry, true)
	_ = windowedReadResponse(past, 1)
	_ = pastEntryTurns(entry)
	for _, p := range []appwire.ThreadReadParams{{}, {ThreadID: " x "}, {Ref: "bad"}, {Ref: "remote:x"}, {Ref: "local:x"}} {
		_, _ = localPastThreadID(p)
	}
	for _, thread := range []appwire.Thread{{}, {Source: "local"}, {Source: "remote"}, {Serf: appwire.SerfThread{Ref: "bad"}}, {Serf: appwire.SerfThread{Ref: "local:x"}}} {
		_ = liveThreadCanMergeLocalPast(thread)
	}
	_ = mergePastThreadForRead(hubcore.WebConfig{}, appwire.ThreadReadParams{}, appwire.Thread{ID: "x"})
	_ = mergePastThreadForRead(hubcore.WebConfig{}, appwire.ThreadReadParams{}, appwire.Thread{SessionID: "x"})
	_ = mergePastThreadForRead(hubcore.WebConfig{}, appwire.ThreadReadParams{}, appwire.Thread{Serf: appwire.SerfThread{Ref: "local:x"}})
	_ = mergePastThreadForRead(web.cfg, appwire.ThreadReadParams{IncludeTurns: true}, appwire.Thread{ID: session, SessionID: session, Preview: session})
	_ = mergePastThreadForRead(web.cfg, appwire.ThreadReadParams{IncludeTurns: true}, appwire.Thread{SessionID: session})
	full := past
	full.Name, full.ModelProvider, full.Path, full.CWD, full.Source, full.Serf.Profile = "n", "m", "p", cwd, "local", "profile"
	_ = mergePastThreadForRead(web.cfg, appwire.ThreadReadParams{ThreadID: session, IncludeTurns: true}, full)

	parts := []hubcore.ReplayPart{
		{Kind: string(llm.ContentText), Text: "text"},
		{Kind: string(llm.ContentThinking)}, {Kind: string(llm.ContentThinking), Thinking: &hubcore.ReplayThinking{Text: "think"}},
		{Kind: string(llm.ContentRedThinking)},
		{Kind: string(llm.ContentAudio)}, {Kind: string(llm.ContentAudio), Audio: &hubcore.ReplayMedia{URL: "u"}},
		{Kind: string(llm.ContentDocument)}, {Kind: string(llm.ContentDocument), Document: &hubcore.ReplayMedia{FileName: "d"}},
		{Kind: string(llm.ContentWebSearch)}, {Kind: string(llm.ContentWebSearch), WebSearch: &hubcore.ReplayWebSearch{Query: "q"}},
		{Kind: string(llm.ContentImage)}, {Kind: string(llm.ContentImage), Image: &hubcore.ReplayImage{}}, {Kind: string(llm.ContentImage), Image: &hubcore.ReplayImage{Data: []byte("i"), Name: "i.png"}},
		{Kind: string(llm.ContentToolCall)}, {Kind: string(llm.ContentToolCall), ToolCall: &hubcore.ReplayToolCall{ID: "c", Name: "shell"}},
		{Kind: string(llm.ContentToolResult)}, {Kind: string(llm.ContentToolResult), ToolResult: &hubcore.ReplayToolResult{ToolCallID: "c"}},
		{Kind: string(llm.ContentToolResult), ToolResult: &hubcore.ReplayToolResult{ToolCallID: "c", ImageData: []byte("img")}},
	}
	turn := hubcore.ReplayTurn{Kind: "assistant", Message: hubcore.ReplayMessage{Role: "assistant", Content: parts}}
	_, _ = replayTurnToAgentTurn(turn)
	_ = appItemsFromReplayTurn("a/b", "turn", 0, turn, map[string]string{})
	_ = appItemsFromReplayTurn("a/b", "user", 1, hubcore.ReplayTurn{Kind: "USER_INPUT", Message: hubcore.ReplayMessage{Role: "user", Content: []hubcore.ReplayPart{{Kind: "image", Image: &hubcore.ReplayImage{}}, {Kind: "image", Image: &hubcore.ReplayImage{Data: []byte("named"), Name: "n.png"}}}}}, map[string]string{})
	_ = appItemsFromReplayTurn("a/b", "tool", 2, hubcore.ReplayTurn{Kind: "TOOL_RESULTS", Message: hubcore.ReplayMessage{Role: "tool", Content: []hubcore.ReplayPart{{Kind: "tool_result"}, {Kind: "tool_result", ToolResult: &hubcore.ReplayToolResult{}}, {Kind: "tool_result", ToolResult: &hubcore.ReplayToolResult{ImageData: []byte("x"), ImageMediaType: "image/jpeg"}}}}}, map[string]string{})

	_ = enrichThreadFileBackedOutputImages(appwire.Thread{})
	_ = enrichThreadFileBackedOutputImages(appwire.Thread{ID: "x"})
	_ = enrichThreadFileBackedOutputImages(appwire.Thread{ID: session, CWD: cwd, Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{Type: "text"}, {Type: "commandExecution", CallID: "c", ArgumentsJSON: `{"file_path":"missing.png"}`}, {Type: "commandExecution", ToolName: "write_file", CallID: "c", Status: appwire.TurnStatusCompleted}}}}})
	_ = appendOutputImagesUnique(nil, nil)
	_ = appendOutputImagesUnique([]appwire.OutputImage{{}}, []appwire.OutputImage{{}, {URL: "u"}, {URL: "u"}, {SHA: "s"}, {Path: "p"}})
	for _, img := range []appwire.OutputImage{{}, {URL: "u"}, {SHA: "s"}, {Path: "p"}} {
		_ = outputImageDescriptorKey(img)
	}

	items := []appwire.ThreadItem{{Type: "text"}, {Type: "commandExecution", ToolName: "other"}, {Type: "commandExecution", ToolName: "delegate"}, {Type: "commandExecution", ToolName: "delegate", Raw: json.RawMessage(`{"job_id":"missing"}`)}}
	thread := appwire.Thread{Turns: []appwire.Turn{{Items: items}, {Items: []appwire.ThreadItem{{Type: "commandExecution", ToolName: "delegate", Raw: json.RawMessage(`{"job_id":"job"}`)}}}}}
	rec := agent.HistoricalJobRecord{JobID: "job", Type: "delegate", Status: "completed", DelegateID: "d", Task: "task", TranscriptRef: "local:child", OriginTurnID: "t", OriginToolCallID: "c", OriginItemID: "i", Reason: "done", OutputBytes: 4}
	_ = reconcileDelegateThreadItems(thread, map[string]agent.HistoricalJobRecord{"job": rec})
	_ = reconcileDelegateThreadItemForTest(appwire.ThreadItem{}, rec)
	for _, bad := range []agent.HistoricalJobRecord{{}, {JobID: "job", Type: "other", Status: "completed"}, {JobID: "job", Type: "delegate", Status: "running"}} {
		_ = reconcileDelegateThreadItem(appwire.ThreadItem{}, bad)
	}
	_ = reconcileDelegateThreadItem(appwire.ThreadItem{Type: "commandExecution", ToolName: "delegate", Raw: json.RawMessage(`{"job_id":"other"}`)}, rec)
	_ = reconcileDelegateThreadItem(appwire.ThreadItem{Type: "commandExecution", ToolName: "delegate", Raw: json.RawMessage(`not-json`)}, rec)
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`x`), json.RawMessage(`{}`), json.RawMessage(`{"started_job_id":" a "}`), json.RawMessage(`{"current_job_id":"b"}`), json.RawMessage(`{"latest_job_id":"c"}`)} {
		_ = delegateJobIDFromRaw(raw)
	}
	for _, status := range []string{"completed", "failed", "cancelled", "stopped", "running"} {
		_ = isTerminalHistoricalJobStatus(status)
	}
}

func covDocServeSeed(t *testing.T) {
	web, cwd, session := covThreadreadServer(t)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/doc/file", nil),
		httptest.NewRequest(http.MethodGet, "/doc/file", nil),
		httptest.NewRequest(http.MethodGet, "/doc/file?session=remote:x&path=a", nil),
		httptest.NewRequest(http.MethodGet, "/doc/file?session="+session+"&path=missing", nil),
		httptest.NewRequest(http.MethodGet, "/doc/file?session="+session+"&path=../x", nil),
	} {
		web.handleDocFile(httptest.NewRecorder(), req)
	}
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/doc/image", nil),
		httptest.NewRequest(http.MethodGet, "/doc/image", nil),
		httptest.NewRequest(http.MethodGet, "/doc/image?session=remote:x&path=a", nil),
		httptest.NewRequest(http.MethodGet, "/doc/image?session="+session+"&path=missing", nil),
		httptest.NewRequest(http.MethodGet, "/doc/image?session="+session+"&path=../x", nil),
	} {
		web.handleDocImage(httptest.NewRecorder(), req)
	}
	files := map[string][]byte{"x.txt": []byte("text"), "x.md": []byte("# md"), "x.markdown": []byte("# md"), "x.bin": []byte{0}, "x.svg": []byte("svg")}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(cwd, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		web.handleDocFile(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/doc/file?session="+session+"&path="+name, nil))
		web.handleDocImage(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/doc/image?session="+session+"&path="+name, nil))
	}
	_, _ = readDocFile(cwd)
	_, _ = readDocFile(filepath.Join(cwd, "missing"))
	if err := os.WriteFile(filepath.Join(cwd, "empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = readDocFile(filepath.Join(cwd, "empty"))
	large := append(make([]byte, 8193), 0)
	_ = looksBinaryBytes(large)
	for _, n := range []int{1, 1024, 1 << 20} {
		_ = formatDocBytes(n)
	}
	writeDocPage(httptest.NewRecorder(), "<x>", "body")
	writeDocMarkdownPage(httptest.NewRecorder(), "<x>", "# x")
}

func covOutputImagesSeed(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "x.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{"", `x`, `{}`, `{"path":"x.png"}`, `{"file_path":"x.png"}`} {
		for _, tool := range []string{"none", "write_file", "edit_file", "apply_patch", "shell", "exec_command"} {
			_ = outputImagesForToolCall("s", cwd, tool, args, `"x.png" 'x.png' x.png https://x/y.png`)
		}
	}
	_ = shellOutputImageCandidates(strings.Repeat(" a.png", outputImageMaxCandidates+2))
	for _, data := range [][]byte{nil, []byte("text"), png, {0xff, 0xd8, 0xff}, []byte("GIF89a"), []byte("RIFF0000WEBP")} {
		_, _ = supportedOutputImageMedia(data, "x")
	}
	_, _ = resolveOutputImageFile("s", cwd, "missing.png", "shell")
	_, _, _ = readOutputImageFile(cwd)
	large := filepath.Join(cwd, "large.png")
	if err := os.WriteFile(large, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, outputImageMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	_, _, _ = readOutputImageFile(large)
	_ = outputImageSHA(png)
	_ = outputImageDisplayName("/")

	base := appwire.Notification{}
	for _, n := range []appwire.Notification{
		base,
		{Method: appwire.NotifyItemStarted},
		{Method: appwire.NotifyItemStarted, Params: json.RawMessage(`x`)},
		{Method: appwire.NotifyItemStarted, Params: json.RawMessage(`{}`)},
		{Method: appwire.NotifyItemStarted, Params: json.RawMessage(`{"item":null}`)},
		{Method: appwire.NotifyItemStarted, Params: json.RawMessage(`{"item":{"type":"text"}}`)},
	} {
		_ = enrichOutputImageNotification("s", cwd, map[string]string{}, n)
	}
	start := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{"item": appwire.ThreadItem{Type: "commandExecution", CallID: "c", ArgumentsJSON: `{"file_path":"x.png"}`}}).Notification
	args := map[string]string{}
	_ = enrichOutputImageNotification("s", cwd, args, *start)
	done := appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{"item": appwire.ThreadItem{Type: "commandExecution", ToolName: "write_file", CallID: "c"}}).Notification
	_ = enrichOutputImageNotification("s", cwd, args, *done)
	direct := appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{"item": appwire.ThreadItem{Type: "commandExecution", ToolName: "write_file", CallID: "d", ArgumentsJSON: `{"file_path":"x.png"}`}}).Notification
	_ = enrichOutputImageNotification("s", cwd, args, *direct)
}

func covImageServeSeed(t *testing.T) {
	web, _, session := covThreadreadServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatal("missing session")
	}
	data := []byte("image")
	sha := imageSha(data)
	path := filepath.Join(entry.StateDir, "sessions", session+".transcript.jsonl")
	lines := []string{"bad", `{"kind":"header"}`, `{"kind":"entry","turn":"bad"}`, `{"kind":"entry","turn":{"message":{"content":[{"kind":"other"},{"kind":"tool_result"},{"kind":"tool_result","tool_result":{}},{"kind":"image"},{"kind":"image","image":{}},{"kind":"image","image":{"data":"aW1hZ2U=","media_type":""}}]}}}`}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _ = findImageInTranscript("missing", sha)
	_, _, _ = findImageInTranscript(path, strings.Repeat("0", 64))
	_, _, _ = findImageInTranscript(path, sha)
	for _, tc := range []struct{ sid, sha string }{{session, "bad"}, {"missing", sha}, {session, strings.Repeat("0", 64)}, {session, sha}} {
		web.handleSessionImage(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), tc.sid, tc.sha)
	}
	(&WebServer{}).handleSessionImage(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), session, sha)
}
