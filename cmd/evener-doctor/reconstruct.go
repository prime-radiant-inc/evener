package doctor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Register the driver used to open the external archive read-only.

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

type reconstructionReport struct {
	SessionID                  string    `json:"session_id"`
	SourceDatabase             string    `json:"source_database"`
	SourceSnapshotSHA256       string    `json:"source_snapshot_sha256"`
	RecoveredThrough           string    `json:"recovered_through"`
	MetadataUpdatedAt          time.Time `json:"metadata_updated_at"`
	TranscriptPath             string    `json:"transcript_path"`
	TranscriptSHA256           string    `json:"transcript_sha256"`
	Entries                    int       `json:"entries"`
	ArchivedMessages           int       `json:"archived_messages"`
	ToolCalls                  int       `json:"tool_calls"`
	ToolResults                int       `json:"tool_results"`
	MissingToolOutputs         int       `json:"missing_tool_outputs"`
	HistoricalAttentionRecords int       `json:"historical_attention_records"`
	Limitations                []string  `json:"limitations"`
}

type archivedMessage struct {
	ID, Ordinal                                                                               int
	Content, Timestamp, SourceType, Kind, PromptSource, StableID, Model, Provider, TokenUsage string
}

type archivedCall struct {
	MessageID, Index    int
	Name, ID, Arguments string
}

type archivedResult struct {
	CallOrdinal, CallIndex, ContentLength, EventIndex int
	ID, Source, Status, Content, Timestamp            string
}

type reconstructionSource struct {
	StartedAt, EndedAt, WorkingDir, Version string
	MessageCount                            int
	Messages                                []archivedMessage
	Calls                                   []archivedCall
	Results                                 []archivedResult
}

func cmdReconstruct(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reconstruct", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("agentsview-db", "", "AgentsView sessions.db (opened read-only)")
	metaPath := fs.String("meta", "", "surviving session .meta.json")
	mutationsPath := fs.String("mutations", "", "optional surviving mutation journal, to restore client input identities")
	output := fs.String("output-dir", "", "new staging directory; must not exist")
	asJSON := fs.Bool("json", false, "emit the recovery report as JSON")
	sid, code := parseSelectorAndFlags(fs, args)
	if code != 0 {
		return code
	}
	sid = strings.TrimPrefix(sid, "local:")
	if err := identifier.ValidateSessionID(sid); err != nil {
		return fail(stderr, "reconstruct", err)
	}
	if *dbPath == "" || *metaPath == "" || *output == "" {
		return fail(stderr, "reconstruct", errors.New("--agentsview-db, --meta, and --output-dir are required"))
	}
	report, err := reconstructSession(context.Background(), sid, *dbPath, *metaPath, *mutationsPath, *output)
	if err != nil {
		return fail(stderr, "reconstruct", err)
	}
	if *asJSON {
		return emitJSON(stdout, report)
	}
	return writef(stdout, "%s: staged %d entries through %s; %d tool outputs unavailable\nTranscript: %s\nReport and preserved source: %s\nThis is reconstructed history, not the original transcript. Review report.json before installing.\n", sid, report.Entries, report.RecoveredThrough, report.MissingToolOutputs, report.TranscriptPath, filepath.Dir(filepath.Dir(report.TranscriptPath)))
}

