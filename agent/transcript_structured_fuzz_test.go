//go:build serffuzz

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/schemagen"
	"primeradiant.com/serf/llm"
)

// generateTranscript builds a valid-but-adversarial transcript JSONL blob from a
// byte Source. It is the structure-aware counterpart to the raw-byte
// FuzzTranscriptReplay corpus: where random bytes almost always die at the first
// json.Unmarshal of the header line (so readTranscriptFull returns early and the
// write/read and resume oracles never run), this synthesizes a real header
// followed by a legal sequence of entries covering EVERY turn kind
// (USER_INPUT/STEERING/ASSISTANT/TOOL/TOOL_RESULTS/SYSTEM/CHECKPOINT/SUMMARY) and
// every content-part kind (text/thinking/redacted_thinking/tool_call/tool_result/
// image/audio/document/web_search), interleaved with api_call lines — so the
// fuzzer spends its budget inside readTranscriptFull, the strict child reader,
// ResumeHistory, and repairOrphanedToolResults rather than in the JSON tokenizer.
//
// Adversarial content lives where the typed decoders accept it: tool_call/result
// pairing spans matched pairs, orphaned calls (forcing synthetic-result repair),
// and orphaned results; bodies use schemagen so the opaque/any regions
// (ToolResult.Content, ToolCall.Arguments, WebSearch.Raw, Usage.Raw) carry weird
// but well-typed values. The envelope (kind, valid RFC3339 timestamps, object
// shapes for map-typed fields, base64-able byte fields) is always structurally
// valid so each line decodes; that keeps the entry-yield rate near 100% — see
// TestStructuredTranscriptReachesDeeper for the gap it opens over raw bytes.
//
// Determinism: the only entropy is the byte Source, drawn through schemagen's
// deterministic primitives. Output ORDER never depends on map iteration — every
// object is a map[string]any handed to json.Marshal, which sorts keys; slices are
// built in source-driven order; no time/rand is consulted. The same bytes always
// yield byte-identical JSONL, so Go's fuzzer can persist crashers.
func generateTranscript(s schemagen.Source) ([]byte, error) {
	g := &transcriptGen{s: s}
	// Draw the line count before the header's optional fields so even a short
	// byte budget reaches the entry-generation path (a longer header would
	// otherwise exhaust the source first and yield a header-only transcript).
	n := s.IntRange(0, 10, "num_lines")
	if err := g.emit(g.header()); err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		if s.Bool("emit_api_call") {
			if err := g.emit(g.apiCall()); err != nil {
				return nil, err
			}
			continue
		}
		if err := g.emit(g.entry()); err != nil {
			return nil, err
		}
	}
	return bytes.Join(g.lines, []byte("\n")), nil
}

// transcriptGen accumulates JSONL lines and the cross-line state (sequence
// counter, outstanding tool-call IDs) the pairing logic needs.
type transcriptGen struct {
	s         schemagen.Source
	lines     [][]byte
	seq       int
	pending   []string // tool-call IDs emitted but not yet resolved by a result
	callSeq   int
	orphanSeq int
}

func (g *transcriptGen) emit(obj map[string]any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal transcript line: %w", err)
	}
	g.lines = append(g.lines, data)
	return nil
}

func (g *transcriptGen) nextSeq() int {
	v := g.seq
	g.seq++
	return v
}

func (g *transcriptGen) newCallID() string {
	id := "call_" + strconv.Itoa(g.callSeq)
	g.callSeq++
	return id
}

// pick is the byte-Source equivalent of schemagen's (unexported) draw helper.
func pick[T any](s schemagen.Source, opts []T, label string) T {
	if len(opts) == 0 {
		var zero T
		return zero
	}
	return opts[s.Intn(len(opts), label)]
}

func (g *transcriptGen) mode(label string) schemagen.Mode {
	if g.s.Bool(label) {
		return schemagen.Adjacent
	}
	return schemagen.Valid
}

// transcriptTimestamps are all valid RFC3339 (so time.Time unmarshal never
// fails), spanning the zero value and the far past/future.
var transcriptTimestamps = []string{
	"2026-06-01T10:00:00Z",
	"2026-06-01T10:00:01.5Z",
	"0001-01-01T00:00:00Z",
	"1970-01-01T00:00:00Z",
	"2999-12-31T23:59:59Z",
	"2026-06-01T10:00:00+09:30",
}

