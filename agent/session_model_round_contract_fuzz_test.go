//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzModelRoundContracts covers the deterministic bookkeeping and recovery
// decisions surrounding a scripted model round. Provider calls themselves are
// exercised by FuzzMsfzCallModelWithFallback; this target stays entirely below
// that scripted boundary and never performs external I/O.
func FuzzModelRoundContracts(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{1, 1, 1, 1, 1, 1, 1, 1})
	f.Add([]byte{2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte(`{"message":"hello\nworld"}`))
	f.Add([]byte(`{"message":"caf\u00e9 \ud83d\ude00"}`))
	f.Add([]byte(`{"message":"broken\uD800"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &msfzReader{data: data}
		text := string(data)

		// The partial JSON decoder is intentionally tolerant of incomplete model
		// tool arguments. It must never panic and completed JSON must agree with
		// encoding/json semantics for the message field.
		got, ok := partialJSONStringField(text, "message")
		if strings.Contains(text, `"message"`) && ok && strings.Contains(text, `"message":"hello\nworld"`) && got != "hello\nworld" {
			t.Fatalf("decoded message = %q", got)
		}
		_, _, _ = unquoteJSONUnicodeEscape(`\u` + modelRoundHex(r.next(), r.next()) + text)
		for _, raw := range []string{
			`{"message":"plain"}`,
			`{"message":"line\nslash\/tab\t"}`,
			`{"message":"caf\u00e9 \ud83d\ude00"}`,
			`{"message":"unfinished`,
			`{"message":"broken\uD800"}`,
			`{"message":false}`,
			`{"message"}`,
			`{}`,
		} {
			_, _ = partialJSONStringField(raw, "message")
		}
		for _, raw := range []string{`\u00e9tail`, `\ud83d\ude00tail`, `\ud83d`, `\ud83d\u1234`, `\uzzzz`} {
			_, _, _ = unquoteJSONUnicodeEscape(raw)
		}

		req := llm.Request{
			Provider:    strings.TrimSpace(modelRoundChoice(r, "openai", "anthropic", "")),
			Model:       strings.TrimSpace(modelRoundChoice(r, "gpt-5.4", "claude", "")),
			HistoryMode: llm.HistoryMode(modelRoundChoice(r, "", string(llm.HistoryModeFullHistory), string(llm.HistoryModeResponsesDelta))),
		}
		if r.boolean() {
			req.Continuation = &llm.ContinuationMetadata{
				EndpointFamily:          modelRoundChoice(r, "responses", "messages", ""),
				RequestFingerprint:      text,
				StorageScopeFingerprint: modelRoundChoice(r, "scope", "", text),
				ContextMarker:           modelRoundChoice(r, responseContextMarkerV1, "", text),
				PreviousResponseIDHash:  text,
				ConversationIDHash:      modelRoundChoice(r, "conversation", "", text),
				StoragePolicyLabel:      modelRoundChoice(r, "store", "none", ""),
			}
		}

		normalized, meta := singleAttemptRequestMetadata(req)
		if normalized.HistoryMode == "" || meta.AttemptGroupID == "" || meta.AttemptIndex != 1 || meta.FinalAttemptCount == nil {
			t.Fatalf("incomplete single-attempt metadata: req=%+v meta=%+v", normalized, meta)
		}
		resp := llm.Response{Raw: map[string]any{"endpoint_url": modelRoundChoice(r, "https://scripted.invalid", "", text), "id_hash": text}}
		meta = completeAttemptMetadata(meta, resp)

		recorder := newModelAttemptRecorder(meta.AttemptGroupID)
		first := recorder.record(context.Background(), llm.AdapterAttemptRecord{Request: normalized})
		terminal := recorder.record(context.Background(), llm.AdapterAttemptRecord{HistoryMode: normalized.HistoryMode, Terminal: true})
		attempts := recorder.attempts()
		if first.AttemptIndex != 1 || terminal.AttemptIndex != 2 || terminal.FinalAttemptCount == nil || len(attempts) != 2 {
			t.Fatalf("attempt recorder invariant failed: first=%+v terminal=%+v attempts=%d", first, terminal, len(attempts))
		}
		attempts[0].AttemptIndex = 99
		if recorder.attempts()[0].AttemptIndex == 99 {
			t.Fatal("attempts returned aliased storage")
		}

		plan := llm.ResponsesContinuationPlan{
			EndpointFamily:          llm.ResponsesEndpointFamily(modelRoundChoice(r, "responses", "chat_completions", "")),
			RequestFingerprint:      text,
			StorageScopeFingerprint: modelRoundChoice(r, "scope", "", text),
			StoragePolicyLabel:      modelRoundChoice(r, "store", "none", ""),
		}
		full := responsesContinuationFullHistoryRequestForPlan(normalized, plan)
		if full.HistoryMode != llm.HistoryModeFullHistory || full.Continuation == nil {
			t.Fatalf("full-history plan was not materialized: %+v", full)
		}
		candidate := responsesContinuationAnchorCandidate{Turn: schema.Turn{
			ResponseID:                      modelRoundChoice(r, "resp", "", text),
			ResponseRequestFingerprint:      plan.RequestFingerprint,
			ResponseStorageScopeFingerprint: plan.StorageScopeFingerprint,
			ResponseContextMarker:           modelRoundChoice(r, responseContextMarkerV1, "wrong", ""),
		}}
		_ = responsesContinuationCandidateMatchesPlan(candidate, plan)
		_ = responsesContinuationDisabledKeyForPlan(normalized, plan, r.boolean())
		_ = responsesContinuationDisabledKeyForMetadata(normalized, normalized.Continuation, r.boolean())
		_ = responsesContinuationRegistryHasEnabledSupport(map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
			plan.EndpointFamily: {Enabled: r.boolean()},
		})
		disableReq := llm.Request{
			Provider: "openai",
			Model:    "gpt-5.4",
			Continuation: &llm.ContinuationMetadata{
				EndpointFamily:          "responses",
				StorageScopeFingerprint: "scope",
				StoragePolicyLabel:      "store",
			},
		}
		disableSession := newSession(t)
		disableSession.disableResponsesContinuationForRequest(llm.Request{}, false)
		disableSession.disableResponsesContinuationForRequest(disableReq, true)
		disablePlan := llm.ResponsesContinuationPlan{EndpointFamily: "responses", StorageScopeFingerprint: "scope", StoragePolicyLabel: "store"}
		if !disableSession.responsesContinuationDisabledForPlan(disableReq, disablePlan, true) {
			t.Fatal("valid continuation disable key was not retained")
		}

		base := []llm.Message{llm.System("system"), llm.Developer("developer"), llm.User("old")}
		turns := []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User(text)),
			schema.NewTurn(schema.TurnSteering, llm.User("steer")),
			schema.NewTurn(schema.TurnToolResults, llm.Message{Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "ignored"},
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call", Name: "read_file", Content: text}},
			}}),
		}
		if len(responsesContinuationDeltaMessages(base, turns)) < 2 || len(responsesContinuationTurnKinds(turns)) != len(turns) || len(expandHistory(turns, replayScope{})) != 3 {
			t.Fatal("history expansion lost deterministic turns")
		}

		read := r.intn(1000)
		write := r.intn(1000)
		usage := llm.Usage{InputTokens: r.intn(1000), CacheReadTokens: &read, CacheWriteTokens: &write}
		tokens, record := effectiveRecordedInputTokens(usage, r.intn(4000), r.boolean())
		if record != (tokens > 0) {
			t.Fatalf("usage decision inconsistent: tokens=%d record=%v", tokens, record)
		}
		if responseHasServerWebSearch([]llm.ContentPart{{Kind: llm.ContentText}}) || !responseHasServerWebSearch([]llm.ContentPart{{Kind: llm.ContentWebSearch}}) {
			t.Fatal("web-search content detection mismatch")
		}
		for _, tc := range []struct{ window, tokens int }{{0, 100}, {100, 80}, {100, 81}} {
			_, _, _ = contextUsageWarning(tc.window, tc.tokens)
		}
		zero := 0
		one := 1
		_, _ = effectiveRecordedInputTokens(llm.Usage{}, 0, false)
		_, _ = effectiveRecordedInputTokens(llm.Usage{CacheReadTokens: &zero, CacheWriteTokens: &one}, 0, false)
		_, _ = effectiveRecordedInputTokens(llm.Usage{InputTokens: 1}, 100, false)
		_, _ = effectiveRecordedInputTokens(llm.Usage{InputTokens: 1}, 100, true)
		_ = buildTranscriptAPILogResponse(resp, modelRoundChoice(r, "hash", "", text))

		ctx, cancel := context.WithCancel(context.Background())
		if r.boolean() {
			cancel()
		}
		_ = isTurnCancellation(ctx, modelRoundChoiceError(r))
		cancel()
		_ = streamUnavailable(modelRoundChoiceError(r))
		for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, llm.NewAbortError("stop", context.Canceled), llm.NewRequestTimeoutError("openai", "timeout", context.DeadlineExceeded), errors.New("other")} {
			_ = isTurnCancellation(context.Background(), err)
		}
		for _, err := range []error{nil, errStreamUnavailable, llm.ErrStreamUnsupported, errors.New("other")} {
			_ = streamUnavailable(err)
		}

		// Exercise the real retry side effects and both terminal budgets on a
		// session, without invoking a provider.
		sess := newSession(t)
		tracker := retryTracker{
			consecutiveEmpty:    r.intn(maxEmptyRetries + 2),
			totalEmpty:          r.intn(maxTotalEmptyResponses + 2),
			consecutiveBareText: r.intn(maxBareTextRetries + 2),
		}
		_, _ = sess.handleNoToolCalls(r.boolean(), &tracker)
		for _, tc := range []struct {
			noContent bool
			tracker   retryTracker
		}{
			{true, retryTracker{}},
			{true, retryTracker{consecutiveEmpty: 1}},
			{true, retryTracker{consecutiveEmpty: 2}},
			{false, retryTracker{}},
			{true, retryTracker{consecutiveEmpty: maxEmptyRetries, totalEmpty: maxTotalEmptyResponses}},
			{false, retryTracker{consecutiveBareText: maxBareTextRetries}},
		} {
			tracker := tc.tracker
			_, _ = sess.handleNoToolCalls(tc.noContent, &tracker)
		}
	})
}

func modelRoundChoice(r *msfzReader, choices ...string) string {
	return choices[r.intn(len(choices))]
}

func modelRoundHex(a, b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[a&15], digits[a>>4], digits[b&15], digits[b>>4]})
}

func modelRoundChoiceError(r *msfzReader) error {
	switch r.intn(6) {
	case 0:
		return nil
	case 1:
		return context.Canceled
	case 2:
		return context.DeadlineExceeded
	case 3:
		return llm.ErrStreamUnsupported
	case 4:
		return errors.Join(errStreamUnavailable, errors.New("scripted stream unavailable"))
	default:
		return llm.NewRequestTimeoutError("openai", "scripted timeout", context.DeadlineExceeded)
	}
}