func reconstructSession(ctx context.Context, sid, dbPath, metaPath, mutationsPath, output string) (reconstructionReport, error) {
	var report reconstructionReport
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		return report, err
	}
	var meta schema.SessionMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return report, err
	}
	if meta.ID != sid || meta.IsSubagent || meta.ParentSessionID != "" {
		return report, errors.New("reconstruction requires matching metadata for a root session; forked and delegated histories are not supported")
	}
	source, err := readReconstructionSource(ctx, dbPath, sid)
	if err != nil {
		return report, err
	}
	mutationIDs, mutationBytes, err := reconstructionMutationIDs(mutationsPath, sid)
	if err != nil {
		return report, err
	}
	report = reconstructionReport{SessionID: sid, SourceDatabase: dbPath, RecoveredThrough: source.EndedAt, MetadataUpdatedAt: meta.UpdatedAt, ArchivedMessages: len(source.Messages), ToolCalls: len(source.Calls), ToolResults: len(source.Results), Limitations: []string{
		"Reconstructed from normalized AgentsView records, not original transcript bytes. Content-part boundaries and whitespace may differ.",
		"Archive coverage ends at recovered_through; surviving metadata may describe later work whose conversation is unavailable.",
		"Media bytes, provider replay identifiers/signatures, message phases, and some private runtime fields are unavailable. Archived reasoning is retained as readable text.",
		"Tool outputs omitted by the archive are replaced with explicit unavailable-content notices. Those notices do not prove the tool succeeded or failed.",
		"Process, job, delegate, attention, queue, and task state are not reconstructed. Verify current files and process state before continuing work.",
		"Attention-resolution records are preserved only in source-snapshot.json and counted as historical_attention_records. Their missing originating delivery IDs make them unsuitable for runtime replay, and they must not enter model history.",
	}}
	header, entries, err := reconstructEntries(source, meta, mutationIDs, &report)
	if err != nil {
		return report, err
	}
	report.Entries = len(entries)
	var transcriptBytes bytes.Buffer
	encoder := json.NewEncoder(&transcriptBytes)
	if err := encoder.Encode(header); err != nil {
		return report, err
	}
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return report, err
		}
	}
	if err := validateReconstructionTranscript(transcriptBytes.Bytes(), transcript.DefaultMaxLineBytes); err != nil {
		return report, err
	}
	sourceBytes, err := json.MarshalIndent(source, "", "  ")
	if err != nil {
		return report, err
	}
	report.SourceSnapshotSHA256 = fmt.Sprintf("%x", sha256.Sum256(sourceBytes))
	report.TranscriptSHA256 = fmt.Sprintf("%x", sha256.Sum256(transcriptBytes.Bytes()))
	output, err = filepath.Abs(output)
	if err != nil {
		return report, err
	}
	report.TranscriptPath = filepath.Join(output, "sessions", sid+".transcript.jsonl")
	if err := os.Mkdir(output, 0o700); err != nil {
		return report, fmt.Errorf("create new staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(output)
		}
	}()
	if err := os.Mkdir(filepath.Join(output, "sessions"), 0o700); err != nil {
		return report, err
	}
	files := map[string][]byte{report.TranscriptPath: transcriptBytes.Bytes(), filepath.Join(output, "sessions", sid+".meta.json"): metaBytes, filepath.Join(output, "source-snapshot.json"): sourceBytes}
	if mutationBytes != nil {
		files[filepath.Join(output, "source-mutations.json")] = mutationBytes
	}
	reportBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	files[filepath.Join(output, "report.json")] = reportBytes
	for name, data := range files {
		if err := writeReconstructionFile(name, data); err != nil {
			return report, err
		}
	}
	complete = true
	return report, nil
}