func (g *transcriptGen) timestamp(label string) string {
	return pick(g.s, transcriptTimestamps, label)
}

var (
	transcriptProfiles  = []string{"openai", "anthropic", "google", "kimi", "openrouter", ""}
	transcriptModels    = []string{"gpt-5.5", "claude-opus", "gemini-2.5", "kimi-k2", ""}
	transcriptToolNames = []string{"shell", "read_file", "edit_file", "glob", "grep", "task", ""}
	transcriptSessions  = []string{"s1", "s2", "", "abc-123", "Σ-session", "sessnul"}
	transcriptRoles     = []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleSystem, llm.RoleDeveloper, llm.Role("weird")}
	transcriptText      = []string{"", " ", "hello", "line1\nline2", "\t \ufeff", "\U0001f4a5", "a\x00b", "{{.}}"}
	taskTypes           = []string{"research", "implement", "verify", "fix", ""}
	taskStatuses        = []string{"pending", "in_progress", "done", "blocked", ""}
)

var transcriptTurnKinds = []schema.TurnKind{
	schema.TurnUserInput, schema.TurnSteering, schema.TurnAssistant,
	schema.TurnTool, schema.TurnToolResults, schema.TurnSystem,
	schema.TurnCheckpoint, schema.TurnSummary,
}

func (g *transcriptGen) header() map[string]any {
	s := g.s
	h := map[string]any{
		"kind":           "header",
		"format_version": pick(s, []int{1, 0, 2}, "format_version"),
		"session_id":     pick(s, transcriptSessions, "session_id"),
		"created_at":     g.timestamp("created_at"),
		"profile_id":     pick(s, transcriptProfiles, "profile_id"),
		"model":          pick(s, transcriptModels, "model"),
	}
	if s.Bool("hdr_parent") {
		h["parent_session_id"] = pick(s, transcriptSessions, "parent_session_id")
		h["parent_tool_call_id"] = g.newCallID()
	}
	if s.Bool("hdr_task") {
		h["task"] = s.String("hdr_task_text")
	}
	if s.Bool("hdr_working_dir") {
		h["working_dir"] = s.String("hdr_wd")
	}
	if s.Bool("hdr_depth") {
		h["depth"] = s.IntRange(0, 5, "hdr_depth_v")
	}
	if s.Bool("hdr_build") {
		h["build_version"] = s.String("hdr_build_v")
	}
	if s.Bool("hdr_system_prompt") {
		h["system_prompt"] = s.String("hdr_sysprompt")
	}
	if s.Bool("hdr_agent_tasks") {
		h["agent_tasks"] = g.agentTasks()
	}
	return h
}

func (g *transcriptGen) agentTasks() []any {
	s := g.s
	n := s.IntRange(0, 2, "n_tasks")
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{
			"id":          s.IntRange(1, 100, "task_id"),
			"type":        pick(s, taskTypes, "task_type"),
			"description": s.String("task_desc"),
			"status":      pick(s, taskStatuses, "task_status"),
		})
	}
	return out
}

func (g *transcriptGen) entry() map[string]any {
	kind := pick(g.s, transcriptTurnKinds, "turn_kind")
	return map[string]any{
		"kind": "entry",
		"seq":  g.nextSeq(),
		"turn": g.turn(kind),
	}
}

func (g *transcriptGen) turn(kind schema.TurnKind) map[string]any {
	s := g.s
	t := map[string]any{
		"kind":      string(kind),
		"message":   g.message(kind),
		"timestamp": g.timestamp("turn_ts"),
	}
	if kind == schema.TurnAssistant {
		if s.Bool("turn_usage") {
			t["usage"] = g.usage()
		}
		if s.Bool("turn_response_meta") {
			t["response_id"] = s.String("resp_id")
			t["response_provider"] = pick(s, transcriptProfiles, "resp_provider")
			t["response_model"] = pick(s, transcriptModels, "resp_model")
		}
	}
	return t
}

func roleFor(k schema.TurnKind) llm.Role {
	switch k {
	case schema.TurnUserInput, schema.TurnSteering:
		return llm.RoleUser
	case schema.TurnAssistant, schema.TurnSummary, schema.TurnCheckpoint:
		return llm.RoleAssistant
	case schema.TurnTool, schema.TurnToolResults:
		return llm.RoleTool
	case schema.TurnSystem:
		return llm.RoleSystem
	default:
		return llm.RoleUser
	}
}

