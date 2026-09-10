package doctor

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func reconstructionFixture(t *testing.T) (dbPath, metaPath, output string) {
	t.Helper()
	dir := t.TempDir()
	dbPath, metaPath, output = filepath.Join(dir, "archive.db"), filepath.Join(dir, "session.meta.json"), filepath.Join(dir, "recovered")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE sessions (id TEXT, agent TEXT, parent_session_id TEXT, started_at TEXT, ended_at TEXT, message_count INTEGER, cwd TEXT, source_version TEXT)`,
		`CREATE TABLE messages (id INTEGER, session_id TEXT, ordinal INTEGER, content TEXT, timestamp TEXT, source_type TEXT, source_subtype TEXT, prompt_source TEXT, source_uuid TEXT, model TEXT, provider_id TEXT, token_usage TEXT)`,
		`CREATE TABLE tool_calls (message_id INTEGER, session_id TEXT, tool_name TEXT, tool_use_id TEXT, input_json TEXT, call_index INTEGER)`,
		`CREATE TABLE tool_result_events (session_id TEXT, tool_call_message_ordinal INTEGER, call_index INTEGER, tool_use_id TEXT, source TEXT, status TEXT, content TEXT, content_length INTEGER, timestamp TEXT, event_index INTEGER)`,
		`INSERT INTO sessions VALUES ('evener:02wLIRxqmq3AUo6vl2OW37','evener','','2026-09-09T01:00:00Z','2026-09-09T01:04:00Z',5,'/workspace','test')`,
		`INSERT INTO messages VALUES (1,'evener:02wLIRxqmq3AUo6vl2OW37',0,'instructions','2026-09-09T01:00:00Z','header','system_prompt','','','','','')`,
		`INSERT INTO messages VALUES (2,'evener:02wLIRxqmq3AUo6vl2OW37',1,'task sentinel','2026-09-09T01:01:00Z','entry','USER_INPUT','','turn_m1','','','')`,
		`INSERT INTO messages VALUES (3,'evener:02wLIRxqmq3AUo6vl2OW37',2,'summary sentinel','2026-09-09T01:02:00Z','entry','SUMMARY','','','','','')`,
		`INSERT INTO messages VALUES (4,'evener:02wLIRxqmq3AUo6vl2OW37',3,'[Tool: read_file]','2026-09-09T01:03:00Z','entry','ASSISTANT','','','model','provider','{"input_tokens":17,"output_tokens":4}')`,
		`INSERT INTO messages VALUES (5,'evener:02wLIRxqmq3AUo6vl2OW37',4,'[TOOL_RESULTS]','2026-09-09T01:04:00Z','entry','TOOL_RESULTS','','','','','')`,
		`INSERT INTO tool_calls VALUES (4,'evener:02wLIRxqmq3AUo6vl2OW37','read_file','call_1','{"file_path":"sentinel.txt"}',0)`,
		`INSERT INTO tool_result_events VALUES ('evener:02wLIRxqmq3AUo6vl2OW37',3,0,'call_1','tool_result','completed','result sentinel',15,'2026-09-09T01:04:00Z',0)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, metaPath, `{"id":"02wLIRxqmq3AUo6vl2OW37","profile_id":"provider","model":"model","created_at":"2026-09-09T01:00:00Z","updated_at":"2026-09-09T01:05:00Z","env_info":{"working_dir":"/workspace"},"config":{}}`)
	return
}

