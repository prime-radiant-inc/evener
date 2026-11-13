package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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
	openai := newFakeErrAdapter("openai", nil)
	// The one-registry production shape: the client answers from the same
	// (embedded) registry the profile resolves on.
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("native-vision result = %+v, want success with no description", result)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("side-channel fired %d calls for a session model that sees images natively", len(openai.Requests()))
	}
}

// The skip is per-media and per-protocol: the Responses tool-result shape
// carries no document, so a vision-capable Responses session model still gets
// PDFs described out of band (Anthropic delivers them inline, #4465).
func TestDescribeImage_StillDescribesPDFsWhenSessionModelSeesImages(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	})
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

// Under Jesse's ruling on #6190, when the active primary model sees media inline
// and VisionModel == "", model_fallbacks entries (including text-only rows) do NOT
// trigger preventive side-channel calls.
func TestDescribeImage_FallbackThatCannotSeeImagesDoesNotPreventNativeSkip(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", nil)
	// The session's client must answer from the same (embedded) registry the
	// profile resolves on, the one-registry shape production wires.
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{ModelFallbacks: []string{"openai/gpt-5.2-codex"}}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("native-vision result = %+v, want success with no description despite text-only fallback", result)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("side-channel fired %d calls, want 0 when active primary sees images inline", len(openai.Requests()))
	}
}

// When the active primary model is text-only, the side-channel runs.
func TestDescribeImage_TextOnlyPrimaryModelRunsSideChannel(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	})
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2-codex")))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "session-described" {
		t.Fatalf("text-only primary result = %+v, want side-channel description", result)
	}
	if len(openai.Requests()) != 1 {
		t.Fatalf("side-channel calls = %d, want 1 for text-only primary", len(openai.Requests()))
	}
}

// Fallbacks that all see tool-result images natively preserve the skip.
func TestDescribeImage_FallbacksThatSeeImagesKeepTheNativeSkip(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", nil)
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
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
	openai := newFakeErrAdapter("openai", nil)
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("routed-vision")}, nil
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	})
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", nil)
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session-described")}, nil
		},
	})
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", nil)
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", nil)
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", nil)
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session pdf description")}, nil
		},
	})
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("session pdf description")}, nil
		},
	})
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	})
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

// An unknown fallback does not prevent the native skip when the active primary
// model sees media inline and VisionModel is unset (#6190).
func TestDescribeImage_UncataloguedFallbackDoesNotPreventNativeSkip(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", nil)
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{ModelFallbacks: []string{"openai/gpt-5.2-not-in-catalog"}}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("uncatalogued-fallback result = %+v, want native skip with no description", result)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("side-channel calls = %d, want 0", len(openai.Requests()))
	}
}

// When the active primary model is uncatalogued, it does not qualify for native vision
// and the side-channel runs.
func TestDescribeImage_UncataloguedPrimaryModelRunsSideChannel(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("a png")}, nil
		},
	})
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2-not-in-catalog")))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "a png" {
		t.Fatalf("uncatalogued-primary result = %+v, want side-channel description", result)
	}
	if len(openai.Requests()) != 1 {
		t.Fatalf("side-channel calls = %d, want 1", len(openai.Requests()))
	}
}

// The unservable-route reroute never hands media to a session model that
// cannot see it either: an unroutable configured vision model plus a
// text-only session row reports vision unavailable with no adapter call.
func TestDescribeImage_UnroutableRouteBlindSessionModelReportsUnavailable(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", nil)
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("m")),
		withConfig(SessionConfig{VisionModel: "nosuchprov/vision-missing"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("unroutable-route blind-session outcome = %v, want provider failure", result.outcome)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("blind session received %d describe calls", len(openai.Requests()))
	}
}

