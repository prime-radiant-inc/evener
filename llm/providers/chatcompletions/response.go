package chatcompletions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
)

type chatCompletionResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []chatChoice   `json:"choices"`
	Usage   map[string]any `json:"usage"`
}

type chatChoice struct {
	Index        int            `json:"index"`
	Message      chatMessage    `json:"message"`
	FinishReason string         `json:"finish_reason"`
	Usage        map[string]any `json:"usage,omitempty"`
}

type chatMessage struct {
	chatReasoningFields
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type chatReasoningFields struct {
	ReasoningContent *string               `json:"reasoning_content,omitempty"`
	Reasoning        *string               `json:"reasoning,omitempty"`
	ReasoningText    *string               `json:"reasoning_text,omitempty"`
	ReasoningDetails []reasoningDetailItem `json:"reasoning_details,omitempty"`
}

// chatExtraContent carries provider-specific continuation metadata that rides
// beside an OpenAI-compatible tool call. Gemini uses this escape hatch for the
// opaque thought signature required on the next function-call turn.
type chatExtraContent struct {
	Google *chatGoogleExtraContent `json:"google,omitempty"`
}

type chatGoogleExtraContent struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// reasoningDetailItem represents an element in the reasoning_details array
// used by OpenRouter for models like MiniMax M2.7. MiniMax's actual format
// is {type: "reasoning.text", text: "...", format: "...", index: N}.
// Raw keeps the item's verbatim JSON, including provider metadata beyond the
// fields decoded here.
type reasoningDetailItem struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Format string `json:"format,omitempty"`
	Index  *int   `json:"index,omitempty"`
	// Signature carries the provider's continuation signature on
	// reasoning.text items (OpenRouter's Anthropic route: streamed as a
	// trailing text-less item, non-stream merged with the text). It must
	// round-trip or Anthropic-routed reasoning continuations break.
	Signature string `json:"signature,omitempty"`
	// Thinking is kept for backward compatibility with older OpenRouter format
	// variants that used {type: "thinking", thinking: "..."}.
	Thinking string `json:"thinking,omitempty"`
	// Raw is the verbatim wire JSON of this item, captured by UnmarshalJSON.
	Raw json.RawMessage `json:"-"`
	// ID and Data carry the opaque {type: "reasoning.encrypted", id, data}
	// items OpenRouter emits for Gemini/o-series: the reasoning chain is
	// encrypted server-side and must be round-tripped verbatim.
	ID   string `json:"id,omitempty"`
	Data string `json:"data,omitempty"`
}

