package doctor

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"primeradiant.com/evener/agent"
	agentdoctor "primeradiant.com/evener/agent/doctor"
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
		`INSERT INTO messages VALUES (5,'evener:02wLIRxqmq3AUo6vl2OW37',4,'','2026-09-09T01:04:00Z','entry','TOOL_RESULTS','','','','','')`,
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

func TestReconstructScopesTurnIdentityAndSteeringProvenance(t *testing.T) {
	for _, tc := range []struct {
		kind         schema.TurnKind
		ordinal      int
		promptSource string
		stableID     string
		mutationID   string
	}{
		{schema.TurnUserInput, 1, "user", "turn-sentinel", "mutation-sentinel"},
		{schema.TurnSteering, 2, "user", "turn-sentinel", "mutation-sentinel"},
		{schema.TurnSteering, 2, "", "turn-sentinel", ""},
		{schema.TurnFailure, 2, "user", "turn-sentinel", "mutation-sentinel"},
		{schema.TurnAssistant, 3, "user", "", ""},
		{schema.TurnTool, 4, "user", "", ""},
		{schema.TurnToolResults, 4, "user", "", ""},
		{schema.TurnEnvironment, 2, "user", "", ""},
		{schema.TurnCheckpoint, 2, "user", "", ""},
		{schema.TurnSummary, 2, "user", "", ""},
		{schema.TurnSystem, 2, "user", "", ""},
		{schema.TurnModelSwitch, 2, "user", "", ""},
		{schema.TurnHookCompleted, 2, "user", "", ""},
	} {
		t.Run(string(tc.kind)+"/"+tc.promptSource, func(t *testing.T) {
			source := reconstructionSourceFixture(t)
			message := &source.Messages[tc.ordinal]
			message.Kind = string(tc.kind)
			message.StableID = "turn-sentinel"
			message.PromptSource = tc.promptSource
			_, entries, err := reconstructEntries(source, schema.SessionMeta{}, map[string]string{"turn-sentinel": "mutation-sentinel"}, &reconstructionReport{})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(entries[tc.ordinal-1])
			if err != nil {
				t.Fatal(err)
			}
			entry, err := transcript.DecodeEntry(data)
			if err != nil {
				t.Fatal(err)
			}
			turn := entry.Turn
			if turn.Kind != tc.kind || turn.StableTurnID != tc.stableID || turn.ClientMutationID != tc.mutationID {
				t.Errorf("turn identity = (%s, %q, %q), want (%s, %q, %q)", turn.Kind, turn.StableTurnID, turn.ClientMutationID, tc.kind, tc.stableID, tc.mutationID)
			}
			steeringSource := ""
			if tc.kind == schema.TurnSteering {
				steeringSource = tc.promptSource
			}
			if turn.SteeringSource != steeringSource {
				t.Errorf("steering source = %q, want %q", turn.SteeringSource, steeringSource)
			}
		})
	}
}

func TestReconstructionValidationEnforcesNativeRecordSize(t *testing.T) {
	for _, kind := range []string{"header", "entry"} {
		t.Run(kind, func(t *testing.T) {
			header := transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion}
			entry := transcript.Entry{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.Message{Role: llm.RoleUser}}}
			// Escaped content proves the bound applies to encoded record bytes.
			text := strings.Repeat("<", 128)
			if kind == "header" {
				header.SystemPrompt = text
			} else {
				entry.Turn.Message.Content = []llm.ContentPart{{Kind: llm.ContentText, Text: text}}
			}
			var data bytes.Buffer
			encoder := json.NewEncoder(&data)
			if err := encoder.Encode(header); err != nil {
				t.Fatal(err)
			}
			headerBytes := data.Len() - 1
			if err := encoder.Encode(entry); err != nil {
				t.Fatal(err)
			}
			limit := max(headerBytes, data.Len()-headerBytes-2)
			if err := validateReconstructionTranscript(data.Bytes(), limit); err != nil {
				t.Fatalf("rejected record at the payload limit: %v", err)
			}
			if err := validateReconstructionTranscript(data.Bytes(), limit-1); !errors.Is(err, transcript.ErrLineTooLong) {
				t.Fatalf("oversized %s error = %v, want ErrLineTooLong", kind, err)
			}
			if err := validateReconstructionTranscript(data.Bytes()[:data.Len()-1], limit); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("incomplete final record error = %v, want unexpected EOF", err)
			}
		})
	}
}

