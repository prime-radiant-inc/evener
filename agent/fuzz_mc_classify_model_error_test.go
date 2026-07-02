//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzMcClassifyModelError drives classifyModelError — the pure decision core
// lifted out of handleModelError — over adversarial (cancellation, error kind,
// content-filter-retry, context-manager, non-retryable) combinations, asserting the
// invariants the effectful wrapper relies on. Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same decision;
//   - the Action is always one of the three enum values (mutually exclusive);
//   - contentFilterRetry only when kind==content-filter && !alreadyRetried && haveContextMgr;
//   - CloseSession ⇒ Action==terminal && llmErrNonRetryable;
//   - cancel never sets EmitContextLenWarn/CloseSession;
//   - EmitContextLenWarn ⇒ kind==context-length.
func FuzzMcClassifyModelError(f *testing.F) {
	kinds := []llm.ErrorKind{
		llm.KindUnknown, llm.KindInvalidRequest, llm.KindContextLength,
		llm.KindContentFilter, llm.KindRateLimit, llm.KindServer,
	}
	f.Add(false, uint8(3), false, true, false)  // content-filter retry path
	f.Add(false, uint8(2), false, true, true)   // context-length terminal, close
	f.Add(true, uint8(3), false, true, true)    // cancellation short-circuits
	f.Add(false, uint8(3), true, true, false)   // content-filter already retried -> terminal
	f.Add(false, uint8(0), false, false, false) // unknown, no ctx mgr

	f.Fuzz(func(t *testing.T, isCancellation bool, kindSel uint8,
		contentFilterAlreadyRetried, haveContextMgr, llmErrNonRetryable bool) {

		kind := kinds[int(kindSel)%len(kinds)]

		dec := classifyModelError(isCancellation, kind, contentFilterAlreadyRetried, haveContextMgr, llmErrNonRetryable)
		if dec2 := classifyModelError(isCancellation, kind, contentFilterAlreadyRetried, haveContextMgr, llmErrNonRetryable); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}

		switch dec.Action {
		case modelErrorCancel, modelErrorContentFilterRetry, modelErrorTerminal:
		default:
			t.Fatalf("invalid Action %v", dec.Action)
		}

		if dec.Action == modelErrorContentFilterRetry {
			if !(kind == llm.KindContentFilter && !contentFilterAlreadyRetried && haveContextMgr) {
				t.Fatalf("contentFilterRetry under wrong conditions: kind=%v retried=%v ctxMgr=%v",
					kind, contentFilterAlreadyRetried, haveContextMgr)
			}
		}

		if dec.CloseSession && !(dec.Action == modelErrorTerminal && llmErrNonRetryable) {
			t.Fatalf("CloseSession must imply terminal+nonRetryable: %+v (nonRetryable=%v)", dec, llmErrNonRetryable)
		}

		if dec.Action == modelErrorCancel && (dec.EmitContextLenWarn || dec.CloseSession) {
			t.Fatalf("cancel must not warn/close: %+v", dec)
		}

		if dec.EmitContextLenWarn && kind != llm.KindContextLength {
			t.Fatalf("EmitContextLenWarn without context-length kind: %v", kind)
		}
	})
}
