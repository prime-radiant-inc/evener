package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const (
	// visionReasoningEffort deliberately caps image and document descriptions
	// below the session's reasoning effort. This side-channel does perception-
	// shaped work, where inheriting a top-tier effort adds latency without
	// improving the description contract.
	visionReasoningEffort = "low"
)

var errVisionSideChannelTimeout = errors.New("vision side-channel deadline")

func (s *Session) visionSideChannelDuration() time.Duration {
	if timeout := s.cfg.testOnly.visionSideChannelTimeout; timeout > 0 {
		return timeout
	}
	return 0
}

type visionSideChannelOutcome uint8

const (
	visionSideChannelSuccess visionSideChannelOutcome = iota
	visionSideChannelOwnedTimeout
	visionSideChannelParentCanceled
	visionSideChannelProviderFailure
	visionSideChannelUndeliverableDocument
)

type visionSideChannelResult struct {
	description string
	elapsed     time.Duration
	usage       llm.Usage
	outcome     visionSideChannelOutcome
}

// describeImage makes a side-channel API call with no tools to describe an image
// using the model's native vision. Returns the text description, or "" on error.
func (s *Session) describeImage(ctx context.Context, r tool.ExecResult) string {
	return s.describeImageCall(ctx, r).description
}

// visionSideChannelStats is the machine-readable accounting contract carried
// in successful image-description steering. Token fields are present only when
// the provider reported usage; usage_available distinguishes an unavailable
// report from a real report whose counters happen to be zero.
type visionSideChannelStats struct {
	ElapsedMS                int64 `json:"elapsed_ms"`
	UsageAvailable           bool  `json:"usage_available"`
	InputTokens              *int  `json:"input_tokens,omitempty"`
	OutputTokens             *int  `json:"output_tokens,omitempty"`
	ReasoningTokens          *int  `json:"reasoning_tokens,omitempty"`
	ReasoningTokensEstimated *int  `json:"reasoning_tokens_estimated,omitempty"`
	CacheReadTokens          *int  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         *int  `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens       *int  `json:"cache_write_1h_tokens,omitempty"`
}

const (
	visionSideChannelStatsOpen  = "<evener:vision_side_channel_stats>"
	visionSideChannelStatsClose = "</evener:vision_side_channel_stats>"
	// visionStructuralClaims is the shared non-text hazard clause (#486): the
	// vision caveats must cover exact structural claims — piece placement, axis
	// values, wire connections — not rendered text alone.
	visionStructuralClaims = "structural claims (such as piece placement, axis values, or connections)"
	visionRequestContract  = "Observe the image faithfully and answer the caller's request. Vision is non-authoritative for exact text, bytes, or " + visionStructuralClaims + "; use OCR or inspect the source when exactness matters."
	visionConsumerReminder = "Vision output is model-generated and is not byte-exact OCR. It may omit, misread, or silently normalize rendered text, and may misread exact " + visionStructuralClaims + ", even when asked to transcribe them. Do not treat it as authoritative for exact-match, byte-exact transcription, or exact " + visionStructuralClaims + "; use a real OCR tool or inspect the source instead."
)

func visionUnavailableSteering(path string) string {
	if path == "" {
		return "Vision is unavailable. Use OCR or inspect the source data, or continue without vision."
	}
	return fmt.Sprintf("Vision is unavailable for %s. Use OCR or inspect the source data, or continue without vision.", strconv.Quote(path))
}

func visionFailureSteering(path string, result visionSideChannelResult) string {
	if result.outcome == visionSideChannelProviderFailure {
		if path == "" {
			return "Vision is unavailable because the vision provider failed. Use OCR or inspect the source data, or continue without vision."
		}
		return fmt.Sprintf("Vision is unavailable for %s because the vision provider failed. Use OCR or inspect the source data, or continue without vision.", strconv.Quote(path))
	}
	return visionUnavailableSteering(path)
}

func formatVisionSideChannelStats(result visionSideChannelResult) string {
	stats := visionSideChannelStats{
		ElapsedMS:      result.elapsed.Milliseconds(),
		UsageAvailable: visionUsageAvailable(result.usage),
	}
	if stats.UsageAvailable {
		stats.InputTokens = new(result.usage.InputTokens)
		stats.OutputTokens = new(result.usage.OutputTokens)
		stats.ReasoningTokens = result.usage.ReasoningTokens
		stats.ReasoningTokensEstimated = result.usage.ReasoningTokensEstimated
		stats.CacheReadTokens = result.usage.CacheReadTokens
		stats.CacheWriteTokens = result.usage.CacheWriteTokens
		stats.CacheWrite1hTokens = result.usage.CacheWrite1hTokens
	}
	payload, err := json.Marshal(stats)
	if err != nil {
		return ""
	}
	return visionSideChannelStatsOpen + string(payload) + visionSideChannelStatsClose
}

func visionUsageAvailable(usage llm.Usage) bool {
	return usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.TotalTokens != 0 ||
		usage.ReasoningTokens != nil ||
		usage.ReasoningTokensEstimated != nil ||
		usage.CacheReadTokens != nil ||
		usage.CacheWriteTokens != nil ||
		usage.CacheWrite1hTokens != nil
}

// visionModelOff is the reserved bare-word setting that disables the vision
// side-channel. Only a slash-free value can be the sentinel: a value with a
// slash always parses as "provider/model", so a provider named "off" stays
// reachable as "off/some-model".
const visionModelOff = "off"

// resolveVisionRoute maps the session's vision_model setting to the route the
// side-channel executes on. "" resolves to the session's active route, "off"
// (case-insensitive) disables the call, "provider/model" pins a provider, and
// a bare model resolves on the active provider at call time — so it follows
// SetModel switches. A malformed "x/" or "/x" value degrades to a bare-model
// lookup on the active provider rather than an unroutable request.
func resolveVisionRoute(profile *provider.Profile, setting string) (providerName, modelID string, off bool) {
	setting = strings.TrimSpace(setting)
	if setting == "" {
		return profile.ID(), profile.Model(), false
	}
	if strings.EqualFold(setting, visionModelOff) {
		return "", "", true
	}
	if prov, model, ok := strings.Cut(setting, "/"); ok && prov != "" && model != "" {
		return prov, model, false
	}
	return profile.ID(), setting, false
}

// visionRouteReasoning gates reasoning_effort for the vision request and names
// the levels the fixed vision cap clamps against: the session route uses the
// profile's own answers (which already carry the registry's facts); any other
// route asks the registry whether the row takes an effort control at all
// (spec §7.4: a reasoning row without an explicit control list is
// effort-capable, a toggle-only row is not). A route that does not resolve
// gets no effort knob rather than one it may reject.
func (s *Session) visionRouteReasoning(profile *provider.Profile, providerName, modelID string) (bool, []string) {
	if providerName == profile.ID() && modelID == profile.Model() {
		return profile.SupportsReasoning(), profile.ReasoningEffortLevels()
	}
	res, err := s.client.Resolve(providerName + "/" + modelID)
	if err != nil {
		return false, nil
	}
	// The question here is "does this route reason", the same one the session
	// branch above answers — not "may I spell a level". Whether a level reaches
	// the wire is the adapters' call: a toggle-only row needs the effort set so
	// its thinking object still opens, and a budget-shaped row needs it to size
	// a numeric budget, while each adapter withholds the name it cannot send.
	return res.Caps.ReasoningEnabled(), res.Caps.EffortValues
}

// sessionChainSeesToolResultMedia reports whether every model that could
// serve a later round — the session model plus each resolvable
// model_fallbacks entry — sees tool-result bytes of this media type inline.
// A generated description is steering text every serving model can read, so
// skipping it is safe only while every model that might serve can see the
// bytes natively: a text-only or image-stripping fallback inherits the same
// history and would otherwise lose the image twice over.
//
// One registry per session is the invariant every production wiring holds:
// the profile resolves on the client's registry, the cross-provider resolver
// is built from that same client, and delegates and restores inherit both.
// When a session is handed a profile from another registry, its facts say
// nothing about what this client dispatches or how the runner classifies a
// fallback — nothing in the chain can vouch, so descriptions keep flowing.
//
// Fallback capability facts resolve at facts depth
// (Profile.ResolveModelFactsForRef, the runner's own classification against
// the profile's registry): the answer is metadata about a model that
// may never serve, while the full resolution the round runner uses to serve
// an entry materializes credentials — including command-bearing keys that
// would spend a mint and prompt the user's password manager just to read an
// image. An entry whose facts cannot be resolved right now breaks the chain
// instead of passing it: the round runner re-resolves every entry on each
// failing round (its "could not be resolved; skipping it" warning is
// per-attempt), so today's error says nothing about whether the entry serves
// later — and an image-stripping fallback that recovers must find the
// description still flowing.
func (s *Session) sessionChainSeesToolResultMedia(profile *provider.Profile, fallbacks []string, mt string) bool {
	if profile.Registry() != s.client.Registry() {
		return false
	}
	if !profile.SeesToolResultMedia(mt) {
		return false
	}
	for _, ref := range fallbacks {
		res, err := profile.ResolveModelFactsForRef(ref)
		if err != nil || !llm.RowSeesToolResultMedia(res, mt) {
			return false
		}
	}
	return true
}

// sessionModelTakesRequestMedia reports whether the session profile can see a
// describe-request's media part: a raster image needs image input (a
// user-message image is the one shape every protocol vouches for), a document
// needs the row's pdf input plus an adapter that represents a user-message
// document. Any other kind stays with the configured route's verdict.
func sessionModelTakesRequestMedia(part llm.ContentPart, profile *provider.Profile) bool {
	switch part.Kind {
	case llm.ContentImage:
		return profile.AcceptsImageInput()
	case llm.ContentDocument:
		return profile.AcceptsDocumentInput(part.Document)
	default:
		return false
	}
}

// visionRouteDeliversDocuments reports whether the route the side-channel
// executes on can represent this user-message document part: the protocol's
// adapter must write the payload in a form the wire carries
// (llm.DeliversUserMessageDocuments — inline bytes ride the Responses,
// Anthropic, Google and Chat builders; a URL rides the first three, and chat
// has no URL file mechanism). Chat is held to one more test: its file part
// is a gateway extension rather than a wire contract, so only a row that
// declares pdf input vouches that this endpoint speaks it. A route that does
// not resolve is allowed through, mirroring visionRouteReasoning's
// degrade-to-send: the provider answers for itself, and reporting vision
// unavailable over a resolution failure would bury a config problem in a
// per-file steering message.
func (s *Session) visionRouteDeliversDocuments(profile *provider.Profile, providerName, modelID string, doc *llm.DocumentData) bool {
	var res registry.Resolved
	if providerName == profile.ID() && modelID == profile.Model() {
		res = profile.Resolved()
	} else {
		resolved, err := s.client.Resolve(providerName + "/" + modelID)
		if err != nil {
			return true
		}
		res = resolved
	}
	if !llm.DeliversUserMessageDocuments(res, doc) {
		return false
	}
	if res.Protocol == registry.ProtocolOpenAIChat {
		return res.Caps.AcceptsModality("pdf")
	}
	return true
}

// applyVisionRouteEffort clamps the fixed low vision cap to the route's
// supported effort levels. A model whose cheapest level is above the cap gets
// that level rather than a value it would reject, and a route that takes no
// effort control gets none on the wire.
func (s *Session) applyVisionRouteEffort(req *llm.Request, profile *provider.Profile, providerName, modelID string) {
	if supportsReasoning, levels := s.visionRouteReasoning(profile, providerName, modelID); supportsReasoning {
		effort := llm.ClampReasoningEffort(visionReasoningEffort, levels)
		req.ReasoningEffort = &effort
	} else {
		req.ReasoningEffort = nil
	}
}

func (s *Session) describeImageCall(ctx context.Context, r tool.ExecResult) visionSideChannelResult {
	if len(r.ImageData) == 0 {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}
	// Skip for explorer agents — they're just inventorying files, not analyzing images.
	if s.cfg.AgentName == "explorer" {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}

	// Snapshot the profile under s.mu: the vision side-channel runs during the
	// round, so a concurrent SetModel (which mutates it under s.mu) must not race
	// this read (PRI-1958 A2/A4). The fixed low vision cap below is deliberately
	// independent of the session/task reasoning effort.
	s.mu.Lock()
	profile := s.profile
	visionSetting := s.cfg.VisionModel
	modelFallbacks := slices.Clone(s.cfg.ModelFallbacks)
	s.mu.Unlock()
	visionSetting = strings.TrimSpace(visionSetting)

	routeProvider, routeModel, visionOff := resolveVisionRoute(profile, visionSetting)
	if visionOff {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}

	mt := strings.ToLower(strings.TrimSpace(r.ImageMediaType))
	if mt == "" {
		mt = "image/png"
	}

	// Native vision replaces the side-channel when every model that could
	// serve a later round sees this result's bytes inline
	// (sessionChainSeesToolResultMedia: image input on a row whose adapter
	// delivers that media, checked across the session model and its
	// fallbacks): a describing call would only duplicate bytes the serving
	// model already sees, and its failures would call native vision
	// unavailable. Documents never qualify — no adapter vouches for a PDF
	// tool result — so they always take the side-channel. An explicitly
	// configured vision model is a deliberate instruction to describe with
	// that model and stays honored.
	if visionSetting == "" && s.sessionChainSeesToolResultMedia(profile, modelFallbacks, mt) {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}

	// Use the caller's vision_prompt as the vision prompt. The calling LLM
	// knows what it needs — we just ask the vision model to answer that question
	// under one unconditional observation contract.
	prompt := visionPrompt(r.ImagePrompt)

	// Build the content part based on media type — images use ContentImage,
	// documents (PDFs) use ContentDocument so the provider sends them correctly.
	var mediaPart llm.ContentPart
	if strings.HasPrefix(mt, "application/pdf") {
		mediaPart = llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{
			Data:      r.ImageData,
			MediaType: mt,
			FileName:  "document.pdf",
		}}
	} else {
		mediaPart = llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{
			Data:      r.ImageData,
			MediaType: mt,
		}}
	}

	// A document whose route cannot represent it never reaches that route:
	// the route's row must claim pdf input and its wire must carry the
	// payload (visionRouteDeliversDocuments), or the model would be asked to
	// describe bytes it never saw — the provider answers an error the round
	// reports as a vision failure (#5472), or worse, a confident description
	// of a document it was not given (#4213). When the session model can
	// carry the document — the same test the retry leg below applies — route
	// there instead, exactly as that leg does after a failed attempt. Only
	// when neither route can represent the document is it undeliverable.
	if mediaPart.Kind == llm.ContentDocument && !s.visionRouteDeliversDocuments(profile, routeProvider, routeModel, mediaPart.Document) {
		if !sessionModelTakesRequestMedia(mediaPart, profile) {
			return visionSideChannelResult{outcome: visionSideChannelUndeliverableDocument}
		}
		routeProvider, routeModel = profile.ID(), profile.Model()
	}

	visionTimeout := s.visionSideChannelDuration()
	var visionCtx context.Context
	var cancel context.CancelFunc
	if visionTimeout > 0 {
		visionCtx, cancel = context.WithTimeoutCause(ctx, visionTimeout, errVisionSideChannelTimeout)
	} else {
		visionCtx, cancel = context.WithCancel(ctx)
	}
	idleTimeout, _ := ParseProviderIdleTimeout(s.cfg.ProviderIdleTimeout)
	defer cancel()
	req := llm.Request{
		Model:    profile.Model(),
		Provider: profile.ID(),
		Messages: []llm.Message{
			{
				Role: llm.RoleUser,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: prompt},
					mediaPart,
				},
			},
		},
		// No tools — force text-only response.
		AdapterTimeout: &llm.AdapterTimeout{
			Connect:    10 * time.Second,
			Request:    visionTimeout,
			StreamRead: idleTimeout,
		},
	}
	// This request is built manually (not via buildModelRequest), so clamp the
	// fixed vision cap to the route's supported levels here too.
	s.applyVisionRouteEffort(&req, profile, routeProvider, routeModel)
	s.applyModelRequestMetadata(&req)

	start := s.sclock().Now()
	resp, ranSessionModel, err := s.cheap.CompleteRouted(visionCtx, profile, routeProvider, routeModel, req)
	if err != nil && !ranSessionModel && visionCtx.Err() == nil && sessionModelTakesRequestMedia(mediaPart, profile) {
		// The failed attempt never touched the session model — cheapmodel
		// reroutes unservable routes and its own refusal fallback there, and
		// both report it — the fixed deadline is not spent, and the session
		// model can see the media (a raster image needs image input, the one
		// user-message shape every protocol vouches for; a document needs the
		// row's pdf input plus an adapter that represents a user-message
		// document, which llm.DeliversUserMessageDocuments vouches for): try
		// the description once on it before declaring vision unavailable. The
		// effort must fit the session model's ladder, not the failed route's.
		// A model that cannot see the media never gets the doomed request:
		// providers reject it, and the ones that do not answer with confident
		// hallucinated descriptions.
		s.applyVisionRouteEffort(&req, profile, profile.ID(), profile.Model())
		resp, _, err = s.cheap.CompleteRouted(visionCtx, profile, profile.ID(), profile.Model(), req)
	}
	elapsed := s.sclock().Now().Sub(start)
	elapsed = max(elapsed, 0)
	if err != nil {
		outcome := visionSideChannelProviderFailure
		cause := context.Cause(visionCtx)
		if errors.Is(cause, errVisionSideChannelTimeout) {
			outcome = visionSideChannelOwnedTimeout
		} else if ctx.Err() != nil {
			// Parent cancellation owns races with the side-channel deadline. This
			// prevents a stale unavailable steering message after a canceled turn.
			outcome = visionSideChannelParentCanceled
		} else if errors.Is(visionCtx.Err(), context.DeadlineExceeded) {
			outcome = visionSideChannelOwnedTimeout
		}
		if outcome != visionSideChannelParentCanceled {
			// Provider errors can contain request URLs, bodies, IDs, or credentials.
			// The user-facing steering below is deliberately sanitized, so keep the
			// warning sanitized too rather than reintroducing the raw adapter error.
			s.emit(events.WarningData{Message: "vision side-channel unavailable"})
		}
		return visionSideChannelResult{elapsed: elapsed, outcome: outcome}
	}

	return visionSideChannelResult{
		description: strings.TrimSpace(resp.Message.Text()),
		elapsed:     elapsed,
		usage:       resp.Usage,
		outcome:     visionSideChannelSuccess,
	}
}

func visionPrompt(intent string) string {
	intent = strings.TrimSpace(intent)
	if intent == "" {
		intent = "Describe what you see in this image in thorough detail."
	}
	return intent + "\n\n" + visionRequestContract
}