// Under Jesse's ruling on #6190, when the active primary model sees media inline
// and VisionModel is unset, fallbacks (even unresolvable ones) do not trigger
// preventive side-channel calls.
func TestDescribeImage_UnresolvableFallbackDoesNotPreventNativeSkip(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", nil)
	client := llm.NewClient()
	client.Register(openai)
	var resolves atomic.Int32
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"ghostprov/ghost-model"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				// The init validation's one call answers; the chain
				// check's call is the one that must not vouch.
				if resolves.Add(1) > 1 {
					return nil, fmt.Errorf("resolver temporarily broken for %s", ref)
				}
				return NewOpenAIProfile("gpt-5.2"), nil
			},
		}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("unresolvable-fallback result = %+v, want native skip with no description", result)
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("side-channel calls = %d, want 0", len(openai.Requests()))
	}
}

// The native-vision capability question is metadata about a model that may
// never serve, so answering it must not resolve the entry the way the round
// runner serves it: full resolution materializes credentials, and a
// command-bearing api_key would spend a mint and prompt the user's password
// manager just to read an image. Facts depth answers with no resolver call
// and no command executed.
func TestDescribeImage_NativeVisionCheckRunsNoCommandCredentials(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "minted")
	instances := map[string]registry.Provider{
		"cmdprov": {
			Surface:       registry.SurfaceOpenAI,
			Protocol:      registry.ProtocolOpenAIResponses,
			APIKey:        "$(touch " + marker + ")",
			DefaultModel:  "cmd-1",
			Transport:     registry.Transport{BaseURL: "http://cmdprov.test.invalid/v1"},
			InheritModels: new(false),
			Models: map[string]registry.Model{
				"cmd-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
			},
		},
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("fixture registry: %v", err)
	}
	sessionProfile, err := provider.Resolve(r, "openai/gpt-5.2")
	if err != nil {
		t.Fatalf("resolve session profile: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(newFakeErrAdapter("openai", nil))
	var resolves atomic.Int32
	sess := newSession(t, withClient(client), withProfile(sessionProfile),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"cmdprov/cmd-1"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				resolves.Add(1)
				return provider.Resolve(r, ref)
			},
		}))
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("init validation did not mint the command credential: %v", statErr)
	}
	os.Remove(marker)

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("capable-fallback result = %+v, want the native skip with no description", result)
	}
	if got := resolves.Load(); got != 1 {
		t.Fatalf("resolver calls after describe = %d, want the init validation's 1 only", got)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the native-vision check materialized the fallback's command credential")
	}
}