func TestReconstructStagesNativeHistoryWithoutChangingSource(t *testing.T) {
	db, meta, output := reconstructionFixture(t)
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	args := []string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", db, "--meta", meta, "--output-dir", output, "--json"}
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, &errOut)
	}
	var report struct {
		TranscriptPath     string `json:"transcript_path"`
		Entries            int    `json:"entries"`
		MissingToolOutputs int    `json:"missing_tool_outputs"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Entries != 5 || report.MissingToolOutputs != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	f, err := os.Open(report.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("missing header")
	}
	h, err := transcript.DecodeHeader(scanner.Bytes())
	if err != nil || h.SessionID != "02wLIRxqmq3AUo6vl2OW37" || h.SystemPrompt != "instructions" {
		t.Fatalf("header: %+v, %v", h, err)
	}
	var entries []transcript.Entry
	for scanner.Scan() {
		e, err := transcript.DecodeEntry(scanner.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if e.Seq != len(entries) {
			t.Fatalf("entry sequence = %d, want %d", e.Seq, len(entries))
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if entries[0].Turn.Message.Text() != "task sentinel" || entries[0].Turn.StableTurnID != "turn_m1" {
		t.Fatal("lost user input or stable identity")
	}
	history := agent.ResumeHistory(entries)
	if len(history) != 4 || history[0].Kind != schema.TurnSummary {
		t.Fatalf("resume compaction boundary: %+v", history)
	}
	call := history[1].Message.Content[0].ToolCall
	result := history[2].Message.Content[0].ToolResult
	if call == nil || result == nil || call.ID != result.ToolCallID || result.Content != "result sentinel" {
		t.Fatal("tool exchange was not reconstructed")
	}
	after, err := os.ReadFile(db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source archive changed")
	}
	transcriptBefore, err := os.ReadFile(report.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if code := run(args, &out, &errOut); code == 0 {
		t.Fatal("overwrote existing recovery output")
	}
	transcriptAfter, err := os.ReadFile(report.TranscriptPath)
	if err != nil || !bytes.Equal(transcriptBefore, transcriptAfter) {
		t.Fatal("existing transcript changed")
	}
}

func TestReconstructReportsFilteredOutputs(t *testing.T) {
	dbPath, meta, output := reconstructionFixture(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE tool_result_events SET content=''`); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", dbPath, "--meta", meta, "--output-dir", output, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, &errOut)
	}
	var report struct {
		MissingToolOutputs int `json:"missing_tool_outputs"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.MissingToolOutputs != 1 {
		t.Fatalf("missing output count: %d", report.MissingToolOutputs)
	}
}

func TestReconstructRejectsIncompleteArchiveWithoutPublishing(t *testing.T) {
	for _, damage := range []string{
		`DELETE FROM tool_result_events`,
		`UPDATE messages SET ordinal=99 WHERE id=5`,
		`UPDATE sessions SET message_count=6`,
		`UPDATE sessions SET parent_session_id='parent'`,
		`UPDATE tool_calls SET input_json='{'`,
		`UPDATE tool_calls SET input_json='null'`,
		`UPDATE tool_calls SET input_json='[]'`,
		`UPDATE tool_calls SET input_json='42'`,
		`UPDATE tool_calls SET input_json='"value"'`,
		`INSERT INTO tool_calls SELECT * FROM tool_calls`,
		`INSERT INTO tool_result_events SELECT session_id,tool_call_message_ordinal,call_index,tool_use_id,source,status,content,content_length,timestamp,event_index+1 FROM tool_result_events`,
		`INSERT INTO tool_result_events SELECT session_id,tool_call_message_ordinal,call_index,tool_use_id,source,status,content,content_length,'2026-09-09T01:05:00Z',event_index+1 FROM tool_result_events; INSERT INTO messages SELECT 6,session_id,5,content,'2026-09-09T01:05:00Z',source_type,source_subtype,prompt_source,source_uuid,model,provider_id,token_usage FROM messages WHERE id=5; UPDATE sessions SET message_count=6`,
		`INSERT INTO messages SELECT 6,session_id,5,content,timestamp,source_type,source_subtype,prompt_source,source_uuid,model,provider_id,token_usage FROM messages WHERE id=5; UPDATE sessions SET message_count=6`,
		`INSERT INTO messages SELECT 6,session_id,5,content,'2026-09-09T01:04:00+00:00',source_type,source_subtype,prompt_source,source_uuid,model,provider_id,token_usage FROM messages WHERE id=5; UPDATE sessions SET message_count=6`,
		`UPDATE messages SET source_subtype='USER_INPUT' WHERE id=4`,
		`UPDATE messages SET source_subtype='TOOL_RESULTS' WHERE id=3`,
		`UPDATE messages SET id=1 WHERE id=2`,
		`UPDATE tool_calls SET call_index=-1`,
		`UPDATE messages SET timestamp='2026-09-09T01:02:00Z' WHERE id=3; UPDATE messages SET source_subtype='TOOL_RESULTS' WHERE id=3; UPDATE messages SET source_subtype='SUMMARY' WHERE id=5; UPDATE tool_result_events SET timestamp='2026-09-09T01:02:00Z'`,
		`UPDATE tool_result_events SET timestamp='2026-09-09T02:00:00Z'`,
	} {
		t.Run(damage, func(t *testing.T) {
			dbPath, meta, output := reconstructionFixture(t)
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(damage); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			if code := run([]string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", dbPath, "--meta", meta, "--output-dir", output}, &out, &errOut); code == 0 {
				t.Fatal("accepted incomplete archive")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("published incomplete output: %v", err)
			}
		})
	}
}

func reconstructionSourceFixture(t *testing.T) reconstructionSource {
	t.Helper()
	dbPath, _, _ := reconstructionFixture(t)
	source, err := readReconstructionSource(context.Background(), dbPath, "02wLIRxqmq3AUo6vl2OW37")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestReconstructMatchesToolResultInstantsAndPreservesRounds(t *testing.T) {
	source := reconstructionSourceFixture(t)
	source.Results[0].Timestamp = "2026-09-08T18:04:00.000-07:00"
	source.Messages = append(source.Messages,
		archivedMessage{ID: 6, Ordinal: 5, SourceType: "entry", Kind: "ASSISTANT", Timestamp: "2026-09-09T01:05:00Z"},
		archivedMessage{ID: 7, Ordinal: 6, SourceType: "entry", Kind: "TOOL_RESULTS", Timestamp: "2026-09-09T01:06:00Z"},
	)
	source.Calls = append(source.Calls, archivedCall{MessageID: 6, ID: "call_2", Name: "read_file", Arguments: `{}`})
	source.Results = append(source.Results, archivedResult{CallOrdinal: 5, ID: "call_2", Source: "tool_result", Content: "second sentinel", Timestamp: "2026-09-09T01:06:00.000+00:00"})
	_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	history := agent.ResumeHistory(entries)
	if len(history) != 6 {
		t.Fatalf("got %d resumed turns, want summary, two tool rounds and notice", len(history))
	}
	for i, want := range []struct{ id, content string }{{"call_1", "result sentinel"}, {"call_2", "second sentinel"}} {
		call := history[1+2*i].Message.Content[0].ToolCall
		parts := history[2+2*i].Message.Content
		if call == nil || call.ID != want.id || len(parts) != 1 || parts[0].ToolResult == nil {
			t.Fatalf("invalid tool round %d", i)
		}
		result := parts[0].ToolResult
		if result.ToolCallID != want.id || result.Content != want.content || result.IsError {
			t.Fatalf("misplaced or synthesized tool result: %+v", result)
		}
	}
}

func TestReconstructNormalizesMissingArgumentsToObject(t *testing.T) {
	source := reconstructionSourceFixture(t)
	source.Calls[0].Arguments = ""
	_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(entries[2].Turn.Message.Content[0].ToolCall.Arguments, &args); err != nil || args == nil || len(args) != 0 {
		t.Fatalf("missing arguments did not become an empty object: %v, %v", args, err)
	}
}

func TestReconstructRejectsAmbiguousOrIncompleteToolCalls(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*reconstructionSource)
	}{
		{"duplicate ID within assistant", func(source *reconstructionSource) {
			call, result := source.Calls[0], source.Results[0]
			call.Index, result.CallIndex = 1, 1
			source.Calls = append(source.Calls, call)
			source.Results = append(source.Results, result)
		}},
		{"duplicate ID across assistants", func(source *reconstructionSource) {
			assistant, tool := source.Messages[3], source.Messages[4]
			assistant.ID, assistant.Ordinal, assistant.Timestamp = 6, 5, "2026-09-09T01:05:00Z"
			tool.ID, tool.Ordinal, tool.Timestamp = 7, 6, "2026-09-09T01:06:00Z"
			source.Messages = append(source.Messages, assistant, tool)
			call, result := source.Calls[0], source.Results[0]
			call.MessageID, result.CallOrdinal, result.Timestamp = assistant.ID, assistant.Ordinal, tool.Timestamp
			source.Calls = append(source.Calls, call)
			source.Results = append(source.Results, result)
		}},
		{"missing first index", func(source *reconstructionSource) {
			source.Calls[0].Index, source.Results[0].CallIndex = 1, 1
		}},
		{"missing middle index", func(source *reconstructionSource) {
			call, result := source.Calls[0], source.Results[0]
			call.ID, result.ID = "call_2", "call_2"
			call.Index, result.CallIndex = 2, 2
			source.Calls = append(source.Calls, call)
			source.Results = append(source.Results, result)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := reconstructionSourceFixture(t)
			test.damage(&source)
			_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
			if err == nil || entries != nil {
				t.Fatal("reconstructed ambiguous or incomplete tool calls")
			}
		})
	}
}

func TestReconstructPreservesMultipleCallsInOneRound(t *testing.T) {
	source := reconstructionSourceFixture(t)
	call, result := source.Calls[0], source.Results[0]
	call.ID, result.ID = "call_2", "call_2"
	call.Index, result.CallIndex = 1, 1
	result.Content = "second-result-sentinel"
	source.Calls = append(source.Calls, call)
	source.Results = append(source.Results, result)
	source.Messages[3].Content += "\n[Tool: read_file]"
	_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	history := agent.ResumeHistory(entries)
	if len(history) != 4 || len(history[1].Message.Content) != 2 || len(history[2].Message.Content) != 2 {
		t.Fatal("lost a call or inserted a synthetic result")
	}
	for i, want := range []struct{ id, content string }{{"call_1", "result sentinel"}, {"call_2", "second-result-sentinel"}} {
		call := history[1].Message.Content[i].ToolCall
		result := history[2].Message.Content[i].ToolResult
		if call == nil || result == nil || call.ID != want.id || result.ToolCallID != want.id || result.Content != want.content || result.IsError {
			t.Fatalf("lost tool exchange %d", i)
		}
	}
}

func TestReconstructPreservesMetadataProfileAndModel(t *testing.T) {
	for _, meta := range []schema.SessionMeta{
		{ProfileID: "profile-sentinel", Model: "configured-model"},
		{ProfileID: "profile-sentinel"},
		{Model: "configured-model"},
		{},
	} {
		source := reconstructionSourceFixture(t)
		h, entries, err := reconstructEntries(source, meta, nil, &reconstructionReport{})
		if err != nil {
			t.Fatal(err)
		}
		wantProfile, wantModel := meta.ProfileID, meta.Model
		if wantProfile == "" {
			wantProfile = "provider"
		}
		if wantModel == "" {
			wantModel = "model"
		}
		if h.ProfileID != wantProfile || h.Model != wantModel {
			t.Fatalf("header identity: %s/%s, want %s/%s", h.ProfileID, h.Model, wantProfile, wantModel)
		}
		if entries[2].Turn.ResponseProvider != "provider" || entries[2].Turn.ResponseModel != "model" {
			t.Fatal("lost per-turn response identity")
		}
	}
}

func TestReconstructRestoresClientMutationIdentity(t *testing.T) {
	dbPath, meta, output := reconstructionFixture(t)
	journal := filepath.Join(filepath.Dir(meta), "mutations.json")
	mustWrite(t, journal, `{"session_id":"02wLIRxqmq3AUo6vl2OW37","journal":{"client-sentinel":{"method":"turn/start","stable_turn_id":"turn_m1"},"interrupt-sentinel":{"method":"turn/interrupt","stable_turn_id":"turn_m1"}}}`)
	var out, errOut bytes.Buffer
	if code := run([]string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", dbPath, "--meta", meta, "--mutations", journal, "--output-dir", output}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, &errOut)
	}
	data, err := os.ReadFile(filepath.Join(output, "sessions", "02wLIRxqmq3AUo6vl2OW37.transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := transcript.DecodeEntry(bytes.Split(data, []byte("\n"))[1])
	if err != nil || entry.Turn.ClientMutationID != "client-sentinel" {
		t.Fatalf("wrong mutation identity: %+v, %v", entry, err)
	}
}

func TestReconstructPreservesAttentionEvidenceWithoutInventingDeliveryState(t *testing.T) {
	dbPath, meta, output := reconstructionFixture(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const evidence = "private-attention-sentinel\n" + `{"attention_id":"attention-sentinel","disposition":"consumed"}`
	if _, err := db.Exec(`UPDATE messages SET source_subtype='ATTENTION_RESOLUTION',content=? WHERE id=3`, evidence); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", dbPath, "--meta", meta, "--output-dir", output}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, &errOut)
	}
	data, err := os.ReadFile(filepath.Join(output, "sessions", "02wLIRxqmq3AUo6vl2OW37.transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(filepath.Join(output, "source-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source reconstructionSource
	if err := json.Unmarshal(snapshot, &source); err != nil {
		t.Fatal(err)
	}
	if source.Messages[2].Kind != "ATTENTION_RESOLUTION" || source.Messages[2].Content != evidence {
		t.Fatal("lost original attention evidence from source snapshot")
	}
	reportBytes, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report reconstructionReport
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatal(err)
	}
	if report.HistoricalAttentionRecords != 1 {
		t.Fatal("report did not count the omitted attention record")
	}
	var metadata schema.SessionMeta
	metaBytes, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.EnvInfo.WorkingDir = output
	requests := make(chan llm.Request, 16)
	client := llm.NewClient()
	client.Register(reconstructionRequestCapture{requests: requests})
	restored, err := agent.RestoreSessionFromMeta(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(output), metadata, output)
	if err != nil {
		t.Fatalf("reconstructed transcript cannot resume: %v", err)
	}
	defer restored.Close()
	if _, err := restored.ProcessInput(context.Background(), "resumed-input-sentinel", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected capture adapter to stop after receiving the request: %v", err)
	}
	foundInput := false
	for len(requests) > 0 {
		request := <-requests
		for _, message := range request.Messages {
			if strings.Contains(message.Text(), "private-attention-sentinel") {
				t.Fatal("reconstruction sent internal attention evidence to the provider")
			}
			foundInput = foundInput || strings.Contains(message.Text(), "resumed-input-sentinel")
		}
	}
	if !foundInput {
		t.Fatal("did not capture the resumed model request")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n"))[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Turn.Kind == schema.TurnAttentionResolution || entry.Turn.AttentionResolution != nil {
			t.Fatalf("incomplete attention bookkeeping entered runtime replay: %+v", entry.Turn)
		}
	}
}

type reconstructionRequestCapture struct{ requests chan<- llm.Request }

func (a reconstructionRequestCapture) Name() string { return "openai" }

func (a reconstructionRequestCapture) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	select {
	case a.requests <- request:
	default:
	}
	return llm.Response{}, context.Canceled
}

func (a reconstructionRequestCapture) Stream(ctx context.Context, request llm.Request) (llm.Stream, error) {
	_, err := a.Complete(ctx, request)
	return nil, err
}

func TestReconstructAttentionInsideToolRoundDoesNotInventAnotherResult(t *testing.T) {
	dbPath, meta, output := reconstructionFixture(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`UPDATE sessions SET message_count=6`,
		`UPDATE messages SET ordinal=5 WHERE id=5`,
		`INSERT INTO messages VALUES (6,'evener:02wLIRxqmq3AUo6vl2OW37',4,'attention sentinel','2026-09-09T01:03:30Z','entry','ATTENTION_RESOLUTION','','','','','')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", dbPath, "--meta", meta, "--output-dir", output}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, &errOut)
	}
	data, err := os.ReadFile(filepath.Join(output, "sessions", "02wLIRxqmq3AUo6vl2OW37.transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []transcript.Entry
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n"))[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	var results int
	for _, turn := range agent.ResumeHistory(entries) {
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil {
				results++
				if part.ToolResult.IsError {
					t.Fatal("resume invented an interrupted-tool error")
				}
			}
		}
	}
	if results != 1 {
		t.Fatalf("got %d results for one archived call", results)
	}
}

