package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// summarizerScript builds the scripted LLM boundary for retry tests: every
// request goes through next with its 1-based call number, and the returned
// snapshot reports the model each call targeted, in order.
func summarizerScript(next func(call int, req llm.Request) (llm.Response, error)) (*llm.Client, func() []string) {
	client := llm.NewClient()
	var calls []string
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		calls = append(calls, req.Model)
		return next(len(calls), req)
	}})
	return client, func() []string { return append([]string(nil), calls...) }
}

// summaryAcceptance is the validator the conversation summary runs on each
// summarizer reply (summarizeFold's accept): a summary section with content
// that was not cut off at the model's output limit.
func summaryAcceptance(reply llm.Response) error {
	if err := checkSummaryReply(reply.Text(), ""); err != nil {
		return err
	}
	return checkNotCutOff(reply)
}

var (
	rejectedSummaryReply = llm.Response{Message: llm.Assistant("")}
	acceptedSummaryReply = llm.Response{Message: llm.Assistant("## Progress\nRecovered.")}
)

func retryTestManager(client *llm.Client, profile *provider.Profile) *Manager {
	return NewManager(profile, client, cheapmodel.New(client))
}

// A summarizer reply that fails local validation is one bad sample, not a
// route verdict: the route that produced it answers up to three attempts, and
// a sample that passes on a later attempt recovers the summary without a
// session-model call.
func TestSummarizationRecoversOnALaterSampleWithoutFallingBack(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(call int, _ llm.Request) (llm.Response, error) {
		if call <= 2 {
			return rejectedSummaryReply, nil
		}
		return acceptedSummaryReply, nil
	})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := retryTestManager(client, profile)
	resp, err := cm.completeSummarization(context.Background(), profile, "prompt", summaryAcceptance)
	if err != nil {
		t.Fatalf("completeSummarization: %v", err)
	}
	if !strings.Contains(resp.Text(), "Recovered.") {
		t.Fatalf("summary = %q, want the third sample's reply", resp.Text())
	}
	if got := calls(); strings.Join(got, " ") != "cheap-fixture cheap-fixture cheap-fixture" {
		t.Fatalf("routes = %v, want three cheap-route calls", got)
	}
}

// A route whose every sample is rejected hands the summary to the next route
// with its attempt budget intact: the session model answers the fold after the
// cheap route spent its three samples.
func TestSummarizationFallsBackToSessionModelAfterCheapRouteRetries(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(call int, req llm.Request) (llm.Response, error) {
		if req.Model == "cheap-fixture" {
			return rejectedSummaryReply, nil
		}
		return acceptedSummaryReply, nil
	})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := retryTestManager(client, profile)
	resp, err := cm.completeSummarization(context.Background(), profile, "prompt", summaryAcceptance)
	if err != nil {
		t.Fatalf("completeSummarization: %v", err)
	}
	if !strings.Contains(resp.Text(), "Recovered.") {
		t.Fatalf("summary = %q, want the session model's reply", resp.Text())
	}
	if got := calls(); strings.Join(got, " ") != "cheap-fixture cheap-fixture cheap-fixture active-fixture" {
		t.Fatalf("routes = %v, want three cheap-route calls then the session model", got)
	}
}

// When every route exhausts its samples, the failure still shows one attempt
// per route: the operator sees the cheap route was tried, not only the
// terminal session attempt the two-entry cap would otherwise keep.
func TestSummarizationRecordsOneAttemptPerRouteWhenEverySampleFails(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(int, llm.Request) (llm.Response, error) {
		return rejectedSummaryReply, nil
	})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := retryTestManager(client, profile)
	_, err := cm.completeSummarization(context.Background(), profile, "prompt", summaryAcceptance)
	if err == nil {
		t.Fatal("every sample rejected, want a failure")
	}
	if got := calls(); len(got) != 6 {
		t.Fatalf("routes = %v, want three samples per route", got)
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil {
		t.Fatal("missing summary failure diagnostic")
	}
	if failure.Terminal.Kind != "rejected_output" || failure.Terminal.Reason != "empty_reply" ||
		failure.Terminal.Route != "session" || failure.Terminal.Model != "active-fixture" {
		t.Fatalf("wrong terminal attempt: %+v", failure.Terminal)
	}
	if len(failure.Previous) != 2 {
		t.Fatalf("previous attempts = %+v, want one per route", failure.Previous)
	}
	if failure.Previous[0].Route != "cheap" || failure.Previous[0].Model != "cheap-fixture" ||
		failure.Previous[0].Reason != "empty_reply" {
		t.Fatalf("cheap route evidence lost: %+v", failure.Previous[0])
	}
	if failure.Previous[1].Route != "session" || failure.Previous[1].Model != "active-fixture" {
		t.Fatalf("session route retry evidence lost: %+v", failure.Previous[1])
	}
}