// UnmarshalJSON decodes the known fields and keeps the item's verbatim JSON
// in Raw for lossless replay of native items.
func (d *reasoningDetailItem) UnmarshalJSON(b []byte) error {
	type plain reasoningDetailItem
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*d = reasoningDetailItem(p)
	d.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// isEncrypted reports whether the item is an opaque encrypted reasoning block
// that must be replayed verbatim to preserve the reasoning chain.
func (d reasoningDetailItem) isEncrypted() bool {
	return d.Type == "reasoning.encrypted" && d.ID != "" && d.Data != ""
}

func encodeEncryptedDetails(items []map[string]any) string {
	if len(items) == 0 {
		return ""
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// wireItem preserves provider-specific continuation metadata, including
// fields not represented by reasoningDetailItem.
func (d reasoningDetailItem) wireItem() map[string]any {
	if wire := decodeDetailObject(d.Raw); wire != nil {
		return wire
	}
	b, _ := json.Marshal(d)
	return decodeDetailObject(b)
}

func decodeDetailObject(b []byte) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var wire map[string]any
	_ = decoder.Decode(&wire)
	return wire
}

type chatToolCall struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Function     chatFunctionCall  `json:"function"`
	ExtraContent *chatExtraContent `json:"extra_content,omitempty"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatFunctionCallRaw is the focused decode target for byte-faithful
// argument capture: Arguments is captured as a json.RawMessage (the string
// token) rather than a string, so rawStringContent can unescape it
// preserving bytes that encoding/json would coerce to U+FFFD.
type chatFunctionCallRaw struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type chatToolCallRaw struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function chatFunctionCallRaw `json:"function"`
}
type chatMessageRaw struct {
	ToolCalls []chatToolCallRaw `json:"tool_calls,omitempty"`
}
type chatChoiceRaw struct {
	Message chatMessageRaw `json:"message"`
}
type chatCompletionResponseRaw struct {
	Choices []chatChoiceRaw `json:"choices"`
}

type chatCompletionChunk struct {
	ID      string                  `json:"id"`
	Model   string                  `json:"model"`
	Choices []chatChunkChoice       `json:"choices"`
	Usage   map[string]any          `json:"usage"`
	Error   *openaichat.InbandError `json:"error"`
}

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        chatDelta      `json:"delta"`
	FinishReason string         `json:"finish_reason"`
	Usage        map[string]any `json:"usage,omitempty"`
}

type chatDelta struct {
	chatReasoningFields
	Role      string              `json:"role"`
	Content   string              `json:"content"`
	ToolCalls []chatChunkToolCall `json:"tool_calls,omitempty"`
}

// reasoningFieldNames are the wire fields OpenAI-compatible providers use for
// reasoning deltas, in llm's canonical parse order. Providers pick one
// (llama.cpp: reasoning_content; OpenRouter/chutes: reasoning; some gateways:
// reasoning_text); chutes.ai duplicates identical content across two, so
// exactly one is read per chunk.
var reasoningFieldNames = llm.OpenAICompatReasoningFields()

type chatChunkToolCall struct {
	Index        int               `json:"index"`
	ID           string            `json:"id,omitempty"`
	Type         string            `json:"type,omitempty"`
	Function     chatChunkFunction `json:"function"`
	ExtraContent *chatExtraContent `json:"extra_content,omitempty"`
}

// chatChunkFunction is a streamed function call. Its arguments stay in their
// wire form so the assembler unescapes each delta once, byte-faithfully:
// the same chunk bytes are never decoded a second time to recover them.
type chatChunkFunction struct {
	Name      string         `json:"name"`
	Arguments chunkArgsToken `json:"arguments"`
}

// chunkArgsToken is a streamed function.arguments value in its JSON wire form.
// encoding/json hands an Unmarshaler the exact token, so keeping those bytes
// costs one copy and no unescape. It accepts only a JSON string (or null), the
// same tokens a string field accepts, so a malformed chunk still fails its own
// decode and degrades to the raw passthrough event.
type chunkArgsToken struct {
	token json.RawMessage
}

func (a *chunkArgsToken) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		// null is what a string field does nothing for, so a duplicate key such
		// as "arguments":"{}","arguments":null keeps the arguments already
		// decoded rather than dropping them.
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		a.token = append(a.token[:0], b...)
		return nil
	}
	return errors.New("chatcompletions: function.arguments is not a JSON string")
}

// carriesFragment reports whether a delta carries argument bytes: a JSON
// string token of at least one content byte. An absent or "" token carries
// none, which is the case the string form skipped.
func (a chunkArgsToken) carriesFragment() bool {
	return len(a.token) > 2 && a.token[0] == '"'
}

// stringForm is the arguments as a Go string: the value a string-typed field
// would have decoded. The assembler writes it only when the raw token cannot
// be unescaped byte-faithfully.
func (a chunkArgsToken) stringForm() string {
	var s string
	_ = json.Unmarshal(a.token, &s)
	return s
}

func thoughtSignatureFromExtraContent(extra *chatExtraContent) string {
	if extra == nil || extra.Google == nil {
		return ""
	}
	return strings.TrimSpace(extra.Google.ThoughtSignature)
}

func (f chatReasoningFields) scalar() (string, string) {
	var blankField string
	for i, v := range [...]*string{f.ReasoningContent, f.Reasoning, f.ReasoningText} {
		if v == nil {
			continue
		}
		if *v != "" {
			return *v, reasoningFieldNames[i]
		}
		if blankField == "" {
			blankField = reasoningFieldNames[i]
		}
	}
	return "", blankField
}

type reasoningDetailBlock struct {
	text         strings.Builder
	textField    string
	wire         map[string]any
	nonText      json.RawMessage
	continuation bool
	position     int
	index        *int
	batch        int
	fragments    []reasoningDetailFragment
}

// While an ID or index is missing, another open block may hold fragments of
// this same item. Their arrival order determines the signed bytes and metadata.
type reasoningDetailFragment struct {
	batch           int
	wire            map[string]any
	text, textField string
}

func (b *reasoningDetailBlock) appendFragment(f reasoningDetailFragment) {
	for key, value := range f.wire {
		switch key {
		case "type", "id", "index", "format":
			if known := b.wire[key]; known != nil && known != "" {
				continue
			}
		}
		b.wire[key] = value
	}
	if b.textField == "" {
		b.textField = f.textField
	}
	b.text.WriteString(f.text)
	b.batch = f.batch
}

// chatReasoning assembles one completion. A trailing signature authenticates
// detail text from its own index. A scalar is an independent source unless it
// mirrors the text on the details.
type chatReasoning struct {
	scalar      strings.Builder
	scalarField string
	// Unsigned details take precedence over a scalar in the same chunk.
	// Until the first text-bearing detail, that display is exactly the scalar.
	display      *strings.Builder
	displayField string
	open         []*reasoningDetailBlock
	blocks       []*reasoningDetailBlock // native detail order, reserved when a block first appears
	hasSignature bool
	batch        int
}

func (r *chatReasoning) add(f chatReasoningFields, settled bool) string {
	scalar, field := f.scalar()
	var detailDelta strings.Builder
	r.batch++
	for pos, item := range f.ReasoningDetails {
		piece := item.Text
		if piece == "" {
			piece = item.Thinking
		}
		if piece != "" {
			if detailDelta.Len() != 0 {
				detailDelta.WriteByte('\n')
			}
			detailDelta.WriteString(piece)
		}
		r.addDetail(pos, item, piece, len(f.ReasoningDetails) == 1, settled)
	}
	delta := scalar
	if detailDelta.Len() != 0 {
		delta = detailDelta.String()
		if r.display == nil {
			r.display = &strings.Builder{}
			r.display.WriteString(r.scalar.String())
			if r.scalar.Len() > 0 {
				r.displayField = r.scalarField
			}
		}
	}
	if r.display != nil {
		r.display.WriteString(delta)
	}
	if scalar != "" {
		if r.scalar.Len() == 0 {
			r.scalarField = field
		}
		r.scalar.WriteString(scalar)
	} else if r.scalarField == "" {
		r.scalarField = field
	}
	return delta
}

// An explicit index or array position identifies a detail in a batch. A
// singleton fragment can omit its index while finishing an earlier block.
// Only a unique compatible block can supply the text a signature authenticates.
func (r *chatReasoning) openDetail(pos int, item reasoningDetailItem, single bool) (int, *reasoningDetailBlock) {
	const (
		byPosition = iota + 1
		byID
		byIndex
	)
	match, best := -1, 0
	idMatch, idCount := -1, 0
	for i, block := range r.open {
		// An array's siblings are separate items. Later deltas can extend
		// each one by its wire index, ID, or position in a subsequent array.
		if block.batch == r.batch {
			continue
		}
		id, _ := block.wire["id"].(string)
		if id != "" && item.ID != "" && id != item.ID {
			continue
		}
		if format, _ := block.wire["format"].(string); format != "" && item.Format != "" && format != item.Format {
			continue
		}
		if kind, _ := block.wire["type"].(string); kind != "" && item.Type != "" && kind != item.Type {
			continue
		}
		rank := byPosition
		switch {
		case item.Index != nil && block.index != nil:
			if *item.Index != *block.index {
				continue
			}
			rank = byIndex
		case id != "" && id == item.ID:
			rank = byID
			idMatch, idCount = i, idCount+1
		case item.Index != nil:
			if *item.Index != block.position {
				continue
			}
		case !single && pos != block.position:
			continue
		}
		if rank > best {
			match, best = i, rank
		} else if rank == best {
			match = -1
		}
	}
	if match >= 0 && best == byIndex && idCount == 1 {
		if id, _ := r.open[match].wire["id"].(string); id == "" {
			match = r.joinDetail(match, idMatch)
		}
	}
	if match < 0 {
		return -1, nil
	}
	return match, r.open[match]
}

// A fragment naming both handles can bind previously separate ID-only and
// index-only histories. Distinct siblings in one array cannot be the same item.
func (r *chatReasoning) joinDetail(byIndex, byID int) int {
	indexed, named := r.open[byIndex], r.open[byID]
	for _, key := range []string{"type", "format"} {
		a, _ := indexed.wire[key].(string)
		b, _ := named.wire[key].(string)
		if a != "" && b != "" && a != b {
			return -1
		}
	}
	fragments := slices.Concat(indexed.fragments, named.fragments)
	slices.SortFunc(fragments, func(a, b reasoningDetailFragment) int { return cmp.Compare(a.batch, b.batch) })
	for i := 1; i < len(fragments); i++ {
		if fragments[i-1].batch == fragments[i].batch {
			return -1
		}
	}
	first, last := min(byIndex, byID), max(byIndex, byID)
	keep, drop := r.open[first], r.open[last]
	*keep = reasoningDetailBlock{wire: make(map[string]any), position: keep.position, index: indexed.index}
	for _, f := range fragments {
		keep.appendFragment(f)
	}
	r.open = slices.Delete(r.open, last, last+1)
	slot := slices.Index(r.blocks, drop)
	r.blocks = slices.Delete(r.blocks, slot, slot+1)
	return first
}

func (r *chatReasoning) addDetail(pos int, item reasoningDetailItem, piece string, single, settled bool) {
	if item.isEncrypted() {
		r.blocks = append(r.blocks, &reasoningDetailBlock{wire: item.wireItem(), continuation: true})
		return
	}
	if settled && piece == "" {
		switch item.Type {
		case "", "reasoning.text", "thinking", "reasoning.encrypted":
		default:
			// A complete non-text detail has its own slot in native replay
			// and supplies no display text or scalar fallback.
			r.blocks = append(r.blocks, &reasoningDetailBlock{nonText: item.Raw})
			return
		}
	}
	index, block := r.openDetail(pos, item, single)
	if block != nil && item.Type == "" {
		item.Type, _ = block.wire["type"].(string)
	}
	signed := item.Type == "reasoning.text" && item.Signature != ""
	wire := item.wireItem()
	// An explicit empty string in a complete message or final delta is a
	// native slot. The same string on an unfinished opening carries no text.
	settledEmpty := settled && piece == "" && item.Type == "reasoning.text" && wire["text"] == ""
	// An identified opening can carry the only ID or format for its later text
	// or signature.
	opening := (item.Index != nil || item.ID != "") && (item.Type == "" || item.Type == "reasoning.text")
	if piece == "" && !signed && !opening && !settledEmpty {
		return
	}
	if block == nil {
		block = &reasoningDetailBlock{wire: make(map[string]any, len(wire)), position: pos}
		r.blocks = append(r.blocks, block)
		if !signed {
			r.open = append(r.open, block)
		}
	}
	if item.Index != nil {
		block.index = item.Index
	}
	f := reasoningDetailFragment{batch: r.batch, wire: wire, text: piece}
	switch {
	case item.Text != "" || settledEmpty:
		f.textField = "text"
	case item.Thinking != "":
		f.textField = "thinking"
	}
	block.appendFragment(f)
	if id, _ := block.wire["id"].(string); !signed && (block.index == nil || id == "") {
		block.fragments = append(block.fragments, f)
	} else {
		block.fragments = nil
	}
	if signed {
		block.continuation = true
		if block.text.Len() > 0 {
			block.wire["text"] = block.text.String()
		}
		r.hasSignature = true
		if index >= 0 {
			r.open = slices.Delete(r.open, index, index+1)
		}
	}
}

// The native array and the displayed text have different boundaries. Keep
// the array's order without duplicating its separately scoped continuations.
func captureChatDetails(t *llm.ThinkingData, blocks []*reasoningDetailBlock) {
	var opaque []map[string]any
	for _, block := range blocks {
		var item json.RawMessage
		switch {
		case block.continuation:
			opaque = append(opaque, block.wire)
		case len(block.nonText) != 0:
			item = block.nonText
		case block.textField != "":
			block.wire[block.textField] = block.text.String()
			item, _ = json.Marshal(block.wire)
		default:
			continue
		}
		t.ChatDetails = append(t.ChatDetails, item)
	}
	t.EncryptedContent = encodeEncryptedDetails(opaque)
}

func (r *chatReasoning) parts() []llm.ContentPart {
	parts := []llm.ContentPart{}
	appendPart := func(thinking llm.ThinkingData) {
		if thinking.Text != "" || thinking.EncryptedContent != "" || len(thinking.ChatDetails) != 0 || (thinking.ChatScalar != nil && thinking.ChatScalar.Empty) {
			parts = append(parts, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &thinking})
		}
	}
	scalar := r.scalar.String()
	thinking := llm.ThinkingData{Text: scalar, Signature: r.scalarField}
	if scalar == "" && r.scalarField != "" {
		thinking = llm.ThinkingData{ChatScalar: &llm.ChatReasoningScalar{Field: r.scalarField, Empty: true}}
	}
	if !r.hasSignature {
		if r.display != nil {
			thinking.Text, thinking.Signature = r.display.String(), r.displayField
			if scalar != "" {
				setChatReplayScalar(&thinking, r.scalarField, scalar)
			}
		}
		captureChatDetails(&thinking, r.blocks)
		appendPart(thinking)
		return parts
	}
	mirror := r.scalarMirror(scalar)
	if r.scalarField != "" && mirror == nil {
		appendPart(thinking)
	}
	for i, block := range r.blocks {
		thinking := llm.ThinkingData{Text: block.text.String()}
		captureChatDetails(&thinking, r.blocks[i:i+1])
		if block == mirror {
			setChatReplayScalar(&thinking, r.scalarField, scalar)
		} else if !block.continuation && r.scalarField != "" {
			thinking.ChatScalar = &llm.ChatReasoningScalar{}
		}
		appendPart(thinking)
	}
	return parts
}

func setChatReplayScalar(thinking *llm.ThinkingData, field, scalar string) {
	if field == thinking.Signature && scalar == thinking.Text {
		return
	}
	thinking.ChatScalar = &llm.ChatReasoningScalar{Field: field}
	if scalar != thinking.Text {
		thinking.ChatScalar.Text = scalar
	}
}

// A single detail owns the observed scalar when its text mirrors one or more
// details. An exact match can reuse its display bytes. For an aggregate, prefer
// an unsigned part so replay of the scalar can survive removal of signed items.
func (r *chatReasoning) scalarMirror(scalar string) *reasoningDetailBlock {
	if scalar == "" {
		return nil
	}
	var exact, aggregate *reasoningDetailBlock
	var pieces []string
	for _, block := range r.blocks {
		text := block.text.String()
		if text == "" {
			continue
		}
		if text == scalar {
			if block.continuation {
				return block
			}
			exact = block
		}
		if aggregate == nil || (aggregate.continuation && !block.continuation) {
			aggregate = block
		}
		pieces = append(pieces, text)
	}
	if exact != nil {
		return exact
	}
	if scalar == strings.Join(pieces, "\n") || scalar == strings.Join(pieces, "") {
		return aggregate
	}
	return nil
}

// fromChatCompletionResponse decodes a non-streaming Chat Completions
// response into an llm.Response. finishMap applies the row's
// Caps.FinishReasonMap before normalization; the caller (Complete) stamps
// Response.Provider from res.Instance after this returns.
//
// The response body preserves native detail metadata and tool arguments that
// a generic map can coerce, including integers larger than a float64 can
// represent exactly. A caller with only a decoded response supplies raw.
func fromChatCompletionResponse(raw map[string]any, finishMap map[string]string, body []byte) (llm.Response, error) {
	b := body
	if b == nil {
		var err error
		b, err = json.Marshal(raw)
		if err != nil {
			return llm.Response{}, err
		}
	}
	var parsed chatCompletionResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		return llm.Response{}, fmt.Errorf("failed to parse chat completion response: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return llm.Response{}, errors.New("no choices in response")
	}
	choice := parsed.Choices[0]

	// Build message.
	var reasoning chatReasoning
	reasoning.add(choice.Message.chatReasoningFields, true)
	parts := reasoning.parts()
	if choice.Message.Content != "" {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: choice.Message.Content})
	}
	var rawArgs []json.RawMessage
	var rawCaptured bool
	for tcIdx, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		// Decode the raw body once; index this call's byte-faithful token.
		if !rawCaptured {
			rawCaptured = true
			rawArgs = captureChatArgsRaw(body)
		}
		if tcIdx < len(rawArgs) && rawArgs[tcIdx] != nil {
			// rawArgs is the RawMessage string token; unescape it to get
			// byte-faithful content for history, validation and replay.
			if content, cerr := protocolhttp.RawStringContent(rawArgs[tcIdx]); cerr == nil {
				args = string(content)
			}
		}
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:               tc.ID,
				Name:             tc.Function.Name,
				Arguments:        json.RawMessage(args),
				ArgumentRepair:   chatArgumentRepair{},
				Type:             "function",
				ThoughtSignature: thoughtSignatureFromExtraContent(tc.ExtraContent),
			},
		})
	}

	msg := llm.Message{Role: llm.RoleAssistant, Content: parts}

	rawFinish := choice.FinishReason
	mappedFinish := mapFinishReason(finishMap, rawFinish)
	var finish llm.FinishReason
	if mappedFinish == rawFinish {
		finish = llm.NormalizeFinishReason("", rawFinish)
	} else {
		finish = llm.FinishReason{Reason: mappedFinish, Raw: rawFinish}
	}

	resp := llm.Response{
		Model:   parsed.Model,
		ID:      parsed.ID,
		Message: msg,
		Finish:  finish,
		Raw:     raw,
	}

	// Usage is normally top-level, but Moonshot/Kimi sometimes report it on
	// choices[0].usage instead. Top-level wins when both are present.
	if parsed.Usage != nil {
		resp.Usage = openaichat.ParseChatUsage(parsed.Usage)
	} else if choice.Usage != nil {
		resp.Usage = openaichat.ParseChatUsage(choice.Usage)
	}

	estimateReasoningUsage(&resp)

	return resp, nil
}

// estimateReasoningUsage fills the display estimate from the settled blocks
// when the provider omits reasoning usage.
func estimateReasoningUsage(resp *llm.Response) {
	if resp.Usage.ReasoningTokens != nil || resp.Usage.ReasoningTokensEstimated != nil {
		return
	}
	chars := 0
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			chars += len(p.Thinking.Text)
		}
	}
	if chars > 0 {
		est := max(chars/4, 1)
		resp.Usage.ReasoningTokensEstimated = &est
	}
}

// rawArgsBodyDecode is the focused raw-args body decode. Tests replace it to
// count focused decodes per response body.
var rawArgsBodyDecode = json.Unmarshal

// captureChatArgsRaw decodes the response body once with a focused struct and
// returns one arguments token per choices[0].message.tool_calls entry, in
// order. Each token is the JSON string token; callers unescape it with
// protocolhttp.RawStringContent. It returns nil if the focused decode fails,
// so the caller keeps the existing string-form args (degrade, never drop).
func captureChatArgsRaw(body []byte) []json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	var focused chatCompletionResponseRaw
	if err := rawArgsBodyDecode(body, &focused); err != nil {
		return nil
	}
	if len(focused.Choices) == 0 {
		return nil
	}
	calls := focused.Choices[0].Message.ToolCalls
	args := make([]json.RawMessage, len(calls))
	for i, call := range calls {
		args[i] = call.Function.Arguments
	}
	return args
}
