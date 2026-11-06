package contextmgr

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A rejected summarizer reply is retried on its own route before the pass
// gives up: the empty first reply from the only route (the session model,
// Jesse's case) does not fail the fold when the second attempt summarizes.
func TestSummarizeRecoversFromRejectedSessionReply(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		if calls.Add(1) == 1 {
			return llm.Response{Message: llm.Assistant("")}, nil
		}
		return llm.Response{Message: llm.Assistant("## Progress\nrecovered-summary-3d7a")}, nil
	}})
	profile := testProfile("openai", "active-fixture", 100000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	history := pingPongHistory("fix the auth bug", "I'll fix it", "run the tests", "all green")

	result, err := cm.summarizeFold(context.Background(), history, 2, "", nil)

	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one rejected, one retry)", calls.Load())
	}
	if len(result) == 0 || result[0].Kind != schema.TurnSummary {
		t.Fatalf("no summary installed: %+v", result)
	}
	if got := result[0].Message.Text(); !strings.Contains(got, "recovered-summary-3d7a") {
		t.Fatalf("summary text = %q, want the second attempt's reply", got)
	}
}

// A route that keeps producing rejected replies is tried three times before
// the resolution fails, and the failure is the rejection, not a silent
// acceptance of the last bad reply.
func TestSummarizeRejectedReplyRetriesThreeAttemptsPerRoute(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		calls.Add(1)
		return llm.Response{Message: llm.Assistant("")}, nil
	}})
	profile := testProfile("openai", "active-fixture", 100000)
	cm := NewManager(profile, client, cheapmodel.New(client))

	_, err := cm.completeSummarization(context.Background(), profile, "prompt", func(resp llm.Response) error {
		return checkSummaryReply(resp.Text(), "")
	})

	if !errors.Is(err, errSummaryEmptyReply) {
		t.Fatalf("err = %v, want empty_reply rejection", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 attempts on the single route", calls.Load())
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil || failure.Terminal.Attempts != 3 || failure.Terminal.Reason != "empty_reply" {
		t.Fatalf("wrong terminal evidence: %+v", failure)
	}
}

// The configured cheap route spends its full retry budget on its own rejected
// replies before the session model is asked at all.
func TestSummarizeSpendsCheapRouteRetriesBeforeSessionFallback(t *testing.T) {
	t.Parallel()
	var models []string
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		models = append(models, req.Model)
		if req.Model == "cheap-fixture" {
			return llm.Response{Message: llm.Assistant("narration without a section")}, nil
		}
		if len(models) == 4 {
			return llm.Response{Message: llm.Assistant("still narrating")}, nil
		}
		return llm.Response{Message: llm.Assistant("## Progress\nrecovered-summary-5b1c")}, nil
	}})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 100000), "cheap-fixture")
	cm := NewManager(profile, client, cheapmodel.New(client))

	resp, err := cm.completeSummarization(context.Background(), profile, "prompt", func(resp llm.Response) error {
		return checkSummaryReply(resp.Text(), "")
	})

	if err != nil {
		t.Fatalf("completeSummarization: %v", err)
	}
	if want := []string{"cheap-fixture", "cheap-fixture", "cheap-fixture", "active-fixture", "active-fixture"}; !slices.Equal(models, want) {
		t.Fatalf("call order = %v, want %v", models, want)
	}
	if !strings.Contains(resp.Text(), "recovered-summary-5b1c") {
		t.Fatalf("result text = %q, want the session retry's reply", resp.Text())
	}
}

// When both routes exhaust their retry budgets, the failure records the cheap
// route's rejection as a prior attempt and the session route as terminal.
func TestSummarizeExhaustedRoutesReportCheapRejectionAsPrior(t *testing.T) {
	t.Parallel()
	var models []string
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
		models = append(models, req.Model)
		return llm.Response{Message: llm.Assistant("narration without a section")}, nil
	}})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 100000), "cheap-fixture")
	cm := NewManager(profile, client, cheapmodel.New(client))

	_, err := cm.completeSummarization(context.Background(), profile, "prompt", func(resp llm.Response) error {
		return checkSummaryReply(resp.Text(), "")
	})

	if !errors.Is(err, errSummaryMissingSection) {
		t.Fatalf("err = %v, want missing_summary_section rejection", err)
	}
	if want := []string{"cheap-fixture", "cheap-fixture", "cheap-fixture", "active-fixture", "active-fixture", "active-fixture"}; !slices.Equal(models, want) {
		t.Fatalf("call order = %v, want %v", models, want)
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil {
		t.Fatal("missing route evidence")
	}
	if failure.Terminal.Route != "session" || failure.Terminal.Attempts != 3 || len(failure.Previous) != 1 || failure.Previous[0].Route != "cheap" || failure.Previous[0].Attempts != 3 {
		t.Fatalf("wrong route evidence: %+v", failure)
	}
}