func (g *transcriptGen) message(kind schema.TurnKind) map[string]any {
	s := g.s
	role := roleFor(kind)
	if s.Bool("role_override") {
		role = pick(s, transcriptRoles, "role")
	}

	var parts []any
	switch kind {
	case schema.TurnAssistant:
		n := s.IntRange(0, 4, "asst_parts")
		for i := 0; i < n; i++ {
			switch s.Intn(3, "asst_part_kind") {
			case 0:
				parts = append(parts, g.textPart())
			case 1:
				parts = append(parts, g.thinkingPart())
			default:
				parts = append(parts, g.toolCallPart())
			}
		}
	case schema.TurnTool, schema.TurnToolResults:
		parts = g.toolResultParts()
	default:
		n := s.IntRange(0, 3, "msg_parts")
		for i := 0; i < n; i++ {
			parts = append(parts, g.contentPart())
		}
	}

	msg := map[string]any{
		"role":    string(role),
		"content": parts,
	}
	if s.Bool("msg_name") {
		msg["name"] = s.String("msg_name_v")
	}
	if s.Bool("msg_tool_call_id") {
		msg["tool_call_id"] = g.newCallID()
	}
	return msg
}

// contentPart yields one of the non-tool-call content kinds (tool-call pairing is
// driven separately on assistant turns).
func (g *transcriptGen) contentPart() any {
	switch g.s.Intn(6, "content_kind") {
	case 0:
		return g.textPart()
	case 1:
		return g.thinkingPart()
	case 2:
		return g.imagePart()
	case 3:
		return g.audioPart()
	case 4:
		return g.documentPart()
	default:
		return g.webSearchPart()
	}
}

func (g *transcriptGen) textPart() any {
	s := g.s
	p := map[string]any{
		"kind": "text",
		"text": pick(s, transcriptText, "text"),
	}
	if s.Bool("text_phase") {
		p["phase"] = pick(s, []string{"commentary", "final_answer", ""}, "phase")
	}
	return p
}

func (g *transcriptGen) thinkingPart() any {
	s := g.s
	th := map[string]any{"text": pick(s, transcriptText, "think_text")}
	if s.Bool("th_id") {
		th["id"] = s.String("th_id_v")
	}
	if s.Bool("th_signature") {
		th["signature"] = s.String("th_sig")
	}
	if s.Bool("th_redacted") {
		th["redacted"] = true
	}
	if s.Bool("th_encrypted") {
		th["encrypted_content"] = s.String("th_enc")
	}
	if s.Bool("th_summary") {
		th["summary"] = g.stringList("th_sum")
	}
	kind := "thinking"
	if s.Bool("th_is_redacted_kind") {
		kind = "redacted_thinking"
	}
	return map[string]any{"kind": kind, "thinking": th}
}

func (g *transcriptGen) toolCallPart() any {
	s := g.s
	tc := map[string]any{"name": pick(s, transcriptToolNames, "tc_name")}
	if s.Bool("tc_has_id") {
		id := g.newCallID()
		g.pending = append(g.pending, id)
		tc["id"] = id
	} else {
		tc["id"] = ""
	}
	if s.Bool("tc_args") {
		tc["arguments"] = schemagen.Value(s, nil, g.mode("tc_args_mode"))
	}
	if s.Bool("tc_type") {
		tc["type"] = "function"
	}
	if s.Bool("tc_item_id") {
		tc["item_id"] = s.String("tc_item")
	}
	if s.Bool("tc_signature") {
		tc["thought_signature"] = s.String("tc_sig")
	}
	return map[string]any{"kind": "tool_call", "tool_call": tc}
}

func (g *transcriptGen) toolResultParts() []any {
	s := g.s
	var parts []any
	// Resolve a deterministic prefix of the outstanding calls; the rest stay
	// pending so the next assistant flush sees orphaned CALLS (synthetic-result
	// repair territory).
	if len(g.pending) > 0 && s.Bool("resolve_pending") {
		k := s.IntRange(0, len(g.pending), "resolve_n")
		for i := 0; i < k; i++ {
			parts = append(parts, g.toolResultPart(g.pending[i]))
		}
		g.pending = g.pending[k:]
	}
	// Emit results for IDs that were never called (orphaned RESULTS).
	if s.Bool("orphan_results") {
		m := s.IntRange(0, 2, "orphan_n")
		for i := 0; i < m; i++ {
			parts = append(parts, g.toolResultPart("orphan_"+strconv.Itoa(g.orphanSeq)))
			g.orphanSeq++
		}
	}
	return parts
}

