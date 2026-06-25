package llm

import "strings"

// HistoryMode selects how prior turns are sent to the model on a request.
type HistoryMode string

const (
	// HistoryModeFullHistory sends the complete prior conversation on every request.
	HistoryModeFullHistory HistoryMode = "full_history"
	// HistoryModeResponsesDelta sends only the delta since an anchored prior
	// response, relying on server-side continuation.
	HistoryModeResponsesDelta HistoryMode = "responses_delta"
	// HistoryModeFullHistoryFallback sends full history after a continuation
	// attempt was abandoned.
	HistoryModeFullHistoryFallback HistoryMode = "full_history_fallback"
	// HistoryModeChatFallback sends history via the chat-completions shape as a fallback.
	HistoryModeChatFallback HistoryMode = "chat_completions_fallback"
)

// ResponsesContinuationMode is the configured policy for using server-side
// responses continuation.
type ResponsesContinuationMode string

const (
	// ResponsesContinuationOff disables responses continuation; requests always use full history.
	ResponsesContinuationOff ResponsesContinuationMode = "off"
	// ResponsesContinuationAuto enables continuation when the endpoint family supports it.
	ResponsesContinuationAuto ResponsesContinuationMode = "auto"
)

// ResponsesErrorClass categorizes a Responses-API error to drive retry and
// fallback handling.
type ResponsesErrorClass string

const (
	// ResponsesErrorContinuationRejected: the server rejected the continuation
	// anchor (e.g. an unknown or expired previous response).
	ResponsesErrorContinuationRejected ResponsesErrorClass = "continuation_rejected"
	// ResponsesErrorModelEndpoint: the model or endpoint is unavailable or misconfigured.
	ResponsesErrorModelEndpoint ResponsesErrorClass = "model_endpoint"
	// ResponsesErrorTransient: a temporary error that is safe to retry.
	ResponsesErrorTransient ResponsesErrorClass = "transient"
	// ResponsesErrorPermanentOther: a permanent error not covered by the other classes.
	ResponsesErrorPermanentOther ResponsesErrorClass = "permanent_other"
)

const (
	// ResponsesStoragePolicyPublicOpenAIStore: public OpenAI endpoint with
	// server-side storage (store=true) enabled.
	ResponsesStoragePolicyPublicOpenAIStore = "public-openai-store"
	// ResponsesStoragePolicyPublicOpenAINoStore: public OpenAI endpoint with
	// server-side storage disabled.
	ResponsesStoragePolicyPublicOpenAINoStore = "public-openai-no-store"
	// ResponsesStoragePolicyCodexUnproven: Codex endpoint whose storage behavior
	// is not yet proven.
	ResponsesStoragePolicyCodexUnproven = "codex-storage-unproven"
)

// ResponsesEndpointFamily identifies which Responses-API endpoint a provider
// call targets.
type ResponsesEndpointFamily string

const (
	// ResponsesEndpointFamilyOpenAIPublic is the public OpenAI /v1/responses endpoint.
	ResponsesEndpointFamilyOpenAIPublic ResponsesEndpointFamily = "openai_public"
	// ResponsesEndpointFamilyOpenAICodex is the ChatGPT/Codex backend responses endpoint.
	ResponsesEndpointFamilyOpenAICodex ResponsesEndpointFamily = "openai_codex"
)

// ResponsesContinuationSupport describes whether an endpoint family supports
// responses continuation, including whether the storage shape and production
// path are proven, the enable flag, and the max anchor age.
type ResponsesContinuationSupport struct {
	EndpointFamily        ResponsesEndpointFamily
	StorageShapeProven    bool
	ProductionPathProven  bool
	Enabled               bool
	MaxAnchorAgeSeconds   int64
	StorageShapeProofID   string
	ProductionPathProofID string
}

// ResponsesContinuationDecision is the chosen HistoryMode for a request along
// with a machine-readable reason string.
type ResponsesContinuationDecision struct {
	HistoryMode HistoryMode
	Reason      string
}

