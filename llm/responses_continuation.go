package llm

import "strings"

type HistoryMode string

const (
	HistoryModeFullHistory         HistoryMode = "full_history"
	HistoryModeResponsesDelta      HistoryMode = "responses_delta"
	HistoryModeFullHistoryFallback HistoryMode = "full_history_fallback"
	HistoryModeChatFallback        HistoryMode = "chat_completions_fallback"
)

type ResponsesContinuationMode string

const (
	ResponsesContinuationOff  ResponsesContinuationMode = "off"
	ResponsesContinuationAuto ResponsesContinuationMode = "auto"
)

const (
	ResponsesStoragePolicyPublicOpenAIStore   = "public-openai-store"
	ResponsesStoragePolicyPublicOpenAINoStore = "public-openai-no-store"
	ResponsesStoragePolicyCodexUnproven       = "codex-storage-unproven"
)

type ResponsesEndpointFamily string

const (
	ResponsesEndpointFamilyOpenAIPublic ResponsesEndpointFamily = "openai_public"
	ResponsesEndpointFamilyOpenAICodex  ResponsesEndpointFamily = "openai_codex"
)

type ResponsesContinuationSupport struct {
	EndpointFamily        ResponsesEndpointFamily
	StorageShapeProven    bool
	ProductionPathProven  bool
	Enabled               bool
	MaxAnchorAgeSeconds   int64
	StorageShapeProofID   string
	ProductionPathProofID string
}

type ResponsesContinuationDecision struct {
	HistoryMode HistoryMode
	Reason      string
}

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

type AuthScopeIdentity struct {
	Version        string
	AuthSource     string
	CredentialHash string
	AccountHash    string
	WorkspaceHash  string
}

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

type ContinuationStoreOverride struct {
	StoreSetByContinuation           bool
	OriginalStore                    *bool
	ProviderOptionKeysByContinuation []string
	StoragePolicy                    string
}

type ResponsesContinuationPlanInput struct {
	EndpointFamily    ResponsesEndpointFamily
	AuthScopeIdentity AuthScopeIdentity
	OrgIDHash         string
	ProjectIDHash     string
	Request           Request
}

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

func PlanResponsesContinuation(input ResponsesContinuationPlanInput) ResponsesContinuationPlan {
	return ResponsesContinuationPlan{
		EndpointFamily:    input.EndpointFamily,
		AuthScopeIdentity: input.AuthScopeIdentity,
		OrgIDHash:         strings.TrimSpace(input.OrgIDHash),
		ProjectIDHash:     strings.TrimSpace(input.ProjectIDHash),
	}
}

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

func DefaultResponsesContinuationSupportRegistry() map[ResponsesEndpointFamily]ResponsesContinuationSupport {
	return map[ResponsesEndpointFamily]ResponsesContinuationSupport{
		ResponsesEndpointFamilyOpenAIPublic: disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAIPublic),
		ResponsesEndpointFamilyOpenAICodex:  disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAICodex),
	}
}

func ResponsesContinuationSupportFor(registry map[ResponsesEndpointFamily]ResponsesContinuationSupport, family ResponsesEndpointFamily) ResponsesContinuationSupport {
	if support, ok := registry[family]; ok {
		return support
	}
	return disabledResponsesContinuationSupport(family)
}

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
