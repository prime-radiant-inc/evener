package doctor

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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
	if _, err := db.Exec(`UPDATE messages SET source_subtype='ATTENTION_RESOLUTION',content=? WHERE id=3`, "opaque sentinel\n"+`{"attention_id":"attention-sentinel","disposition":"consumed"}`); err != nil {
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
	entry, err := transcript.DecodeEntry(bytes.Split(data, []byte("\n"))[2])
	if err != nil {
		t.Fatal(err)
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
	restored, err := agent.RestoreSessionFromMeta(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(output), metadata, output)
	if err != nil {
		t.Fatalf("reconstructed transcript cannot resume: %v", err)
	}
	defer restored.Close()
	if entry.Turn.Kind != schema.TurnSystem || entry.Turn.AttentionResolution != nil || entry.Turn.Message.Text() == "" {
		t.Fatalf("incomplete attention bookkeeping entered runtime replay: %+v", entry.Turn)
	}
}