func TestReconstructIncludesDrainedSteeringMutationIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mutations.json")
	mustWrite(t, p, `{"session_id":"02wLIRxqmq3AUo6vl2OW37","journal":{"drain-sentinel":{"method":"turn/drainAsSteer","stable_turn_id":"turn_m1"}}}`)
	ids, _, err := reconstructionMutationIDs(p, "02wLIRxqmq3AUo6vl2OW37")
	if err != nil {
		t.Fatal(err)
	}
	if ids["turn_m1"] != "drain-sentinel" {
		t.Fatal("lost drained input identity")
	}
}

func TestReconstructRetainsArchivedCacheUsage(t *testing.T) {
	source := reconstructionSource{Messages: []archivedMessage{
		{ID: 1, Ordinal: 0, SourceType: "header", Kind: "system_prompt", Content: "sentinel"},
		{ID: 2, Ordinal: 1, SourceType: "entry", Kind: "ASSISTANT", Timestamp: "2026-09-09T01:00:00Z", Content: "sentinel", TokenUsage: `{"input_tokens":17,"output_tokens":4,"cache_read_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30}}`},
	}}
	_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	u := entries[0].Turn.Usage
	if u.TotalTokens != 81 || u.CacheWriteTokens == nil || *u.CacheWriteTokens != 20 || u.CacheWrite1hTokens == nil || *u.CacheWrite1hTokens != 30 {
		t.Fatalf("cache usage lost: %+v", u)
	}
}