// ContinuationMetadata carries the redacted continuation handles, anchor/delta
// counts, endpoint, fingerprints, and storage-policy labels recorded for a
// continuation request.
type ContinuationMetadata struct {
	PreviousResponseIDHash  string
	ConversationIDHash      string
	AnchorTurnIndex         int
	DeltaTurnCount          int
	DeltaTurnKinds          []string
	EndpointFamily          string
	RequestFingerprint      string
	ContextMarker           string
	StoragePolicyLabel      string
	StorageScopeFingerprint string
	ChatFallbackHistoryLen  int
}

// AuthScopeIdentity identifies the authenticated scope (auth source plus hashed
// credential, account, and workspace) that a continuation anchor is bound to.
type AuthScopeIdentity struct {
	Version        string
	AuthSource     string
	CredentialHash string
	AccountHash    string
	WorkspaceHash  string
}

// ContinuationStorageScope is the set of provider, endpoint, auth, and hashed
// identity fields that define the storage boundary within which a continuation
// anchor is valid, plus its computed fingerprint.
type ContinuationStorageScope struct {
	Fingerprint        string
	HashVersion        string
	Provider           string
	EndpointFamily     string
	BaseURL            string
	Path               string
	AuthSource         string
	OrgIDHash          string
	ProjectIDHash      string
	AccountHash        string
	WorkspaceHash      string
	CredentialHash     string
	ConversationIDHash string
	StoragePolicy      string
}

// ContinuationStoreOverride records whether continuation forced the request's
// store flag on, the original store value, any provider-option keys it set, and
// the storage policy, so the change can be reverted.
type ContinuationStoreOverride struct {
	StoreSetByContinuation           bool
	OriginalStore                    *bool
	ProviderOptionKeysByContinuation []string
	StoragePolicy                    string
}

// ResponsesContinuationPlanInput is the endpoint family, auth scope identity,
// hashed org/project IDs, and request used to build a ResponsesContinuationPlan.
type ResponsesContinuationPlanInput struct {
	EndpointFamily    ResponsesEndpointFamily
	AuthScopeIdentity AuthScopeIdentity
	OrgIDHash         string
	ProjectIDHash     string
	Request           Request
}

// ResponsesContinuationPlan is the resolved continuation context for a request:
// endpoint family, auth scope, fingerprints, storage scope and policy, and
// whether continuation storage and chat fallback are allowed.
type ResponsesContinuationPlan struct {
	EndpointFamily             ResponsesEndpointFamily
	AuthScopeIdentity          AuthScopeIdentity
	OrgIDHash                  string
	ProjectIDHash              string
	RequestFingerprint         string
	StorageScope               ContinuationStorageScope
	StorageScopeFingerprint    string
	StoragePolicyLabel         string
	ContinuationStorageAllowed bool
	CanFallbackToChat          bool
}

// PlanResponsesContinuation builds a ResponsesContinuationPlan from the input,
// carrying through the endpoint family, auth scope identity, and trimmed
// org/project ID hashes. It does not yet compute the request fingerprint,
// storage scope, or storage/fallback decisions (those plan fields are left zero).
func PlanResponsesContinuation(input ResponsesContinuationPlanInput) ResponsesContinuationPlan {
	return ResponsesContinuationPlan{
		EndpointFamily:    input.EndpointFamily,
		AuthScopeIdentity: input.AuthScopeIdentity,
		OrgIDHash:         strings.TrimSpace(input.OrgIDHash),
		ProjectIDHash:     strings.TrimSpace(input.ProjectIDHash),
	}
}

// ApplyResponsesContinuationStoreOverride returns a cloned request and an
// override record; under the public-OpenAI-store policy it forces Store=true
// (capturing the original) unless it was already true, and otherwise leaves the
// request unchanged.
func ApplyResponsesContinuationStoreOverride(req Request, policy string) (Request, ContinuationStoreOverride) {
	owned := cloneRequestForContinuationStore(req)
	override := ContinuationStoreOverride{StoragePolicy: policy}
	if policy != ResponsesStoragePolicyPublicOpenAIStore {
		return owned, override
	}
	if req.Store != nil && *req.Store {
		return owned, override
	}
	override.StoreSetByContinuation = true
	if req.Store != nil {
		original := *req.Store
		override.OriginalStore = &original
	}
	store := true
	owned.Store = &store
	return owned, override
}