func validateReconstructionTranscript(data []byte, maxLineBytes int) error {
	// Match native restore's bounded framing as well as its record schema.
	reader := bufio.NewReader(bytes.NewReader(data))
	for i := 0; ; i++ {
		line, complete, consumed, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil {
			return err
		}
		if !complete {
			if i == 0 || consumed > 0 {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if i == 0 {
			_, err = transcript.DecodeHeader(line)
		} else {
			_, err = transcript.DecodeEntry(line)
		}
		if err != nil {
			return err
		}
	}
}

func writeReconstructionFile(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	return errors.Join(writeErr, syncErr, f.Close())
}

func readReconstructionSource(ctx context.Context, dbPath, sid string) (reconstructionSource, error) {
	var source reconstructionSource
	absolute, err := filepath.Abs(dbPath)
	if err != nil {
		return source, err
	}
	u := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return source, err
	}
	defer db.Close() //nolint:errcheck // read-only database
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source, err
	}
	defer tx.Rollback() //nolint:errcheck // preserve one read snapshot, never commit changes
	archiveID := "evener:" + sid
	var provider, parent string
	err = tx.QueryRowContext(ctx, `SELECT agent, COALESCE(parent_session_id,''), COALESCE(started_at,''), COALESCE(ended_at,''), message_count, COALESCE(cwd,''), COALESCE(source_version,'') FROM sessions WHERE id=?`, archiveID).Scan(&provider, &parent, &source.StartedAt, &source.EndedAt, &source.MessageCount, &source.WorkingDir, &source.Version)
	if err != nil {
		return source, err
	}
	if provider != "evener" || parent != "" {
		return source, errors.New("archive must be an Evener root session")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, ordinal, COALESCE(content,''), COALESCE(timestamp,''), COALESCE(source_type,''), COALESCE(source_subtype,''), COALESCE(prompt_source,''), COALESCE(source_uuid,''), COALESCE(model,''), COALESCE(provider_id,''), COALESCE(token_usage,'') FROM messages WHERE session_id=? ORDER BY ordinal`, archiveID)
	if err != nil {
		return source, err
	}
	for rows.Next() {
		var m archivedMessage
		if err := rows.Scan(&m.ID, &m.Ordinal, &m.Content, &m.Timestamp, &m.SourceType, &m.Kind, &m.PromptSource, &m.StableID, &m.Model, &m.Provider, &m.TokenUsage); err != nil {
			_ = rows.Close()
			return source, err
		}
		source.Messages = append(source.Messages, m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return source, err
	}
	if len(source.Messages) != source.MessageCount || source.MessageCount == 0 {
		return source, errors.New("archive message count is incomplete or empty")
	}
	rows, err = tx.QueryContext(ctx, `SELECT message_id, tool_name, COALESCE(tool_use_id,''), COALESCE(input_json,''), call_index FROM tool_calls WHERE session_id=? ORDER BY message_id, call_index`, archiveID)
	if err != nil {
		return source, err
	}
	for rows.Next() {
		var c archivedCall
		if err := rows.Scan(&c.MessageID, &c.Name, &c.ID, &c.Arguments, &c.Index); err != nil {
			_ = rows.Close()
			return source, err
		}
		source.Calls = append(source.Calls, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return source, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT tool_call_message_ordinal, call_index, tool_use_id, source, status, COALESCE(content,''), content_length, COALESCE(timestamp,''), event_index FROM tool_result_events WHERE session_id=? ORDER BY tool_call_message_ordinal, call_index, event_index`, archiveID)
	if err != nil {
		return source, err
	}
	for rows.Next() {
		var r archivedResult
		if err := rows.Scan(&r.CallOrdinal, &r.CallIndex, &r.ID, &r.Source, &r.Status, &r.Content, &r.ContentLength, &r.Timestamp, &r.EventIndex); err != nil {
			_ = rows.Close()
			return source, err
		}
		source.Results = append(source.Results, r)
	}
	return source, errors.Join(rows.Err(), rows.Close())
}

