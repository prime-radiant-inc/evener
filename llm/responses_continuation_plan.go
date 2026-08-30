package llm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

const responsesRequestFingerprintPrefix = "cont-req-v2:"

// ResponsesEndpointFamilyFor is spec §7.6: openai_codex on the Codex
// transport, openai_public everywhere else.
func ResponsesEndpointFamilyFor(res registry.Resolved) ResponsesEndpointFamily {
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		return ResponsesEndpointFamilyOpenAICodex
	}
	return ResponsesEndpointFamilyOpenAIPublic
}

// ResponsesRequestFingerprint hashes a built Responses body minus the
// fields that differ between a continuation request and its full-history
// twin (spec §7.6): input, previous_response_id, conversation, stream, and
// on the public family store. The v2 prefix marks the cut-over: v1 was
// computed from the pre-registry builder.
func ResponsesRequestFingerprint(family ResponsesEndpointFamily, body map[string]any) (string, error) {
	excluded := excludedFingerprintFields(family)
	filtered := make(map[string]any, len(body))
	for k, v := range body {
		if excluded[k] {
			continue
		}
		filtered[k] = v
	}

	b, err := json.Marshal(filtered)
	if err != nil {
		return "", fmt.Errorf("marshal request fingerprint body: %w", err)
	}
	sum := sha256.Sum256(b)
	return responsesRequestFingerprintPrefix + base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// excludedFingerprintFields are the body fields a continuation is allowed to
// vary without changing the fingerprint: the input and its anchors always,
// and store only on the public API (spec §7.6's storage-policy split).
func excludedFingerprintFields(family ResponsesEndpointFamily) map[string]bool {
	excluded := map[string]bool{
		"input":                true,
		"previous_response_id": true,
		"conversation":         true,
		"stream":               true,
	}
	if family == ResponsesEndpointFamilyOpenAIPublic {
		excluded["store"] = true
	}
	return excluded
}

// AuthScopeProvider is implemented by an authenticator whose credential
// carries an identity beyond the key value (the Codex transport): the
// claims the continuation storage scope hashes (spec §7.6).
type AuthScopeProvider interface {
	AuthScope(ctx context.Context, res registry.Resolved) (accountID, workspaceID string, err error)
}

// responsesStoragePolicy labels the built body's storage behaviour: Codex is
// unproven, public with store = true is storable, else not.
func responsesStoragePolicy(family ResponsesEndpointFamily, body map[string]any) (string, bool) {
	if family == ResponsesEndpointFamilyOpenAICodex {
		return ResponsesStoragePolicyCodexUnproven, false
	}
	if store, _ := body["store"].(bool); store {
		return ResponsesStoragePolicyPublicOpenAIStore, true
	}
	return ResponsesStoragePolicyPublicOpenAINoStore, false
}

// continuationAvailable is spec §7.6's gate: the openai-responses protocol
// with previous_response_id and store both sendable after layering.
func continuationAvailable(res registry.Resolved) bool {
	return res.Protocol == registry.ProtocolOpenAIResponses && res.Caps.Fields["previous_response_id"] && res.Caps.Fields["store"]
}

// hashIfSet hashes a scope value, leaving an absent one absent rather than
// hashing the empty string.
func hashIfSet(h *ContinuationHasher, kind, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return h.HashContinuationScopeValue(kind, value)
}

// planContinuation computes the plan from Resolved and the built body
// (spec §7.6). Unavailable endpoints get a plan with the family and no
// storage permission, so the session falls back to full history.
func (c *Client) planContinuation(ctx context.Context, req Request, res registry.Resolved, p Protocol) (ResponsesContinuationPlan, error) {
	family := ResponsesEndpointFamilyFor(res)
	plan := ResponsesContinuationPlan{EndpointFamily: family}
	if family == ResponsesEndpointFamilyOpenAICodex {
		plan.StoragePolicyLabel = ResponsesStoragePolicyCodexUnproven
	}
	if !continuationAvailable(res) {
		return plan, nil
	}
	hasher, err := c.ContinuationHasher()
	if err != nil {
		return plan, err
	}
	body, err := p.BuildBody(req, res)
	if err != nil {
		return plan, err
	}
	fingerprint, err := ResponsesRequestFingerprint(family, body)
	if err != nil {
		return plan, err
	}
	policy, allowed := responsesStoragePolicy(family, body)

	authSource, credentialValue := "api_key", res.Credential.Source+"\x00"+res.Credential.Value
	account, workspace := "", ""
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		authSource = "oauth"
		if a, ok := AuthenticatorFor(res.Transport.Auth); ok {
			if sp, ok := a.(AuthScopeProvider); ok {
				if account, workspace, err = sp.AuthScope(ctx, res); err != nil {
					return plan, err
				}
			}
		}
		credentialValue = "oauth:" + strings.TrimSpace(account) + ":" + strings.TrimSpace(workspace)
	}
	credentialHash, err := hasher.HashContinuationScopeValue("credential", credentialValue)
	if err != nil {
		return plan, err
	}
	hashes := map[string]string{}
	for kind, value := range map[string]string{"account": account, "workspace": workspace, "org_id": res.Headers["OpenAI-Organization"], "project_id": res.Headers["OpenAI-Project"], "conversation_id": req.ConversationID} {
		if hashes[kind], err = hashIfSet(hasher, kind, value); err != nil {
			return plan, err
		}
	}
	scope := ContinuationStorageScope{
		HashVersion: ContinuationScopeHashVersion, Provider: res.Instance, EndpointFamily: string(family),
		BaseURL: strings.TrimRight(strings.TrimSpace(res.Transport.BaseURL), "/"), Path: res.Transport.Endpoint,
		AuthSource: authSource, OrgIDHash: hashes["org_id"], ProjectIDHash: hashes["project_id"],
		AccountHash: hashes["account"], WorkspaceHash: hashes["workspace"], CredentialHash: credentialHash,
		ConversationIDHash: hashes["conversation_id"], StoragePolicy: policy,
	}
	if scope.Fingerprint, err = hasher.HashContinuationStorageScope(scope); err != nil {
		return plan, err
	}
	plan.AuthScopeIdentity = AuthScopeIdentity{Version: ContinuationScopeHashVersion, AuthSource: authSource, CredentialHash: credentialHash, AccountHash: hashes["account"], WorkspaceHash: hashes["workspace"]}
	plan.OrgIDHash, plan.ProjectIDHash = hashes["org_id"], hashes["project_id"]
	plan.RequestFingerprint = fingerprint
	plan.StorageScope, plan.StorageScopeFingerprint = scope, scope.Fingerprint
	plan.StoragePolicyLabel, plan.ContinuationStorageAllowed = policy, allowed
	return plan, nil
}
