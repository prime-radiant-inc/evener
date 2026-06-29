package difftest

import (
	"fmt"
	"strings"
)

// The encoders below are the inverse of each provider's stream decoder: they
// serialize a logicalResponse into the minimal SSE that drives the decoder to
// reconstruct the same logical content. They are NOT full API fidelity — they
// emit only the fields each decoder actually reads (learned from the metamorphic
// fuzz seeds in each provider package and the decode paths).
//
// Usage encoding note: every encoder emits total = input+output and no cache
// fields, because all four parseUsage implementations compute the triple that
// way when no cache details are present. For Anthropic, output_tokens is also
// emitted in message_start (not just message_delta) so parseUsage's
// total=input+output is computed from the final figures — Anthropic's wire
// normally reports output_tokens:0 at message_start, which would make the
// decoded TotalTokens stale (input+0); emitting the final output keeps the
// triple self-consistent for the differential.

// ---- Anthropic (messages event stream) ----

func encodeAnthropic(lr logicalResponse) []byte {
	var b strings.Builder
	ev := func(event, data string) {
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", event, data)
	}

	ev("message_start", fmt.Sprintf(
		`{"type":"message_start","message":{"id":"msg_diff","model":"diff-model","usage":{"input_tokens":%d,"output_tokens":%d}}}`,
		lr.InTok, lr.OutTok))

	idx := 0
	block := func(start, delta string) {
		ev("content_block_start", start)
		ev("content_block_delta", delta)
		ev("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, idx))
		idx++
	}

	if lr.Reasoning != "" {
		block(
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, idx),
			fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`, idx, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		block(
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, idx),
			fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, idx, qs(lr.Text)))
	}
	for i, tc := range lr.Tools {
		block(
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"toolu_%d","name":%s,"input":{}}}`, idx, i, qs(tc.Name)),
			fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, idx, qs(tc.argsJSON())))
	}

	stop := map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use"}[lr.Finish]
	ev("message_delta", fmt.Sprintf(
		`{"type":"message_delta","delta":{"stop_reason":%s},"usage":{"output_tokens":%d}}`, qs(stop), lr.OutTok))
	ev("message_stop", `{"type":"message_stop"}`)
	return []byte(b.String())
}

// ---- Google Gemini (streamGenerateContent, alt=sse) ----

func encodeGoogle(lr logicalResponse) []byte {
	var parts []string
	if lr.Reasoning != "" {
		parts = append(parts, fmt.Sprintf(`{"thought":true,"text":%s}`, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		parts = append(parts, fmt.Sprintf(`{"text":%s}`, qs(lr.Text)))
	}
	for _, tc := range lr.Tools {
		parts = append(parts, fmt.Sprintf(`{"functionCall":{"name":%s,"args":%s}}`, qs(tc.Name), tc.argsJSON()))
	}

	finish := map[string]string{"stop": "STOP", "length": "MAX_TOKENS", "tool_calls": "STOP"}[lr.Finish]

	// usageMetadata is emitted in its own chunk BEFORE the finishReason chunk.
	// The Gemini stream decoder builds the final response and returns the moment
	// it sees a finishReason, parsing usageMetadata only on a chunk that lacks a
	// finish reason; this separate-chunk shape matches how the decoder is fed (and
	// the provider's metamorphic seed). See report: usage carried on the SAME
	// chunk as finishReason is dropped by the decoder (lower-confidence finding).
	var b strings.Builder
	fmt.Fprintf(&b, "data: {\"candidates\":[{\"content\":{\"parts\":[%s]}}]}\n\n", strings.Join(parts, ","))
	fmt.Fprintf(&b,
		"data: {\"usageMetadata\":{\"promptTokenCount\":%d,\"candidatesTokenCount\":%d,\"totalTokenCount\":%d}}\n\n",
		lr.InTok, lr.OutTok, lr.totalTok())
	fmt.Fprintf(&b, "data: {\"candidates\":[{\"finishReason\":%s}]}\n\n", qs(finish))
	return []byte(b.String())
}

// ---- OpenAI Responses API ----

func encodeOpenAIResponses(lr logicalResponse) []byte {
	var b strings.Builder
	frame := func(data string) { fmt.Fprintf(&b, "data: %s\n\n", data) }

	if lr.Reasoning != "" {
		frame(`{"type":"response.reasoning_summary_part.added"}`)
		frame(fmt.Sprintf(`{"type":"response.reasoning_summary_text.delta","delta":%s}`, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		frame(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%s}`, qs(lr.Text)))
	}
	for i, tc := range lr.Tools {
		args := tc.argsJSON()
		frame(fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_%d","id":"fc_%d","name":%s}}`, i, i, qs(tc.Name)))
		frame(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","call_id":"call_%d","delta":%s}`, i, qs(args)))
		frame(fmt.Sprintf(`{"type":"response.function_call_arguments.done","call_id":"call_%d","arguments":%s}`, i, qs(args)))
	}

	// The final Response is rebuilt from response.completed.output (the
	// StreamAccumulator prefers a Finish event's Response when it carries
	// content), so every content unit must also appear in the output array.
	var output []string
	if lr.Text != "" {
		output = append(output, fmt.Sprintf(`{"type":"message","content":[{"type":"output_text","text":%s}]}`, qs(lr.Text)))
	}
	for i, tc := range lr.Tools {
		output = append(output, fmt.Sprintf(`{"type":"function_call","call_id":"call_%d","id":"fc_%d","name":%s,"arguments":%s}`, i, i, qs(tc.Name), qs(tc.argsJSON())))
	}

	usage := fmt.Sprintf(`"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}`, lr.InTok, lr.OutTok, lr.totalTok())
	var completed string
	if lr.Finish == "length" {
		completed = fmt.Sprintf(
			`{"type":"response.completed","response":{"id":"resp_diff","model":"diff-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[%s],%s}}`,
			strings.Join(output, ","), usage)
	} else {
		completed = fmt.Sprintf(
			`{"type":"response.completed","response":{"id":"resp_diff","model":"diff-model","status":"completed","output":[%s],%s}}`,
			strings.Join(output, ","), usage)
	}
	frame(completed)
	return []byte(b.String())
}

// ---- OpenAI-compatible Chat Completions ----

func encodeOpenAICompat(lr logicalResponse) []byte {
	var b strings.Builder
	frame := func(data string) { fmt.Fprintf(&b, "data: %s\n\n", data) }

	if lr.Reasoning != "" {
		frame(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%s}}]}`, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		frame(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"role":"assistant","content":%s}}]}`, qs(lr.Text)))
	}
	for i, tc := range lr.Tools {
		frame(fmt.Sprintf(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"id":"call_%d","type":"function","function":{"name":%s,"arguments":%s}}]}}]}`,
			i, i, qs(tc.Name), qs(tc.argsJSON())))
	}

	finish := map[string]string{"stop": "stop", "length": "length", "tool_calls": "tool_calls"}[lr.Finish]
	frame(fmt.Sprintf(`{"choices":[{"index":0,"delta":{},"finish_reason":%s}]}`, qs(finish)))
	frame(fmt.Sprintf(`{"id":"c_diff","model":"diff-model","usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`, lr.InTok, lr.OutTok, lr.totalTok()))
	frame("[DONE]")
	return []byte(b.String())
}