// With a single summarization route, the failure's previous evidence is that
// route's own earlier sample: the operator sees the retry happened.
func TestSummarizationSingleRouteRecordsItsOwnRetries(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(int, llm.Request) (llm.Response, error) {
		return rejectedSummaryReply, nil
	})
	profile := testProfile("openai", "active-fixture", 1000)
	cm := retryTestManager(client, profile)
	_, err := cm.completeSummarization(context.Background(), profile, "prompt", summaryAcceptance)
	if err == nil {
		t.Fatal("every sample rejected, want a failure")
	}
	if got := calls(); len(got) != 3 {
		t.Fatalf("routes = %v, want three samples on the one route", got)
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil || len(failure.Previous) != 1 ||
		failure.Previous[0].Route != "session" || failure.Previous[0].Model != "active-fixture" {
		t.Fatalf("want the session route's own earlier sample in previous, got %+v", failure)
	}
}

// A reply cut off at the model's output limit is a route capability, not a bad
// sample: the route gets one attempt and the session rung takes over as before.
func TestSummarizationDoesNotRetryAnOutputLimitCut(t *testing.T) {
	t.Parallel()
	cutOffReply := llm.Response{
		Message: llm.Assistant("## Progress\npartial"),
		Finish:  llm.FinishReason{Reason: llm.FinishReasonLength},
	}
	client, calls := summarizerScript(func(int, llm.Request) (llm.Response, error) {
		return cutOffReply, nil
	})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := retryTestManager(client, profile)
	_, err := cm.completeSummarization(context.Background(), profile, "prompt", summaryAcceptance)
	if err == nil {
		t.Fatal("cut-off replies accepted, want a failure")
	}
	if got := calls(); strings.Join(got, " ") != "cheap-fixture active-fixture" {
		t.Fatalf("routes = %v, want one attempt per route", got)
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil || failure.Terminal.Reason != "output_limit" {
		t.Fatalf("want the output-limit rejection as terminal, got %+v", failure)
	}
}

// A valid first reply costs exactly one call: retries only answer a rejected
// sample.
func TestSummarizationMakesNoExtraCallWhenTheFirstReplyIsValid(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(int, llm.Request) (llm.Response, error) {
		return acceptedSummaryReply, nil
	})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := retryTestManager(client, profile)
	if _, err := cm.completeSummarization(context.Background(), profile, "prompt", summaryAcceptance); err != nil {
		t.Fatalf("completeSummarization: %v", err)
	}
	if got := calls(); strings.Join(got, " ") != "cheap-fixture" {
		t.Fatalf("routes = %v, want a single call", got)
	}
}

// A cancelled run unwinds on the attempt in flight: no retry and no session
// rung on a dead context.
func TestSummarizationStopsRetryingWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	client, calls := summarizerScript(func(int, llm.Request) (llm.Response, error) {
		cancel()
		return rejectedSummaryReply, nil
	})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := retryTestManager(client, profile)
	_, err := cm.completeSummarization(ctx, profile, "prompt", summaryAcceptance)
	if err == nil {
		t.Fatal("rejected reply on a cancelled run, want a failure")
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("routes = %v, want the one in-flight attempt only", got)
	}
	if rejection, ok := errors.AsType[summaryRejectionError](err); !ok || rejection != errSummaryEmptyReply {
		t.Fatalf("terminal = %v, want the rejected sample as the failure", err)
	}
}

