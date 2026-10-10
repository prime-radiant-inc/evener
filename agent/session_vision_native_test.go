package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// visionRouteError is the ordinary provider failure the retry tests script on a
// configured vision route: permanent enough to reach the caller, not a refusal.
func visionRouteError(provider string) error {
	return llm.ErrorFromHTTPStatus(provider, 500, "vision route exploded", nil, nil)
}

// visionRouteRefusal is the permanent model-refusal wording cheapmodel learns
// a route by, so its internal session-model fallback fires.
func visionRouteRefusal(provider string) error {
	return llm.ErrorFromHTTPStatus(provider, 400, "The provided model identifier is invalid.", nil, nil)
}

// When the session model sees tool-result images natively and no vision model
// is configured, the describing side-channel is pure duplication: the same
// bytes already travel inline in the tool result. The call must not fire.
func TestDescribeImage_SkipsCallWhenSessionModelSeesImagesNatively(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("native-vision result = %+v, want success with no description", result)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("side-channel fired %d calls for a session model that sees images natively", len(openai.Requests()))
	}
}

// The skip is per-media: no adapter delivers PDF tool-result bytes inline, so
// a vision-capable session model still gets PDFs described out of band.
func TestDescribeImage_StillDescribesPDFsWhenSessionModelSeesImages(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	}}
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf", ImagePrompt: "describe it",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "session-described" {
		t.Fatalf("PDF result = %+v, want the side-channel description", result)
	}
	reqs := openai.Requests()
	if len(reqs) != 1 {
		t.Fatalf("PDF side-channel calls = %d, want 1", len(reqs))
	}
	media := reqs[0].Messages[0].Content[1]
	if media.Kind != llm.ContentDocument {
		t.Fatalf("PDF media part = %v, want ContentDocument", media.Kind)
	}
}

// A generated description is steering text every later serving model can
// read; skipping it is safe only while every model that might serve sees the
// bytes natively. A model_fallbacks entry that cannot (a text-only row) must
// keep the descriptions coming even when the primary is vision-capable.
func TestDescribeImage_FallbackThatCannotSeeImagesKeepsDescriptions(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	}}
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{ModelFallbacks: []string{"openai/gpt-5.2-codex"}}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "session-described" {
		t.Fatalf("fallback-chain result = %+v, want the side-channel description for the text-only fallback", result)
	}
	if len(openai.Requests()) != 1 {
		t.Fatalf("side-channel calls = %d, want the description the fallback needs", len(openai.Requests()))
	}
}

// Fallbacks that all see tool-result images natively preserve the skip.
func TestDescribeImage_FallbacksThatSeeImagesKeepTheNativeSkip(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{ModelFallbacks: []string{"openai/o4-mini", "openai/gpt-5.2"}}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("capable-chain result = %+v, want the native skip with no description", result)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("side-channel fired %d calls though every serving model sees images natively", len(openai.Requests()))
	}
}

// An explicitly configured vision model stays a deliberate instruction even
// when the session model is vision-capable: the ref keeps describing.
func TestDescribeImage_ExplicitRefStillRunsWhenSessionModelSeesImages(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("routed-vision")}, nil
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	if got := sess.describeImage(context.Background(), visionImageResult()); got != "routed-vision" {
		t.Fatalf("routed description = %q, want routed-vision", got)
	}
	if len(openai.Requests()) != 0 {
		t.Fatal("session provider received a vision call despite the pinned route")
	}
	reqs := anthropic.Requests()
	if len(reqs) != 1 || reqs[0].Model != "claude-x" {
		t.Fatalf("anthropic requests = %d (model %q), want one claude-x call", len(reqs), visionModelOf(reqs))
	}
}

// The always-try-the-main-model rule: when the configured vision route fails
// for an ordinary provider reason and the session model accepts image input,
// the description retries once on the session model before vision is
// declared unavailable.
func TestDescribeImage_FallsBackToSessionModelWhenConfiguredRouteFails(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	}}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "session-described" {
		t.Fatalf("fallback result = %+v, want the session model's description", result)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	reqs := openai.Requests()
	if len(reqs) != 1 || reqs[0].Model != "gpt-5.2" {
		t.Fatalf("session-model fallback calls = %d (model %q), want one gpt-5.2 call", len(reqs), visionModelOf(reqs))
	}
	if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != visionReasoningEffort {
		t.Fatalf("fallback effort = %#v, want the clamped vision cap %q", reqs[0].ReasoningEffort, visionReasoningEffort)
	}
}