// Every reply rejection kind retries the same way, whatever validation raised
// it.
func TestSummarizeRetriesEveryRejectedReplyKind(t *testing.T) {
	t.Parallel()
	for _, rejection := range []summaryRejectionError{
		errSummaryEmptyReply,
		errSummaryMissingSection,
		errSummaryOutputLimit,
		errSummaryEmptyBatch,
	} {
		t.Run(string(rejection), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
				calls.Add(1)
				return llm.Response{Message: llm.Assistant("a reply")}, nil
			}})
			profile := testProfile("openai", "active-fixture", 100000)
			cm := NewManager(profile, client, cheapmodel.New(client))
			var judged atomic.Int32

			_, err := cm.completeSummarization(context.Background(), profile, "prompt", func(llm.Response) error {
				if judged.Add(1) == 1 {
					return rejection
				}
				return nil
			})

			if err != nil {
				t.Fatalf("completeSummarization: %v", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("calls = %d, want the rejected attempt plus one retry", calls.Load())
			}
		})
	}
}

// A resolution with no route to call makes no calls at all: a missing
// summarization model is a configuration failure, not something a retry can
// fix. (The other deterministic local failure, uncondensable partner
// messages, is raised by condensePartnerMessages after its loop, outside
// completeSummarization's judge, so it never reaches the re-roll ladder.)
func TestSummarizeMissingModelMakesNoCalls(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		calls.Add(1)
		return llm.Response{Message: llm.Assistant("a reply")}, nil
	}})
	cm := NewManager(nil, client, cheapmodel.New(client))

	_, err := cm.completeSummarization(context.Background(), nil, "prompt", nil)

	if !errors.Is(err, errSummaryMissingModel) {
		t.Fatalf("err = %v, want missing model", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want none: there is no route to call", calls.Load())
	}
}

// A provider error surfacing on a retry ends the route: the client has already
// retried transient provider failures, so the resolution reports the API error
// as terminal instead of re-rolling a call that errored.
func TestSummarizeProviderErrorOnRetryIsTerminal(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	serverErr := llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil)
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		if calls.Add(1) == 1 {
			return llm.Response{Message: llm.Assistant("")}, nil
		}
		return llm.Response{}, serverErr
	}})
	profile := testProfile("openai", "active-fixture", 100000)
	cm := NewManager(profile, client, cheapmodel.New(client))

	_, err := cm.completeSummarization(context.Background(), profile, "prompt", func(resp llm.Response) error {
		return checkSummaryReply(resp.Text(), "")
	})

	if !errors.Is(err, serverErr) {
		t.Fatalf("err = %v, want the provider error as terminal", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want the rejected attempt plus the errored retry", calls.Load())
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil || failure.Terminal.Kind != "server" || failure.Terminal.Status != 503 || failure.Terminal.Attempts != 2 {
		t.Fatalf("wrong terminal evidence: %+v", failure)
	}
}

// Cancellation between attempts stops the resolution immediately and reports
// the cancellation, never a rejection of the last reply.
func TestSummarizeRetriesStopOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		calls.Add(1)
		cancel()
		return llm.Response{Message: llm.Assistant("")}, nil
	}})
	profile := testProfile("openai", "active-fixture", 100000)
	cm := NewManager(profile, client, cheapmodel.New(client))

	_, err := cm.completeSummarization(ctx, profile, "prompt", func(resp llm.Response) error {
		return checkSummaryReply(resp.Text(), "")
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want no retry after cancellation", calls.Load())
	}
	failure := summarizerFailureWarning(err).SummaryFailure
	if failure == nil || failure.Terminal.Kind != "cancelled" {
		t.Fatalf("wrong terminal evidence: %+v", failure)
	}
}