// When the active primary model on a gateway sees images inline, fallbacks do not
// trigger side-channel calls (#6190).
func TestDescribeImage_NamespacedFallbackOnAGatewayDoesNotBreakTheNativeSkip(t *testing.T) {
	t.Parallel()
	gw := newFakeErrAdapter("gw", nil)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"gw": {
				Surface:       registry.SurfaceOpenAI,
				Protocol:      registry.ProtocolOpenAIResponses,
				APIKey:        "k",
				DefaultModel:  "vis-1",
				Transport:     registry.Transport{BaseURL: "http://gw.test.invalid/v1"},
				InheritModels: new(false),
				Models: map[string]registry.Model{
					"vis-1":                     {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
					"anthropic/claude-opus-4-5": {Caps: registry.Caps{InputModalities: []string{"text"}}},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("fixture registry: %v", err)
	}
	sessionProfile, err := provider.Resolve(r, "gw/vis-1")
	if err != nil {
		t.Fatalf("resolve session profile: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(gw)
	sess := newSession(t, withClient(client), withProfile(sessionProfile),
		withConfig(SessionConfig{ModelFallbacks: []string{"anthropic/claude-opus-4-5"}}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("namespaced-fallback result = %+v, want native skip with no description", result)
	}
	if len(gw.Requests()) != 0 {
		t.Fatalf("side-channel calls = %d, want 0", len(gw.Requests()))
	}
}

// A profile renamed through withModelID inherits its source row's input
// modalities, and inherited image input is no evidence the named model sees
// media. An ordinary configured-route failure must not retry the image on
// such a session model: the retry would ask an uncatalogued, possibly
// text-only model and could accept a hallucinated description.
func TestDescribeImage_RouteFailureDoesNotRetryMediaOnAnInheritedRowProfile(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", nil)
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("anthropic")
		},
	})
	sess := newSession(t, withAdapter(openai), withAdapter(anthropic),
		withProfile(NewOpenAIProfile("gpt-5.2").WithModel("anthropic/claude-x")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-route-x"}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("route-failure outcome = %v, want provider failure", result.outcome)
	}
	if len(anthropic.Requests()) != 1 {
		t.Fatalf("configured-route calls = %d, want 1", len(anthropic.Requests()))
	}
	if len(openai.Requests()) != 0 {
		t.Fatalf("inherited-capability session model received %d retry calls", len(openai.Requests()))
	}
}

// A disabled row in fallbacks does not prevent native skip when the primary sees images (#6190).
func TestDescribeImage_DisabledGatewayRowFallbackDoesNotBreakTheNativeSkip(t *testing.T) {
	t.Parallel()
	gw := newFakeErrAdapter("gw", nil)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"gw": {
				Surface:       registry.SurfaceOpenAI,
				Protocol:      registry.ProtocolOpenAIResponses,
				APIKey:        "k",
				DefaultModel:  "vis-1",
				Transport:     registry.Transport{BaseURL: "http://gw.test.invalid/v1"},
				InheritModels: new(false),
				Models: map[string]registry.Model{
					"vis-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
					// Image-capable on a delivering protocol, but disabled —
					// serving never uses this row.
					"anthropic/claude-opus-4-5": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}, Disabled: new(true)},
				},
			},
			// The prefix's instance serves the ref through chat completions,
			// which strips tool-result images.
			"anthropic": {
				Surface:       registry.SurfaceOpenAI,
				Protocol:      registry.ProtocolOpenAIChat,
				APIKey:        "k",
				DefaultModel:  "claude-opus-4-5",
				Transport:     registry.Transport{BaseURL: "http://anthropic-gw.test.invalid/v1"},
				InheritModels: new(false),
				Models: map[string]registry.Model{
					"claude-opus-4-5": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("fixture registry: %v", err)
	}
	sessionProfile, err := provider.Resolve(r, "gw/vis-1")
	if err != nil {
		t.Fatalf("resolve session profile: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(gw)
	sess := newSession(t, withClient(client), withProfile(sessionProfile),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"anthropic/claude-opus-4-5"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				// Validation needs a same-surface answer; the disabled row
				// means the runner treats the ref as cross-instance.
				return provider.Resolve(r, "openai/gpt-5.2")
			},
		}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("disabled-gateway-row result = %+v, want native skip with no description", result)
	}
	if len(gw.Requests()) != 0 {
		t.Fatalf("side-channel calls = %d, want 0", len(gw.Requests()))
	}
}

// VisionSetting off, none, and disabled all skip the side-channel even when the primary is text-only.
func TestDescribeImage_VisionSettingOffNoneDisabled(t *testing.T) {
	t.Parallel()
	for _, setting := range []string{"off", "OFF", "none", "NONE", "disabled", "DISABLED"} {
		t.Run(setting, func(t *testing.T) {
			openai := newFakeErrAdapter("openai", nil)
			sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("gpt-5.2-codex")),
				withConfig(SessionConfig{VisionModel: setting}))
			result := sess.describeImageCall(context.Background(), visionImageResult())
			if result.outcome != visionSideChannelSuccess || result.description != "" {
				t.Fatalf("setting %q result = %+v, want success with no description", setting, result)
			}
			if len(openai.Requests()) != 0 {
				t.Fatalf("setting %q fired %d calls, want 0", setting, len(openai.Requests()))
			}
		})
	}
}