// A session model that cannot take image input must never be handed a
// describe request for an image it cannot see: that produces confident
// hallucinated descriptions. Its side-channel failures stay unavailable.
func TestDescribeImage_NoSessionFallbackWhenSessionModelCannotSeeImages(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("m")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("text-only main outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("text-only session model was asked to describe an image: %d calls", len(openai.Requests()))
	}
}

// The refusal fallback cheapmodel already owns stays as is: the route
// refuses, the session model describes, no extra attempt appears.
func TestDescribeImage_RefusalOfConfiguredRouteFallsBackToSession(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	}}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	if got := sess.describeImage(context.Background(), visionImageResult()); got != "session-described" {
		t.Fatalf("refusal fallback description = %q, want session-described", got)
	}
	if len(anthropic.Requests()) != 1 || len(openai.Requests()) != 1 {
		t.Fatalf("calls = anthropic %d, session %d; want 1 each", len(anthropic.Requests()), len(openai.Requests()))
	}
}

// A text-only session model is never handed the describe request through
// cheapmodel's refusal fallback: the configured route's refusal reports
// unavailable instead of asking a model that cannot see the image — the
// hallucination hazard in #4213.
func TestDescribeImage_TextOnlySessionRefusesToDescribeOnRouteRefusal(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("m")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("text-only refusal outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("refusal fallback handed the image to a text-only session model: %d calls", len(openai.Requests()))
	}
}

// The same refusal path must not hand a PDF to an image-only session model:
// the row declares no pdf input, so a refusing configured route reports
// unavailable instead of describing with a model that cannot see the document.
func TestDescribeImage_PDFRouteRefusalNeverFallsBackToAnImageOnlySessionModel(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf",
	})
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("PDF refusal outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("document refusal fell back to the session model: %d calls", len(openai.Requests()))
	}
}

// The retry is for media the session model is known to take. An image-only
// row never retries a PDF there: the row declares no pdf input, and asking
// blind repeats the hallucination hazard the gate exists to prevent.
func TestDescribeImage_PDFRouteFailureDoesNotRetryOnAnImageOnlySessionModel(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf",
	})
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("PDF route failure outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("PDF retried on the session model: %d calls", len(openai.Requests()))
	}
}

// A refusing configured route falls back to a session model that can see the
// document: the row declares pdf input and the Responses builder represents a
// user-message document as input_file, so the fallback describes rather than
// reporting the route unavailable.
func TestDescribeImage_PDFRouteRefusalFallsBackToADocumentCapableSessionModel(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session pdf description")}, nil
		},
	}}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.4")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "session pdf description" {
		t.Fatalf("PDF refusal fallback result = %+v, want the session model's description", result)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 1 {
		t.Fatalf("session-model calls = %d, want the refusal fallback", len(openai.Requests()))
	}
}

// The raster-image retry has a document counterpart: when the failed route
// attempt never reached the session model and the session row can see the
// document (pdf input declared, Responses input_file), the PDF retries there
// once before vision is declared unavailable.
func TestDescribeImage_PDFRouteFailureRetriesOnADocumentCapableSessionModel(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session pdf description")}, nil
		},
	}}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.4")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "session pdf description" {
		t.Fatalf("PDF retry result = %+v, want the session model's description", result)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 1 {
		t.Fatalf("session-model calls = %d, want the retry", len(openai.Requests()))
	}
}

// A document media type reaches the side-channel normalized: case and
// surrounding whitespace must not reclassify a PDF as a raster image, which
// would send document bytes in an image part and open the retry gate to
// documents.
func TestDescribeImage_NormalizesDocumentMediaTypes(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("a pdf")}, nil
		},
	}}
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: " Application/PDF ", ImagePrompt: "describe",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "a pdf" {
		t.Fatalf("normalized-PDF result = %+v, want the side-channel description", result)
	}
	reqs := openai.Requests()
	if len(reqs) != 1 {
		t.Fatalf("side-channel calls = %d, want 1", len(reqs))
	}
	if media := reqs[0].Messages[0].Content[1]; media.Kind != llm.ContentDocument {
		t.Fatalf("media part for a normalized PDF = %v, want ContentDocument", media.Kind)
	}
}

