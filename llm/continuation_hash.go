package llm

import "context"

// StampContinuationIDHash records the redaction-keyed hash of the response id
// for the session's continuation bookkeeping when a hasher is available. The
// dispatching client attaches its own hasher to the context; fallback stands
// in for it, which is what a protocol's process-wide hasher does. A nil
// response, an empty id, or no hasher at all stamps nothing.
func StampContinuationIDHash(ctx context.Context, resp *Response, fallback *ContinuationHasher) {
	hasher := ContinuationHasherFromContext(ctx)
	if hasher == nil {
		hasher = fallback
	}
	if hasher == nil || resp == nil || resp.ID == "" {
		return
	}
	hash, err := hasher.HashContinuationHandle("response_id", resp.ID)
	if err != nil {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["id_hash"] = hash
}