// A session whose profile was resolved on a different registry than its
// client's cannot vouch for native vision at all: the profile's facts say
// nothing about what this client dispatches or how the runner classifies a
// fallback. The chain check keeps descriptions flowing for such a session.
func TestDescribeImage_MismatchedRegistriesKeepTheSideChannel(t *testing.T) {
	t.Parallel()
	gwAdapter := newFakeErrAdapter("gw", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("a png")}, nil
		},
	})
	registryFor := func(imageFallback bool) *registry.Registry {
		mods := []string{"text", "image"}
		if !imageFallback {
			mods = []string{"text"}
		}
		r, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"gw": {
					Surface:       registry.SurfaceOpenAI,
					Protocol:      registry.ProtocolOpenAIResponses,
					APIKey:        "k",
					DefaultModel:  "vis-1",
					Transport:     registry.Transport{BaseURL: "http://gw.test.invalid/v1"},
					InheritModels: new(false),
					Models: map[string]registry.Model{
						"vis-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
						"fb-1":  {Caps: registry.Caps{InputModalities: mods}},
					},
				},
			}),
		)
		if err != nil {
			t.Fatalf("fixture registry: %v", err)
		}
		return r
	}
	// The profile's registry vouches for the fallback; the client's registry,
	// the one serving answers from, does not.
	profileRegistry := registryFor(true)
	clientRegistry := registryFor(false)
	sessionProfile, err := provider.Resolve(profileRegistry, "gw/vis-1")
	if err != nil {
		t.Fatalf("resolve session profile: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(clientRegistry))
	client.Register(gwAdapter)
	// A fallback whose row the profile's registry vouches for, and the same
	// registry mismatch, so the guard must break the chain before the
	// fallback's facts are even read.
	sess := newSession(t, withClient(client), withProfile(sessionProfile),
		withConfig(SessionConfig{ModelFallbacks: []string{"fb-1"}}))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "a png" {
		t.Fatalf("client-registry result = %+v, want the side-channel description", result)
	}
	if len(gwAdapter.Requests()) != 1 {
		t.Fatalf("side-channel calls = %d, want 1", len(gwAdapter.Requests()))
	}
}

// The primary alone cannot vouch across a registry mismatch either: a
// Responses-protocol profile from another registry must not skip when this
// client's registry knows the same provider/model as chat completions, which
// strips tool-result images.
func TestDescribeImage_MismatchedRegistryProfileDoesNotSkipForThePrimaryAlone(t *testing.T) {
	t.Parallel()
	gwAdapter := newFakeErrAdapter("gw", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("a png")}, nil
		},
	})
	registryFor := func(protocol string) *registry.Registry {
		r, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"gw": {
					Surface:       registry.SurfaceOpenAI,
					Protocol:      protocol,
					APIKey:        "k",
					DefaultModel:  "vis-1",
					Transport:     registry.Transport{BaseURL: "http://gw.test.invalid/v1"},
					InheritModels: new(false),
					Models: map[string]registry.Model{
						"vis-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
					},
				},
			}),
		)
		if err != nil {
			t.Fatalf("fixture registry: %v", err)
		}
		return r
	}
	// The profile's registry says Responses — the primary sees tool-result
	// images natively; the client's registry says chat completions, which
	// strip them.
	sessionProfile, err := provider.Resolve(registryFor(registry.ProtocolOpenAIResponses), "gw/vis-1")
	if err != nil {
		t.Fatalf("resolve session profile: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(registryFor(registry.ProtocolOpenAIChat)))
	client.Register(gwAdapter)
	sess := newSession(t, withClient(client), withProfile(sessionProfile))

	result := sess.describeImageCall(context.Background(), visionImageResult())
	if result.outcome != visionSideChannelSuccess || result.description != "a png" {
		t.Fatalf("primary-mismatch result = %+v, want the side-channel description", result)
	}
	if len(gwAdapter.Requests()) != 1 {
		t.Fatalf("side-channel calls = %d, want 1", len(gwAdapter.Requests()))
	}
}

// A document media type reaches the side-channel normalized: case and
// surrounding whitespace must not reclassify a PDF as a raster image, which
// would send document bytes in an image part and open the retry gate to
// documents.
func TestDescribeImage_NormalizesDocumentMediaTypes(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("a pdf")}, nil
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("openai")
		},
	})
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("openai")
		},
	})
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
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("openai")
		},
	})
	anthropic := newFakeErrAdapter("anthropic", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteRefusal("anthropic")
		},
	})
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
	openai := newFakeErrAdapter("openai", nil)
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
	openai := newFakeErrAdapter("openai", nil)
	// The one-registry production shape: the client answers from the same
	// (embedded) registry the profile resolves on.
	client := llm.NewClient()
	client.Register(openai)
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")))

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