func (g *transcriptGen) toolResultPart(id string) any {
	s := g.s
	tr := map[string]any{
		"tool_call_id": id,
		"content":      schemagen.Value(s, nil, g.mode("tr_content_mode")),
		"is_error":     s.Bool("tr_is_error"),
	}
	if s.Bool("tr_name") {
		tr["name"] = pick(s, transcriptToolNames, "tr_name_v")
	}
	if s.Bool("tr_duration") {
		tr["duration_ms"] = s.IntRange(0, 100000, "tr_dur")
	}
	if s.Bool("tr_state") {
		// tool_state is json.RawMessage; any well-typed value round-trips.
		tr["tool_state"] = schemagen.Value(s, nil, g.mode("tr_state_mode"))
	}
	if s.Bool("tr_image") {
		tr["image_data"] = g.bytesField("tr_img")
		tr["image_media_type"] = pick(s, []string{"image/png", "image/jpeg", ""}, "tr_img_mt")
	}
	return map[string]any{"kind": "tool_result", "tool_result": tr}
}

func (g *transcriptGen) imagePart() any {
	s := g.s
	img := map[string]any{}
	if s.Bool("img_url") {
		img["url"] = s.String("img_url_v")
	}
	if s.Bool("img_data") {
		img["data"] = g.bytesField("img_data")
	}
	if s.Bool("img_media") {
		img["media_type"] = pick(s, []string{"image/png", "image/jpeg", "image/webp", ""}, "img_mt")
	}
	if s.Bool("img_detail") {
		img["detail"] = pick(s, []string{"auto", "low", "high", ""}, "img_detail_v")
	}
	return map[string]any{"kind": "image", "image": img}
}

func (g *transcriptGen) audioPart() any {
	s := g.s
	a := map[string]any{}
	if s.Bool("aud_url") {
		a["url"] = s.String("aud_url_v")
	}
	if s.Bool("aud_data") {
		a["data"] = g.bytesField("aud_data")
	}
	if s.Bool("aud_media") {
		a["media_type"] = pick(s, []string{"audio/mpeg", "audio/wav", ""}, "aud_mt")
	}
	return map[string]any{"kind": "audio", "audio": a}
}

func (g *transcriptGen) documentPart() any {
	s := g.s
	d := map[string]any{}
	if s.Bool("doc_url") {
		d["url"] = s.String("doc_url_v")
	}
	if s.Bool("doc_data") {
		d["data"] = g.bytesField("doc_data")
	}
	if s.Bool("doc_media") {
		d["media_type"] = pick(s, []string{"application/pdf", "text/plain", ""}, "doc_mt")
	}
	if s.Bool("doc_name") {
		d["file_name"] = s.String("doc_name_v")
	}
	return map[string]any{"kind": "document", "document": d}
}

func (g *transcriptGen) webSearchPart() any {
	s := g.s
	ws := map[string]any{
		// Raw is json.RawMessage with no omitempty; always supply a well-typed body.
		"raw": schemagen.Value(s, nil, g.mode("ws_raw_mode")),
	}
	if s.Bool("ws_query") {
		ws["query"] = s.String("ws_query_v")
	}
	return map[string]any{"kind": "web_search", "web_search": ws}
}

func (g *transcriptGen) usage() map[string]any {
	s := g.s
	u := map[string]any{
		"input_tokens":  s.IntRange(0, 1000000, "in_tok"),
		"output_tokens": s.IntRange(0, 1000000, "out_tok"),
		"total_tokens":  s.IntRange(0, 2000000, "tot_tok"),
	}
	if s.Bool("u_reasoning") {
		u["reasoning_tokens"] = s.IntRange(0, 100000, "u_r")
	}
	if s.Bool("u_cache_read") {
		u["cache_read_tokens"] = s.IntRange(0, 100000, "u_cr")
	}
	if s.Bool("u_cache_write") {
		u["cache_write_tokens"] = s.IntRange(0, 100000, "u_cw")
	}
	if s.Bool("u_raw") {
		// Usage.Raw is map[string]any; an object value keeps the line decodable.
		u["raw"] = map[string]any{"k": schemagen.Value(s, nil, g.mode("u_raw_mode"))}
	}
	return u
}