func TestReconstructMatchesToolResultInstantsAndPreservesRounds(t *testing.T) {
	source := reconstructionSourceFixture(t)
	source.Results[0].Timestamp = "2026-09-08T18:04:00.000-07:00"
	source.Messages = append(source.Messages,
		archivedMessage{ID: 6, Ordinal: 5, SourceType: "entry", Kind: "ASSISTANT", Timestamp: "2026-09-09T01:05:00Z"},
		archivedMessage{ID: 7, Ordinal: 6, SourceType: "entry", Kind: "TOOL_RESULTS", Timestamp: "2026-09-09T01:06:00Z"},
	)
	source.Calls = append(source.Calls, archivedCall{MessageID: 6, ID: "call_2", Name: "read_file", Arguments: `{}`})
	source.Results = append(source.Results, archivedResult{CallOrdinal: 5, ID: "call_2", Source: "tool_result", Status: "completed", Content: "second sentinel", Timestamp: "2026-09-09T01:06:00.000+00:00"})
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
	source.Messages[3].Content += "\n\n[Tool: read_file]"
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

func TestReconstructPreservesLiteralMessageText(t *testing.T) {
	source := reconstructionSourceFixture(t)
	source.Messages[1].Content = "[USER_INPUT]"
	source.Messages[3].Content = "[ASSISTANT]"
	source.Messages = source.Messages[:4]
	source.Calls, source.Results = nil, nil
	_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Turn.Message.Text() != "[USER_INPUT]" || entries[2].Turn.Message.Text() != "[ASSISTANT]" {
		t.Fatal("discarded literal message text")
	}
}

func TestReconstructPreservesLiteralToolMarkerInAssistantText(t *testing.T) {
	source := reconstructionSourceFixture(t)
	const content = "[Tool: read_file]\n\nliteral-sentinel [Tool: read_file] tail-sentinel"
	source.Messages[3].Content = content
	_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	history := agent.ResumeHistory(entries)
	if history[1].Message.Text() != content {
		t.Fatalf("changed ambiguous assistant content: %q", history[1].Message.Text())
	}
	parts := history[1].Message.Content
	if len(parts) != 2 || parts[1].ToolCall == nil || parts[1].ToolCall.ID != "call_1" {
		t.Fatal("lost structured tool call while preserving assistant text")
	}
}

func TestReconstructRejectsMissingCallIndexBeforeStaging(t *testing.T) {
	dbPath, meta, output := reconstructionFixture(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE tool_calls SET call_index=NULL`); err != nil {
		t.Fatal(err)
	}
	_, err = reconstructSession(context.Background(), "02wLIRxqmq3AUo6vl2OW37", dbPath, meta, "", output)
	if err == nil {
		t.Fatal("invented zero index for a call with an unknown position")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("published output with unknown call index: %v", err)
	}
}

func TestReconstructUsesRecordedToolErrorStatus(t *testing.T) {
	for _, status := range []string{"completed", "error"} {
		t.Run(status, func(t *testing.T) {
			source := reconstructionSourceFixture(t)
			source.Results[0].Status = status
			const content = "[tool error] literal-sentinel"
			source.Results[0].Content = content
			if status == "error" {
				source.Results[0].Content = "[tool error] " + content
			}
			_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
			if err != nil {
				t.Fatal(err)
			}
			result := entries[3].Turn.Message.Content[0].ToolResult
			if result == nil || result.IsError != (status == "error") || result.Content != content {
				t.Fatalf("changed literal tool output or error status: %+v", result)
			}
		})
	}
}

func TestReconstructRejectsUnknownResultStatusBeforeStaging(t *testing.T) {
	for _, status := range []string{"", "unknown-status", "ERROR", "running", "idle", "failed", "cancelled", "stopped", "exhausted"} {
		t.Run(status, func(t *testing.T) {
			dbPath, meta, output := reconstructionFixture(t)
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE tool_result_events SET status=?`, status); err != nil {
				t.Fatal(err)
			}
			_, err = reconstructSession(context.Background(), "02wLIRxqmq3AUo6vl2OW37", dbPath, meta, "", output)
			if err == nil {
				t.Fatal("accepted unknown result status")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("published output for unknown status: %v", err)
			}
		})
	}
}