// visionWarningMessages collects every warning message emitted while f runs.
func visionWarningMessages(sess *Session, f func()) []string {
	var msgs []string
	done := make(chan struct{})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if w, ok := ev.Data.(events.WarningData); ok {
			msgs = append(msgs, w.Message)
		}
	}, func() { close(done) })
	f()
	sess.Close()
	<-done
	return msgs
}

// A typed provider failure stays diagnosable through structured metadata —
// provider, status, code — while the provider's own response text, which can
// echo request or credential material, reaches neither steering (sent to the
// session's own provider) nor the warning.
func TestDescribeImage_ProviderFailureExposesOnlyStructuredCause(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, visionRouteError("openai")
		},
	})
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("m")),
		withConfig(SessionConfig{VisionModel: "openai/vision-m"}))

	var result visionSideChannelResult
	msgs := visionWarningMessages(sess, func() {
		result = sess.describeImageCall(context.Background(), visionImageResult())
	})
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("outcome = %v, want provider failure", result.outcome)
	}
	for _, m := range msgs {
		if strings.Contains(m, "vision route exploded") {
			t.Fatalf("warning message = %q, want the provider's own text kept out", m)
		}
	}
	steering := visionFailureSteering("/tmp/a.png", result)
	if !strings.Contains(steering, "status 500") {
		t.Fatalf("steering = %q, want the structured status", steering)
	}
	if strings.Contains(steering, "vision route exploded") {
		t.Fatalf("steering = %q, want the provider's own text kept out", steering)
	}
}

// A rejection that echoes credential material must not leak it into steering
// (sent to the session's provider) or the warning; the structured status still
// names the cause.
func TestDescribeImage_CredentialEchoStaysOutOfSteeringAndWarning(t *testing.T) {
	t.Parallel()
	const echoed = "sk-live-abc123"
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			raw := map[string]any{"error": map[string]any{"code": "invalid_api_key"}}
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "invalid x-api-key "+echoed, raw, nil)
		},
	})
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("m")),
		withConfig(SessionConfig{VisionModel: "openai/vision-m"}))

	var result visionSideChannelResult
	msgs := visionWarningMessages(sess, func() {
		result = sess.describeImageCall(context.Background(), visionImageResult())
	})
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("outcome = %v, want provider failure", result.outcome)
	}
	steering := visionFailureSteering("/tmp/a.png", result)
	if strings.Contains(steering, echoed) {
		t.Fatalf("steering = %q, want the echoed credential kept out", steering)
	}
	if !strings.Contains(steering, "status 401") {
		t.Fatalf("steering = %q, want the structured status", steering)
	}
	for _, m := range msgs {
		if strings.Contains(m, echoed) {
			t.Fatalf("warning message = %q, want the echoed credential kept out", m)
		}
	}
}

// An untyped error keeps the sanitized shape: request URLs and transport
// details an arbitrary error may carry must not reach the warning.
func TestDescribeImage_UntypedFailureWarningStaysSanitized(t *testing.T) {
	t.Parallel()
	openai := newFakeErrAdapter("openai", []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, errors.New("dial https://request.example/secret failed")
		},
	})
	sess := newSession(t, withAdapter(openai), withProfile(NewOpenAIProfile("m")),
		withConfig(SessionConfig{VisionModel: "openai/vision-m"}))

	var result visionSideChannelResult
	msgs := visionWarningMessages(sess, func() {
		result = sess.describeImageCall(context.Background(), visionImageResult())
	})
	if result.outcome != visionSideChannelProviderFailure {
		t.Fatalf("outcome = %v, want provider failure", result.outcome)
	}
	if steering := visionFailureSteering("/tmp/a.png", result); strings.Contains(steering, "request.example") {
		t.Fatalf("steering = %q, want the untyped error's details kept out", steering)
	}
	if slices.ContainsFunc(msgs, func(m string) bool {
		return strings.Contains(m, "request.example")
	}) {
		t.Fatalf("warning messages = %q, want the untyped error's details kept out", msgs)
	}
}