// The whole-journey contract: a fold whose first samples are rejected installs
// the later sample's summary and warns nobody.
func TestSummarizeFoldRecoversFromRejectedRepliesWithoutAWarning(t *testing.T) {
	t.Parallel()
	client, _ := summarizerScript(func(call int, _ llm.Request) (llm.Response, error) {
		if call <= 2 {
			return rejectedSummaryReply, nil
		}
		return acceptedSummaryReply, nil
	})
	profile := testOpenAIProfileWithContextWindow(minSummarizingWindow)
	cm := retryTestManager(client, profile)
	cm.PreserveRecentTurns = 2
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Fix the auth bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent")),
	}
	emit, warnings := warningsFrom()
	if summarized := cm.ForceCompact(context.Background(), &history, "", emit); !summarized {
		t.Fatal("fold did not install a summary")
	}
	if len(*warnings) != 0 {
		t.Fatalf("a recovered fold warned: %+v", *warnings)
	}
	if history[0].Kind != schema.TurnSummary || !strings.Contains(history[0].Message.Text(), "Recovered.") {
		t.Fatalf("history head = %s, want the recovered summary", history[0].Kind)
	}
}

// A condensation batch that answers empty twice is resampled within the
// batch's own budget: the third sample completes the condensation.
func TestCondensePartnerMessagesRetriesAnEmptyBatchReply(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(call int, _ llm.Request) (llm.Response, error) {
		if call <= 2 {
			return rejectedSummaryReply, nil
		}
		return llm.Response{Message: llm.Assistant("condensed-3f9a")}, nil
	})
	profile := testProfile("openai", "active-fixture", 1000)
	cm := retryTestManager(client, profile)
	condensed, err := cm.condensePartnerMessages(context.Background(), profile, []string{strings.Repeat("x", 101)}, 100)
	if err != nil {
		t.Fatalf("condensePartnerMessages: %v", err)
	}
	if !strings.Contains(condensed, "condensed-3f9a") {
		t.Fatalf("condensation = %q, want the third sample's reply", condensed)
	}
	if got := calls(); len(got) != 3 {
		t.Fatalf("batch calls = %v, want three samples", got)
	}
}

// The session-log fork summary shares the conversation summary's failure
// class: an unparseable reply is one bad sample and is resampled before the
// failure surfaces.
func TestForkSummarizeRetriesAnUnparseableReply(t *testing.T) {
	t.Parallel()
	client, calls := summarizerScript(func(call int, _ llm.Request) (llm.Response, error) {
		if call <= 2 {
			return llm.Response{Message: llm.Assistant("not JSON")}, nil
		}
		return llm.Response{Message: llm.Assistant(`{"action":"shell","summary":"ran","outcome":"success"}`)}, nil
	})
	profile := testProfile("openai", "active-fixture", 1000)
	entry, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, canaryHistory(), 7)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}
	if entry.Action != "shell" || entry.Turn != 7 {
		t.Fatalf("entry = %+v, want the third sample's parsed entry", entry)
	}
	if got := calls(); len(got) != 3 {
		t.Fatalf("fork calls = %v, want three samples", got)
	}
}

// A fork reply that never parses fails after the sample budget without
// embedding the raw reply: this error surfaces as a warning, and warnings
// never carry conversation-adjacent text.
func TestForkSummarizeErrorCarriesNoReplyText(t *testing.T) {
	t.Parallel()
	const canary = "junk-fork-reply-6b1c"
	client, calls := summarizerScript(func(int, llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant(canary)}, nil
	})
	profile := testProfile("openai", "active-fixture", 1000)
	_, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, canaryHistory(), 7)
	if err == nil {
		t.Fatal("unparseable replies accepted, want a failure")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("fork error embeds the raw reply: %v", err)
	}
	if got := calls(); len(got) != 3 {
		t.Fatalf("fork calls = %v, want three samples", got)
	}
}