func TestReconstructPreservesSuccessfulDelegateLifecycleResults(t *testing.T) {
	for _, tool := range []string{"delegate", "delegate_send", "job_status"} {
		for _, status := range []string{"running", "idle", "failed", "cancelled", "stopped", "exhausted"} {
			t.Run(tool+"/"+status, func(t *testing.T) {
				source := reconstructionSourceFixture(t)
				source.Calls[0].Name = tool
				source.Results[0].Status = status
				_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
				if err != nil {
					t.Fatal(err)
				}
				history := agent.ResumeHistory(entries)
				result := history[2].Message.Content[0].ToolResult
				if result == nil || result.IsError || result.Content != source.Results[0].Content {
					t.Fatalf("changed successful delegate lifecycle result: %+v", result)
				}
			})
		}
	}
}

func TestReconstructRejectsDuplicateHeaderFieldsBeforeStaging(t *testing.T) {
	for _, kind := range []string{"system_prompt", "initial_task", "agent_tasks"} {
		t.Run(kind, func(t *testing.T) {
			dbPath, meta, output := reconstructionFixture(t)
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE messages SET source_type='header', source_subtype=?, content='[]' WHERE id IN (2,3)`, kind); err != nil {
				t.Fatal(err)
			}
			_, err = reconstructSession(context.Background(), "02wLIRxqmq3AUo6vl2OW37", dbPath, meta, "", output)
			if err == nil {
				t.Fatal("accepted duplicate header field")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("published output for duplicate header: %v", err)
			}
		})
	}
}

func TestReconstructRejectsResultsCrossingToolRoundBoundaries(t *testing.T) {
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnEnvironment, schema.TurnCheckpoint, schema.TurnSummary, schema.TurnSystem, schema.TurnAssistant, schema.TurnModelSwitch, schema.TurnFailure} {
		t.Run(string(kind), func(t *testing.T) {
			source := reconstructionSourceFixture(t)
			tool := source.Messages[4]
			tool.Ordinal = 5
			boundary := archivedMessage{ID: 6, Ordinal: 4, SourceType: "entry", Kind: string(kind), Content: "boundary-sentinel", Timestamp: "2026-09-09T01:03:30Z"}
			source.Messages = append(source.Messages[:4], boundary, tool)
			_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
			if err == nil || entries != nil {
				t.Fatal("accepted a result from an interrupted tool round")
			}
		})
	}
}

func TestReconstructPreservesFailureMutationIdentityOnRestore(t *testing.T) {
	dbPath, metaPath, output := reconstructionFixture(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`UPDATE messages SET source_subtype='TURN_FAILURE',source_uuid='turn_m1',content='{"message":"failure-sentinel"}' WHERE id=3`,
		`DELETE FROM messages WHERE id>=4`, `DELETE FROM tool_calls`, `DELETE FROM tool_result_events`, `UPDATE sessions SET message_count=3`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := filepath.Join(filepath.Dir(metaPath), "journal.json")
	const journal = `{"version":1,"session_id":"02wLIRxqmq3AUo6vl2OW37","journal":{"failed-client":{"client_mutation_id":"failed-client","method":"turn/start","payload":{},"payload_hash":"44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","stable_turn_id":"turn_m1","operation_state":"applied","execution_state":"failureRecording","projection_state":"pending","attempt_generation":1,"failure":{"message":"failure-sentinel"}}},"pending_executions":{"failed-client":{"client_mutation_id":"failed-client","method":"turn/start","input":[{"type":"text","text":"task sentinel"}],"execution_state":"failureRecording","turn_id":"turn_m1","projection_state":"pending"}},"budget_reservations":{},"next_turn_sequence":1,"accepted_turns":1}`
	mustWrite(t, journalPath, journal)
	report, err := reconstructSession(context.Background(), "02wLIRxqmq3AUo6vl2OW37", dbPath, metaPath, journalPath, output)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(output, "mutations"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(output, "mutations", "02wLIRxqmq3AUo6vl2OW37.json"), journal)
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta schema.SessionMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	meta.EnvInfo.WorkingDir = output
	restored, err := agent.RestoreSessionFromMeta(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(output), meta, output)
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
	data, err := os.ReadFile(report.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	var failure schema.Turn
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n"))[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Turn.Kind == schema.TurnFailure {
			failures++
			failure = entry.Turn
		}
	}
	if failures != 1 {
		t.Fatalf("restore appended a duplicate failure: got %d", failures)
	}
	if failure.ClientMutationID != "failed-client" || failure.StableTurnID != "turn_m1" {
		t.Fatal("lost failure mutation identity")
	}
}

func TestReconstructKeepsFailureAndHookMessagesReadable(t *testing.T) {
	hook := schema.HookInfo{Event: "hook-event-sentinel", Matcher: "matcher-sentinel", ExitCode: 17}
	for _, tc := range []struct {
		kind    string
		detail  any
		message string
	}{
		{"TURN_FAILURE", schema.TurnFailureInfo{Message: "failure-readable-sentinel"}, "failure-readable-sentinel"},
		{"HOOK_COMPLETED", hook, hook.Announcement()},
	} {
		for _, prefix := range []string{"", "prefix-sentinel"} {
			t.Run(tc.kind+"/"+prefix, func(t *testing.T) {
				dbPath, meta, output := reconstructionFixture(t)
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				detail, err := json.Marshal(tc.detail)
				if err != nil {
					t.Fatal(err)
				}
				content, want := string(detail), tc.message
				if prefix != "" {
					content, want = prefix+"\n"+content, prefix
				}
				if _, err := db.Exec(`UPDATE messages SET source_subtype=?,content=? WHERE id=3`, tc.kind, content); err != nil {
					t.Fatal(err)
				}
				if _, err := reconstructSession(context.Background(), "02wLIRxqmq3AUo6vl2OW37", dbPath, meta, "", output); err != nil {
					t.Fatal(err)
				}
				view, err := agentdoctor.Transcript(output, "02wLIRxqmq3AUo6vl2OW37", agentdoctor.TranscriptOpts{})
				if err != nil {
					t.Fatal(err)
				}
				if view.Turns[1].Text != want {
					t.Fatalf("rendered diagnostic text = %q, want %q", view.Turns[1].Text, want)
				}
			})
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
	mustWrite(t, journal, reconstructionJournalFixture(t, map[string]string{"client-sentinel": "turn/start", "interrupt-sentinel": "turn/interrupt"}))
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
	mustWrite(t, p, reconstructionJournalFixture(t, map[string]string{"drain-sentinel": "turn/drainAsSteer"}))
	ids, _, err := reconstructionMutationIDs(p, "02wLIRxqmq3AUo6vl2OW37")
	if err != nil {
		t.Fatal(err)
	}
	if ids["turn_m1"] != "drain-sentinel" {
		t.Fatal("lost drained input identity")
	}
}

func reconstructionJournalFixture(t *testing.T, methods map[string]string) string {
	t.Helper()
	records := map[string]any{}
	for id, method := range methods {
		records[id] = map[string]any{
			"client_mutation_id": id, "method": method, "stable_turn_id": "turn_m1",
			"payload_hash": "retained-payload-hash", "operation_state": "terminal",
			"execution_state": "completed", "projection_state": "reflected", "attempt_generation": 1,
		}
	}
	data, err := json.Marshal(map[string]any{
		"version": 1, "session_id": "02wLIRxqmq3AUo6vl2OW37", "journal": records,
		"pending_executions": map[string]any{}, "budget_reservations": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReconstructRejectsInvalidMutationJournalBeforeStaging(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(map[string]any, map[string]any)
	}{
		{"missing journal", func(j, _ map[string]any) { delete(j, "journal") }},
		{"null journal", func(j, _ map[string]any) { j["journal"] = nil }},
		{"null record", func(j, _ map[string]any) { j["journal"].(map[string]any)["client-sentinel"] = nil }},
		{"missing record identity", func(_, r map[string]any) { delete(r, "client_mutation_id") }},
		{"mismatched record identity", func(_, r map[string]any) { r["client_mutation_id"] = "different-client" }},
		{"missing method", func(_, r map[string]any) { delete(r, "method") }},
		{"missing execution state", func(_, r map[string]any) { delete(r, "execution_state") }},
		{"invalid operation state", func(_, r map[string]any) { r["operation_state"] = "invalid" }},
		{"missing attempt generation", func(_, r map[string]any) { delete(r, "attempt_generation") }},
		{"invalid payload hash", func(_, r map[string]any) { r["payload"] = map[string]any{} }},
		{"unknown record field", func(_, r map[string]any) { r["stable_trun_id"] = "turn_m1" }},
		{"unsupported version", func(j, _ map[string]any) { j["version"] = 2 }},
		{"missing pending executions", func(j, _ map[string]any) { delete(j, "pending_executions") }},
		{"missing reservations", func(j, _ map[string]any) { delete(j, "budget_reservations") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, meta, output := reconstructionFixture(t)
			var journal map[string]any
			if err := json.Unmarshal([]byte(reconstructionJournalFixture(t, map[string]string{"client-sentinel": "turn/start"})), &journal); err != nil {
				t.Fatal(err)
			}
			record := journal["journal"].(map[string]any)["client-sentinel"].(map[string]any)
			tc.damage(journal, record)
			data, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(filepath.Dir(meta), "journal.json")
			mustWrite(t, journalPath, string(data))
			var out, errOut bytes.Buffer
			if code := run([]string{"reconstruct", "02wLIRxqmq3AUo6vl2OW37", "--agentsview-db", dbPath, "--meta", meta, "--mutations", journalPath, "--output-dir", output}, &out, &errOut); code == 0 {
				t.Fatal("accepted invalid mutation journal")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("published output for invalid journal: %v", err)
			}
		})
	}
}

func TestReconstructRetainsArchivedCacheUsage(t *testing.T) {
	source := reconstructionSource{Messages: []archivedMessage{
		{ID: 1, Ordinal: 0, SourceType: "header", Kind: "system_prompt", Content: "sentinel"},
		{ID: 2, Ordinal: 1, SourceType: "entry", Kind: "ASSISTANT", Timestamp: "2026-09-09T01:00:00Z", Content: "sentinel", TokenUsage: `{"input_tokens":17,"output_tokens":4,"cache_read_input_tokens":10,"cache_creation_input_tokens":99,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30}}`},
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

func TestReconstructRetainsAggregateCacheUsageWithoutBreakdown(t *testing.T) {
	for _, tc := range []struct {
		name         string
		aggregate    int
		hasBreakdown bool
		breakdown    any
		wantWrite    bool
	}{
		{"aggregate only", 20, false, nil, true},
		{"explicit zero", 0, false, nil, true},
		{"null breakdown", 20, true, nil, true},
		{"empty breakdown", 20, true, map[string]any{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := reconstructionSourceFixture(t)
			usage := map[string]any{"input_tokens": 17, "output_tokens": 4, "cache_creation_input_tokens": tc.aggregate}
			if tc.hasBreakdown {
				usage["cache_creation"] = tc.breakdown
			}
			data, err := json.Marshal(usage)
			if err != nil {
				t.Fatal(err)
			}
			source.Messages[3].TokenUsage = string(data)
			_, entries, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
			if err != nil {
				t.Fatal(err)
			}
			got, total := entries[2].Turn.Usage, 21
			if tc.wantWrite {
				total += tc.aggregate
			}
			if got.TotalTokens != total || (got.CacheWriteTokens != nil) != tc.wantWrite || got.CacheWrite1hTokens != nil {
				t.Fatalf("cache accounting = %+v, want write presence %t and total %d", got, tc.wantWrite, total)
			}
			if tc.wantWrite && *got.CacheWriteTokens != tc.aggregate {
				t.Fatalf("cache writes = %d, want %d", *got.CacheWriteTokens, tc.aggregate)
			}
		})
	}
}
