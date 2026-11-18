package chatcompletions

import (
	"encoding/json"
	"errors"
	"fmt"
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
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	Reasoning        string                `json:"reasoning,omitempty"`
	ReasoningText    string                `json:"reasoning_text,omitempty"`
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
// Raw keeps the item's verbatim JSON so encrypted items replay unchanged —
// providers may require metadata beyond the fields decoded here.
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
// in Raw for lossless replay of encrypted items.
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

// carriesContinuation reports whether the item holds provider continuation
// material that must survive replay: encrypted blocks, or reasoning.text
// items bearing a signature.
func (d reasoningDetailItem) carriesContinuation() bool {
	return d.isEncrypted() || (d.Type == "reasoning.text" && d.Signature != "")
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
	var wire map[string]any
	if len(d.Raw) > 0 && json.Unmarshal(d.Raw, &wire) == nil {
		return wire
	}
	b, _ := json.Marshal(d)
	_ = json.Unmarshal(b, &wire)
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
	for i, v := range [...]string{f.ReasoningContent, f.Reasoning, f.ReasoningText} {
		if v != "" {
			return v, reasoningFieldNames[i]
		}
	}
	return "", ""
}

type reasoningDetailBlock struct {
	text strings.Builder
	wire map[string]any
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
	open         map[int]*reasoningDetailBlock
	blocks       []*reasoningDetailBlock
	continuation []*reasoningDetailBlock
	hasSignature bool
}

func (r *chatReasoning) add(f chatReasoningFields) string {
	scalar, field := f.scalar()
	var detailDelta strings.Builder
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
		r.addDetail(pos, item, piece)
	}
	delta := scalar
	if detailDelta.Len() != 0 {
		delta = detailDelta.String()
		if r.display == nil {
			r.display = &strings.Builder{}
			r.display.WriteString(r.scalar.String())
			r.displayField = r.scalarField
		}
	}
	if r.display != nil {
		r.display.WriteString(delta)
	}
	if scalar != "" {
		r.scalar.WriteString(scalar)
		if r.scalarField == "" {
			r.scalarField = field
		}
	}
	return delta
}

func (r *chatReasoning) addDetail(index int, item reasoningDetailItem, piece string) {
	if item.isEncrypted() {
		r.continuation = append(r.continuation, &reasoningDetailBlock{wire: item.wireItem()})
		return
	}
	if piece == "" && !item.carriesContinuation() {
		return
	}
	if item.Index != nil {
		index = *item.Index
	}
	block := r.open[index]
	if block == nil {
		block = &reasoningDetailBlock{}
		if r.open == nil {
			r.open = make(map[int]*reasoningDetailBlock)
		}
		r.open[index] = block
		r.blocks = append(r.blocks, block)
	}
	block.text.WriteString(piece)
	if item.carriesContinuation() {
		block.wire = item.wireItem()
		if block.text.Len() > 0 {
			block.wire["text"] = block.text.String()
		}
		r.continuation = append(r.continuation, block)
		r.hasSignature = true
		delete(r.open, index)
	}
}

func (r *chatReasoning) parts() []llm.ContentPart {
	parts := []llm.ContentPart{}
	appendPart := func(thinking llm.ThinkingData) {
		if thinking.Text != "" || thinking.EncryptedContent != "" {
			parts = append(parts, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &thinking})
		}
	}
	scalar := r.scalar.String()
	if !r.hasSignature {
		thinking := llm.ThinkingData{Text: scalar, Signature: r.scalarField}
		if r.display != nil {
			thinking.Text, thinking.Signature = r.display.String(), r.displayField
			if scalar != "" {
				setChatReplayScalar(&thinking, r.scalarField, scalar)
			}
		}
		var opaque []map[string]any
		for _, block := range r.continuation {
			opaque = append(opaque, block.wire)
		}
		thinking.EncryptedContent = encodeEncryptedDetails(opaque)
		appendPart(thinking)
		return parts
	}
	mirror := r.scalarMirror(scalar)
	if scalar != "" && mirror == nil {
		appendPart(llm.ThinkingData{Text: scalar, Signature: r.scalarField})
	}
	for _, block := range r.blocks {
		if block.wire == nil {
			thinking := llm.ThinkingData{Text: block.text.String()}
			if block == mirror {
				setChatReplayScalar(&thinking, r.scalarField, scalar)
			} else if scalar != "" {
				thinking.ChatScalar = &llm.ChatReasoningScalar{}
			}
			appendPart(thinking)
		}
	}
	for _, block := range r.continuation {
		thinking := llm.ThinkingData{Text: block.text.String(), EncryptedContent: encodeEncryptedDetails([]map[string]any{block.wire})}
		if block == mirror {
			setChatReplayScalar(&thinking, r.scalarField, scalar)
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
			if block.wire != nil {
				return block
			}
			exact = block
		}
		if aggregate == nil || (aggregate.wire != nil && block.wire == nil) {
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
// body is the raw response body bytes; when non-nil, a focused decode
// captures tool-call arguments as json.RawMessage so rawStringContent can
// unescape them preserving bytes (invalid UTF-8, non-canonical JSON) that
// the map[string]any decode coerces. When nil, the existing string-form
// path is used (args come from the lossy map, already U+FFFD-coerced for
// invalid UTF-8).
func fromChatCompletionResponse(raw map[string]any, finishMap map[string]string, body []byte) (llm.Response, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return llm.Response{}, err
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
	reasoning.add(choice.Message.chatReasoningFields)
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
			// byte-faithful content. rescueClaudeXMLArgs still applies on
			// the unescaped content (it returns it unchanged when no XML is
			// present, preserving raw bytes).
			if content, cerr := protocolhttp.RawStringContent(rawArgs[tcIdx]); cerr == nil {
				args = string(content)
			}
		}
		// Pick the raw or fallback string first, then rescue once: rescuing
		// both candidates parses large argument objects twice and throws the
		// first result away.
		args = rescueClaudeXMLArgs(args)
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:               tc.ID,
				Name:             tc.Function.Name,
				Arguments:        json.RawMessage(args),
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