func (g *transcriptGen) apiCall() map[string]any {
	s := g.s
	req := map[string]any{
		"model":         pick(s, transcriptModels, "ac_req_model"),
		"provider":      pick(s, transcriptProfiles, "ac_req_provider"),
		"message_count": s.IntRange(0, 500, "ac_msg_count"),
		"tool_count":    s.IntRange(0, 50, "ac_tool_count"),
	}
	if s.Bool("ac_tool_names") {
		req["tool_names"] = g.toolNameList()
	}
	if s.Bool("ac_effort") {
		req["reasoning_effort"] = pick(s, []string{"low", "medium", "high", "none", ""}, "ac_effort_v")
	}

	call := map[string]any{
		"kind":          "api_call",
		"seq":           g.nextSeq(),
		"round":         s.IntRange(0, 20, "ac_round"),
		"ts":            g.timestamp("ac_ts"),
		"latency_ms":    s.IntRange(0, 600000, "ac_latency"),
		"system_prompt": s.String("ac_sysprompt"),
		"request":       req,
	}
	if s.Bool("ac_with_response") {
		resp := map[string]any{
			"model":           pick(s, transcriptModels, "ac_resp_model"),
			"finish_reason":   pick(s, []string{"stop", "length", "tool_calls", "error", ""}, "ac_finish"),
			"text_length":     s.IntRange(0, 100000, "ac_textlen"),
			"tool_call_count": s.IntRange(0, 50, "ac_tcc"),
			"usage":           g.usage(),
			// APILogResponse.Raw is map[string]any; supply an object.
			"raw": map[string]any{"k": schemagen.Value(s, nil, g.mode("ac_raw_mode"))},
		}
		call["response"] = resp
	} else if s.Bool("ac_with_error") {
		call["error"] = s.String("ac_error")
		call["source"] = s.String("ac_source")
		call["title"] = s.String("ac_title")
		call["hint"] = s.String("ac_hint")
	}
	return call
}

func (g *transcriptGen) toolNameList() []any {
	n := g.s.IntRange(0, 4, "n_tool_names")
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pick(g.s, transcriptToolNames, "tn"))
	}
	return out
}

func (g *transcriptGen) stringList(label string) []any {
	n := g.s.IntRange(0, 3, label+"_n")
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pick(g.s, transcriptText, label+"_v"))
	}
	return out
}

// bytesField returns raw bytes; json.Marshal base64-encodes them, so a []byte
// struct field (ImageData.Data, etc.) decodes cleanly and round-trips.
func (g *transcriptGen) bytesField(label string) []byte {
	n := g.s.IntRange(0, 8, label+"_n")
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.s.Intn(256, label+"_b"))
	}
	return b
}

// transcriptStructuredSeeds steer the generator across its branches from the
// first bytes; the empty seed exercises the exhaustion-default path (header
// only).
var transcriptStructuredSeeds = [][]byte{
	{},
	{0x00},
	{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	bytes.Repeat([]byte{0xff}, 32),
	[]byte("structured-but-adversarial-transcript-seed"),
	{0x02, 0x02, 0x01, 0x05, 0x02, 0x07, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
}

// FuzzTranscriptReplayStructured is roadmap lane 8.4: a structure-aware sibling of
// FuzzTranscriptReplay. It consumes fuzz bytes through generateTranscript to
// synthesize a valid-but-adversarial transcript, then drives it through the REAL
// readTranscriptFull / write-read / ResumeHistory / strict-child-reader path and
// asserts the IDENTICAL oracles as the raw-byte target: never panic, write→read
// round-trip fixed point, ResumeHistory idempotence, and api_call round-trip.
// Because the transcripts are structurally valid, this target reaches the entry
// decoders, orphan-repair, and compaction-scan logic that random bytes almost
// never construct — see TestStructuredTranscriptReachesDeeper for the gap. A
// failure here on a valid transcript is a real bug, not a test artifact.
func FuzzTranscriptReplayStructured(f *testing.F) {
	for _, seed := range transcriptStructuredSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := generateTranscript(schemagen.NewByteSource(data))
		if err != nil {
			// A transcript that won't even marshal is a generator defect.
			t.Fatalf("generateTranscript: %v", err)
		}

		dir := t.TempDir()
		inPath := filepath.Join(dir, "in.jsonl")
		if err := os.WriteFile(inPath, raw, 0o644); err != nil {
			t.Fatalf("write input transcript: %v", err)
		}
		d, err := readTranscriptFull(inPath)
		if err != nil {
			return // no header / unreadable: no-panic floor proven, stop
		}

		assertTranscriptWriteReadRoundTrip(t, dir, d)
		assertResumeHistoryIdempotent(t, d.Entries)
		assertAPICallRoundTrip(t, dir, d)

		sid := d.Header.SessionID
		_, _ = readStrictChildTranscript(inPath, sid, transcriptJSONLMaxLineBytes)
		_, _ = validateStrictChildTranscript(inPath, sid, transcriptJSONLMaxLineBytes)
		_, _ = readStrictChildTranscript(inPath, sid+"_mismatch", transcriptJSONLMaxLineBytes)
		_, _ = readStrictChildTranscript(inPath, sid, 4)
	})
}