// When both the configured route and the session model refuse, cheapmodel
// reports ErrAllModelsRefused after its own single session attempt; the
// vision path must not add a third.
func TestDescribeImage_AllModelsRefusedMakesNoExtraSessionAttempt(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("openai")
		},
	}}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("all-refused outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 || len(openai.Requests()) != 1 {
		t.Fatalf("calls = anthropic %d, session %d; want 1 each with no third attempt", len(anthropic.Requests()), len(openai.Requests()))
	}
}

// An unservable configured route is cheapmodel's to reroute onto the session
// model; when that run fails ordinarily, the retry must not re-ask the
// session model cheapmodel already tried.
func TestDescribeImage_UnservableRouteReroutesWithoutRetry(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("openai")
		},
	}}
	// The route's provider does not exist: no override is registered and the
	// registry cannot resolve it, so CanServe fails and cheapmodel reroutes
	// the attempt onto the session model before any wire call.
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "nosuchprov/vision-missing"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("rerouted failure outcome = %v, want provider failure", result.outcome)
	}
	reqs := openai.Requests()
	if len(reqs) != 1 {
		t.Fatalf("session-model calls = %d, want exactly the one rerouted attempt", len(reqs))
	}
}

// A refusal whose session-model fallback then fails ordinarily returns a
// joined error, not ErrAllModelsRefused; the retry must still not re-ask the
// session model that already failed.
func TestDescribeImage_RefusalFallbackFailureDoesNotRetryTheSessionModel(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("openai")
		},
	}}
	anthropic := &fakeErrAdapter{name: "anthropic", steps: []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	}}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("refusal fallback failure outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 || len(openai.Requests()) != 1 {
		t.Fatalf("calls = anthropic %d, session %d; want 1 each with no retry", len(anthropic.Requests()), len(openai.Requests()))
	}
}

// An owned side-channel timeout spends the fixed deadline; the retry must not
// fire a doomed second request on the session model under the dead context.
func TestDescribeImage_OwnedTimeoutDoesNotRetryOnTheSessionModel(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	anthropic := &contextBlockingAdapter{name: "anthropic", started: make(chan struct{}), canceled: make(chan struct{})}
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{
			VisionModel: "anthropic/claude-x",
			testOnly:    testConfig{visionSideChannelTimeout: 20 * time.Millisecond},
		}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelOwnedTimeout {
		t.Fatalf("timeout outcome = %v, want owned timeout", result.outcome)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("timeout retried on the session model: %d calls", len(openai.Requests()))
	}
}

// The tool round mirrors the call-level skip: a session model with native
// tool-result vision reads an image with no steering, no warning, and no
// side-channel call.
func TestPersistImage_NoSteeringWhenSessionModelSeesImagesNatively(t *testing.T) {
	t.Parallel()
	openai := &fakeErrAdapter{name: "openai"}
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2")))

	var kinds []events.EventKind
	done := make(chan struct{})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		kinds = append(kinds, ev.Kind)
	}, func() { close(done) })

	if err := sess.persistToolResults(context.Background(),
		[]llm.ToolCallData{{ID: "c1", Name: "read_file", Arguments: []byte(`{"file_path":"/tmp/a.png"}`)}},
		[]tool.ExecResult{{CallID: "c1", ToolName: "read_file", ImageData: []byte("png"), ImageMediaType: "image/png"}},
	); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	if steered := sess.drainSteering(); len(steered) != 0 {
		t.Fatalf("native-vision round steered %#v, want no image-description or unavailable steering", steered)
	}
	sess.Close()
	<-done
	if slices.Contains(kinds, events.EventWarning) {
		t.Fatalf("native-vision round emitted warnings %v, want none", kinds)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("native-vision round made %d side-channel calls, want 0", len(openai.Requests()))
	}
}

func visionModelOf(reqs []llm.Request) string {
	if len(reqs) == 0 {
		return ""
	}
	return reqs[0].Model
}