// ClearResponsesContinuationStoreOverride returns a cloned request with the
// Store flag restored to its pre-override value (nil if originally unset),
// reverting any override applied by ApplyResponsesContinuationStoreOverride.
func ClearResponsesContinuationStoreOverride(req Request, override ContinuationStoreOverride) Request {
	cleared := cloneRequestForContinuationStore(req)
	if !override.StoreSetByContinuation {
		return cleared
	}
	if override.OriginalStore == nil {
		cleared.Store = nil
		return cleared
	}
	original := *override.OriginalStore
	cleared.Store = &original
	return cleared
}

// DefaultResponsesContinuationSupportRegistry returns the built-in support
// table: public OpenAI enabled with a 3600s max anchor age, and Codex disabled.
func DefaultResponsesContinuationSupportRegistry() map[ResponsesEndpointFamily]ResponsesContinuationSupport {
	return map[ResponsesEndpointFamily]ResponsesContinuationSupport{
		ResponsesEndpointFamilyOpenAIPublic: {
			EndpointFamily:        ResponsesEndpointFamilyOpenAIPublic,
			StorageShapeProven:    true,
			ProductionPathProven:  true,
			Enabled:               true,
			MaxAnchorAgeSeconds:   3600,
			StorageShapeProofID:   "2026-06-24-responses-continuation-phase-0b",
			ProductionPathProofID: "2026-06-24-responses-continuation-phase-12a-public",
		},
		ResponsesEndpointFamilyOpenAICodex: disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAICodex),
	}
}

// ResponsesContinuationSupportFor returns the support entry for family from the
// registry, or a disabled entry when the family is absent.
func ResponsesContinuationSupportFor(registry map[ResponsesEndpointFamily]ResponsesContinuationSupport, family ResponsesEndpointFamily) ResponsesContinuationSupport {
	if support, ok := registry[family]; ok {
		return support
	}
	return disabledResponsesContinuationSupport(family)
}

// DecideResponsesContinuation chooses the HistoryMode from the mode and
// support: responses-delta only when mode is auto, support is enabled, and the
// max anchor age is positive; otherwise full history with an explanatory reason.
func DecideResponsesContinuation(mode ResponsesContinuationMode, support ResponsesContinuationSupport) ResponsesContinuationDecision {
	if mode != ResponsesContinuationAuto {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_off",
		}
	}
	if !support.Enabled {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_endpoint_not_enabled",
		}
	}
	if support.MaxAnchorAgeSeconds <= 0 {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_anchor_age_unbounded",
		}
	}
	return ResponsesContinuationDecision{
		HistoryMode: HistoryModeResponsesDelta,
		Reason:      "continuation_enabled",
	}
}

// DecideResponsesContinuationForRequest applies DecideResponsesContinuation and
// then downgrades to full history when the request already carries a ConversationID.
func DecideResponsesContinuationForRequest(mode ResponsesContinuationMode, support ResponsesContinuationSupport, req Request) ResponsesContinuationDecision {
	decision := DecideResponsesContinuation(mode, support)
	if decision.HistoryMode != HistoryModeResponsesDelta {
		return decision
	}
	if strings.TrimSpace(req.ConversationID) != "" {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_conversation_id_present",
		}
	}
	return decision
}

func disabledResponsesContinuationSupport(family ResponsesEndpointFamily) ResponsesContinuationSupport {
	return ResponsesContinuationSupport{EndpointFamily: family}
}

func cloneRequestForContinuationStore(req Request) Request {
	out := req
	if req.Store != nil {
		store := *req.Store
		out.Store = &store
	}
	out.ProviderOptions = cloneProviderOptions(req.ProviderOptions)
	return out
}

func cloneProviderOptions(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneProviderOptionValue(v)
	}
	return out
}

func cloneProviderOptionValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, item := range typed {
			out[k] = cloneProviderOptionValue(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for k, item := range typed {
			out[k] = item
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneProviderOptionValue(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	default:
		return v
	}
}