func reconstructionMutationIDs(path, sid string) (map[string]string, []byte, error) {
	ids := map[string]string{}
	if path == "" {
		return ids, nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	ids, err = agent.ClientMutationInputIdentities(data, sid)
	return ids, data, err
}

func reconstructEntries(source reconstructionSource, meta schema.SessionMeta, mutations map[string]string, report *reconstructionReport) (transcript.Header, []transcript.Entry, error) {
	h := transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: meta.ID, CreatedAt: meta.CreatedAt, ProfileID: meta.ProfileID, Model: meta.Model, WorkingDir: source.WorkingDir, BuildVersion: source.Version}
	var entries []transcript.Entry
	fail := func(err error) (transcript.Header, []transcript.Entry, error) { return h, nil, err }
	calls := map[int][]archivedCall{}
	callLocations := map[[2]int]archivedCall{}
	callIDs := map[string]bool{}
	messages := map[int]archivedMessage{}
	toolOrdinals := map[time.Time]int{}
	for i, m := range source.Messages {
		if m.Ordinal != i {
			return fail(errors.New("archive ordinals contain gaps or duplicates"))
		}
		if _, exists := messages[m.ID]; exists {
			return fail(fmt.Errorf("duplicate archived message ID %d", m.ID))
		}
		messages[m.ID] = m
		if m.SourceType == "entry" && (m.Kind == string(schema.TurnTool) || m.Kind == string(schema.TurnToolResults)) {
			stamp, err := time.Parse(time.RFC3339Nano, m.Timestamp)
			if err != nil {
				return fail(err)
			}
			// The archive identifies result messages only by timestamp. Equal
			// instants cannot be assigned to distinct messages without guessing.
			stamp = stamp.UTC()
			if _, exists := toolOrdinals[stamp]; exists {
				return fail(fmt.Errorf("ambiguous archived tool-result timestamp %s", m.Timestamp))
			}
			toolOrdinals[stamp] = m.Ordinal
		}
	}
	for _, c := range source.Calls {
		m, ok := messages[c.MessageID]
		if !ok || m.SourceType != "entry" || m.Kind != string(schema.TurnAssistant) || c.Index < 0 || c.ID == "" || c.Name == "" {
			return fail(errors.New("invalid or unlinked archived tool call"))
		}
		key := [2]int{m.Ordinal, c.Index}
		if _, exists := callLocations[key]; exists {
			return fail(fmt.Errorf("duplicate archived tool call at ordinal %d index %d", m.Ordinal, c.Index))
		}
		if c.Index != len(calls[c.MessageID]) {
			return fail(fmt.Errorf("non-contiguous archived tool call indices at ordinal %d", m.Ordinal))
		}
		if callIDs[c.ID] {
			return fail(fmt.Errorf("duplicate archived tool call ID %s", c.ID))
		}
		callIDs[c.ID] = true
		if c.Arguments == "" {
			c.Arguments = "{}"
		}
		var arguments map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.Arguments), &arguments); err != nil || arguments == nil {
			return fail(fmt.Errorf("archived tool call %s arguments must be a JSON object", c.ID))
		}
		calls[c.MessageID] = append(calls[c.MessageID], c)
		callLocations[key] = c
	}
	results := map[int][]archivedResult{}
	resultCalls := map[[2]int]bool{}
	for _, r := range source.Results {
		key := [2]int{r.CallOrdinal, r.CallIndex}
		c, ok := callLocations[key]
		if !ok || c.ID != r.ID || r.Source != "tool_result" {
			return fail(errors.New("invalid or unlinked archived tool result"))
		}
		if !validArchivedResultStatus(c.Name, r.Status) {
			return fail(fmt.Errorf("unsupported archived tool result status %q for %s", r.Status, c.Name))
		}
		if resultCalls[key] {
			return fail(fmt.Errorf("duplicate archived tool result at call ordinal %d index %d", r.CallOrdinal, r.CallIndex))
		}
		stamp, err := time.Parse(time.RFC3339Nano, r.Timestamp)
		if err != nil {
			return fail(err)
		}
		ordinal, ok := toolOrdinals[stamp.UTC()]
		if !ok || ordinal <= r.CallOrdinal {
			return fail(errors.New("archive has tool result events without a matching message after their call"))
		}
		results[ordinal] = append(results[ordinal], r)
		resultCalls[key] = true
	}
	if len(resultCalls) != len(callLocations) {
		return fail(errors.New("archive has tool calls without recoverable result events"))
	}
	firstAssistant := true
	toolRoundOrdinal := -1
	headerFields := map[string]bool{}
	for _, m := range source.Messages {
		if m.SourceType == "header" {
			if headerFields[m.Kind] {
				return fail(fmt.Errorf("duplicate archived header field %q", m.Kind))
			}
			headerFields[m.Kind] = true
			switch m.Kind {
			case "system_prompt":
				h.SystemPrompt = m.Content
			case "initial_task":
				h.Task = m.Content
			case "agent_tasks":
				if err := json.Unmarshal([]byte(m.Content), &h.AgentTasks); err != nil {
					return fail(err)
				}
			default:
				return fail(fmt.Errorf("unknown archived header field %q", m.Kind))
			}
			continue
		}
		if m.SourceType != "entry" {
			return fail(fmt.Errorf("unknown archive source type %q", m.SourceType))
		}
		stamp, err := time.Parse(time.RFC3339Nano, m.Timestamp)
		if err != nil {
			return fail(err)
		}
		turn := schema.Turn{Kind: schema.TurnKind(m.Kind), Timestamp: stamp, StableTurnID: m.StableID, SteeringSource: m.PromptSource}
		content := m.Content
		switch turn.Kind {
		case schema.TurnTool, schema.TurnToolResults, schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnSteering:
			// Native history permits these records inside a pending tool round.
		default:
			toolRoundOrdinal = m.Ordinal
		}
		switch turn.Kind {
		case schema.TurnUserInput, schema.TurnSteering, schema.TurnEnvironment, schema.TurnCheckpoint, schema.TurnSummary:
			turn.Message.Role = llm.RoleUser
			if turn.Kind == schema.TurnUserInput || (turn.Kind == schema.TurnSteering && m.PromptSource == "user") {
				turn.ClientMutationID = mutations[m.StableID]
			}
		case schema.TurnAssistant:
			turn.Message.Role = llm.RoleAssistant
			turn.ResponseModel, turn.ResponseProvider = m.Model, m.Provider
			if firstAssistant {
				if h.Model == "" {
					h.Model = m.Model
				}
				if h.ProfileID == "" {
					h.ProfileID = m.Provider
				}
				firstAssistant = false
			}
			if m.TokenUsage != "" {
				var usage struct {
					Input         int  `json:"input_tokens"`
					Output        int  `json:"output_tokens"`
					Reasoning     *int `json:"reasoning_tokens"`
					CacheRead     *int `json:"cache_read_input_tokens"`
					CacheWrite    *int `json:"cache_creation_input_tokens"`
					CacheCreation *struct {
						FiveMinute *int `json:"ephemeral_5m_input_tokens"`
						OneHour    *int `json:"ephemeral_1h_input_tokens"`
					} `json:"cache_creation"`
				}
				if err := json.Unmarshal([]byte(m.TokenUsage), &usage); err != nil {
					return fail(err)
				}
				turn.Usage = llm.Usage{InputTokens: usage.Input, OutputTokens: usage.Output, TotalTokens: usage.Input + usage.Output, ReasoningTokens: usage.Reasoning, CacheReadTokens: usage.CacheRead, CacheWriteTokens: usage.CacheWrite}
				if usage.CacheCreation != nil {
					turn.Usage.CacheWriteTokens = usage.CacheCreation.FiveMinute
					turn.Usage.CacheWrite1hTokens = usage.CacheCreation.OneHour
				}
				for _, cached := range []*int{turn.Usage.CacheReadTokens, turn.Usage.CacheWriteTokens, turn.Usage.CacheWrite1hTokens} {
					if cached != nil {
						turn.Usage.TotalTokens += *cached
					}
				}
			}
			var markers []string
			for _, c := range calls[m.ID] {
				markers = append(markers, "[Tool: "+c.Name+"]")
			}
			// Only a canonical tool-only block has unambiguous provenance.
			// In mixed content a matching marker may be literal assistant text.
			if content == strings.Join(markers, "\n\n") {
				content = ""
			}
		case schema.TurnTool, schema.TurnToolResults:
			turn.Message.Role = llm.RoleTool
			if len(results[m.Ordinal]) == 0 {
				return fail(fmt.Errorf("archived tool-result message at ordinal %d has no recoverable results", m.Ordinal))
			}
			for _, r := range results[m.Ordinal] {
				if r.CallOrdinal != toolRoundOrdinal {
					return fail(fmt.Errorf("archived tool result at ordinal %d crosses a tool-round boundary", m.Ordinal))
				}
				c := callLocations[[2]int{r.CallOrdinal, r.CallIndex}]
				text := r.Content
				if text == "" && r.ContentLength > 0 {
					report.MissingToolOutputs++
					text = fmt.Sprintf("[Session reconstruction: this tool result body (%d bytes) was not retained by the archive. Its contents are unavailable; verify current state before relying on it.]", r.ContentLength)
				}
				isError := r.Status == "error"
				if isError {
					text = strings.TrimPrefix(text, "[tool error] ")
				}
				turn.Message.Content = append(turn.Message.Content, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: r.ID, Name: c.Name, Content: text, IsError: isError}})
			}
		case schema.TurnAttentionResolution:
			// The source snapshot retains this evidence. Native resolutions need
			// originating delivery IDs that the archive omits; converting them to
			// another turn kind would expose private bookkeeping to the model.
			report.HistoricalAttentionRecords++
			continue
		case schema.TurnSystem, schema.TurnModelSwitch, schema.TurnFailure, schema.TurnHookCompleted:
			turn.Message.Role = llm.RoleSystem
			if turn.Kind == schema.TurnFailure {
				turn.ClientMutationID = mutations[m.StableID]
			}
			detail, prefix := content, ""
			if n := strings.LastIndex(content, "\n{"); n >= 0 {
				detail, prefix = content[n+1:], content[:n]
			}
			if json.Valid([]byte(detail)) {
				switch turn.Kind {
				case schema.TurnFailure:
					err = json.Unmarshal([]byte(detail), &turn.Error)
				case schema.TurnHookCompleted:
					err = json.Unmarshal([]byte(detail), &turn.Hook)
				}
				if err != nil {
					return fail(err)
				}
				if turn.Error != nil || turn.Hook != nil {
					content = prefix
					if strings.TrimSpace(content) == "" {
						if turn.Error != nil {
							content = turn.Error.Message
						} else {
							content = turn.Hook.Announcement()
						}
					}
				}
			}
		default:
			return fail(fmt.Errorf("unsupported archived turn kind %q", m.Kind))
		}
		if strings.TrimSpace(content) != "" {
			turn.Message.Content = append([]llm.ContentPart{{Kind: llm.ContentText, Text: strings.TrimSpace(content)}}, turn.Message.Content...)
		}
		for _, c := range calls[m.ID] {
			turn.Message.Content = append(turn.Message.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: c.ID, Name: c.Name, Arguments: json.RawMessage(c.Arguments)}})
		}
		entries = append(entries, transcript.Entry{Kind: "entry", Seq: len(entries), Turn: turn})
	}
	if len(entries) == 0 || h.SystemPrompt == "" {
		return fail(errors.New("archive lacks conversation entries or initial system prompt"))
	}
	note := fmt.Sprintf("[SESSION RECONSTRUCTION]\nThis session was reconstructed from the AgentsView archive through %s. Later conversation may be missing; metadata was last updated %s. %d tool-result bodies were not retained and carry explicit unavailable notices. The original live processes were interrupted; archived claims about running processes, jobs, delegates, or test status are historical evidence only. Recheck the working tree and current state before continuing. Media and provider replay signatures were not retained. Full recovery provenance is in the staged report.json.", source.EndedAt, meta.UpdatedAt.Format(time.RFC3339Nano), report.MissingToolOutputs)
	entries = append(entries, transcript.Entry{Kind: "entry", Seq: len(entries), Turn: schema.Turn{Kind: schema.TurnSteering, Message: llm.User(note), Timestamp: time.Now().UTC()}})
	return h, entries, nil
}

func validArchivedResultStatus(toolName, status string) bool {
	switch status {
	case "completed", "error":
		return true
	case "running", "idle", "failed", "cancelled", "stopped", "exhausted":
		// AgentsView uses delegate lifecycle as the status of successful calls
		// to these tools. A failed delegate is not a failed status query.
		return toolName == "delegate" || toolName == "delegate_send" || toolName == "job_status"
	default:
		return false
	}
}
