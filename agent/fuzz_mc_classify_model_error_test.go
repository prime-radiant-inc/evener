//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzMcClassifyModelError drives classifyModelError — the pure decision core
// lifted out of handleModelError — over adversarial (cancellation, error kind,
// content-filter-retry, context-manager) combinations, asserting the
// invariants the effectful wrapper relies on. Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same decision;
//   - the Action is always one of the three enum values (mutually exclusive);
//   - contentFilterRetry only when kind==content-filter && !alreadyRetried && haveContextMgr;
//   - cancel never sets EmitContextLenWarn;
//   - EmitContextLenWarn ⇒ terminal context-length error.
func FuzzMcClassifyModelError(f *testing.F) {
	kinds := []llm.ErrorKind{
		llm.KindUnknown, llm.KindInvalidRequest, llm.KindContextLength,
		llm.KindContentFilter, llm.KindRateLimit, llm.KindServer,
	}
	f.Add(false, uint8(3), false, true)  // content-filter retry path
	f.Add(false, uint8(2), false, true)  // context-length terminal warning
	f.Add(true, uint8(3), false, true)   // cancellation short-circuits
	f.Add(false, uint8(3), true, true)   // content-filter already retried -> terminal
	f.Add(false, uint8(0), false, false) // unknown, no ctx mgr

	f.Fuzz(func(t *testing.T, isCancellation bool, kindSel uint8,
		contentFilterAlreadyRetried, haveContextMgr bool) {

		kind := kinds[int(kindSel)%len(kinds)]

		dec := classifyModelError(isCancellation, kind, contentFilterAlreadyRetried, haveContextMgr)
		if dec2 := classifyModelError(isCancellation, kind, contentFilterAlreadyRetried, haveContextMgr); dec != dec2 {
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

		if dec.Action == modelErrorCancel && dec.EmitContextLenWarn {
			t.Fatalf("cancel must not warn: %+v", dec)
		}

		if dec.EmitContextLenWarn && (dec.Action != modelErrorTerminal || kind != llm.KindContextLength) {
			t.Fatalf("context warning requires terminal context-length error: kind=%v dec=%+v", kind, dec)
		}
	})
}