// TestStructuredTranscriptReachesDeeper is the evidence (and determinism guard)
// for lane 8.4. Over a fixed-seed Monte Carlo it measures, for each target, the
// fraction of inputs whose first line decodes as a transcript header (so
// readTranscriptFull returns nil and the oracles run at all) and the fraction
// that yield at least one decoded entry (so the write/read + resume oracles run
// over real turns). Raw random bytes essentially never form a valid header line;
// the structured generator does so on nearly every input.
func TestStructuredTranscriptReachesDeeper(t *testing.T) {
	const samples = 3000
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.jsonl")

	write := func(b []byte) transcriptData {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatalf("write probe: %v", err)
		}
		d, err := readTranscriptFull(path)
		if err != nil {
			return transcriptData{Skipped: -1} // sentinel: header did not decode
		}
		return d
	}

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	var rawHeader, rawEntries, structHeader, structEntries int
	for i := 0; i < samples; i++ {
		data := make([]byte, rng.Intn(64))
		_, _ = rng.Read(data)

		if d := write(data); d.Skipped != -1 {
			rawHeader++
			if len(d.Entries) > 0 {
				rawEntries++
			}
		}

		gen, err := generateTranscript(schemagen.NewByteSource(data))
		if err != nil {
			t.Fatalf("generateTranscript(%x): %v", data, err)
		}
		// Determinism: identical bytes must yield identical transcripts.
		gen2, err := generateTranscript(schemagen.NewByteSource(data))
		if err != nil {
			t.Fatalf("generateTranscript(%x) second call: %v", data, err)
		}
		if !bytes.Equal(gen, gen2) {
			t.Fatalf("generateTranscript not deterministic for %x:\n once=%s\n twice=%s", data, gen, gen2)
		}

		if d := write(gen); d.Skipped != -1 {
			structHeader++
			if len(d.Entries) > 0 {
				structEntries++
			}
		}
	}

	rawHeaderRate := float64(rawHeader) / samples
	structHeaderRate := float64(structHeader) / samples
	structEntryRate := float64(structEntries) / samples
	t.Logf("header decode: raw=%.1f%% (%d)  structured=%.1f%% (%d)",
		rawHeaderRate*100, rawHeader, structHeaderRate*100, structHeader)
	t.Logf("entry yield (>=1 decoded entry): raw=%.1f%% (%d)  structured=%.1f%% (%d)",
		float64(rawEntries)/samples*100, rawEntries, structEntryRate*100, structEntries)

	if rawHeaderRate > 0.05 {
		t.Errorf("raw random bytes should rarely form a transcript header, got %.1f%%", rawHeaderRate*100)
	}
	if structHeaderRate < 0.99 {
		t.Errorf("structured generator should produce a decodable header almost always, got %.1f%%", structHeaderRate*100)
	}
	if structEntryRate < 0.5 {
		t.Errorf("structured generator should yield decoded entries most of the time, got %.1f%%", structEntryRate*100)
	}
	if structHeaderRate <= rawHeaderRate {
		t.Errorf("structured (%.1f%%) must reach the reader more than raw bytes (%.1f%%)",
			structHeaderRate*100, rawHeaderRate*100)
	}
}