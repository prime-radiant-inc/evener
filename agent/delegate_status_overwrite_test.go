package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestStableSendFailurePreservesDurableStatus is a source-level red test for
// issue #184: "Stopped delegate's delegate_send status reports
// cancelled/failed, never the durable outcome stopped".
//
// In stableSendFailureOutcomeAfterDispatch (agent/delegate_runtime.go), the
// durable outcome from stableDelegateFailedSendResult sets result.Status from
// snapshot.lastOutcome.Status (e.g. StatusStopped for an externally stopped
// delegate). The subsequent populateStableDelegateSendResult(&result,
// *resolution.packet) call overwrites result.Status from the terminal packet's
// outcome, which for a stopped delegate may report OutcomeCancelled and thus
// clobber StatusStopped -> StatusCancelled.
//
// The existing save/restore at the call site only preserves the durable REASON:
//
//	durableReason := result.Reason
//	populateStableDelegateSendResult(&result, *resolution.packet)
//	if durableReason != "" {
//		result.Reason = durableReason
//	}
//
// The STATUS is never saved or restored, so the durable status is lost. This
// test asserts the function body saves the durable STATUS before the
// populateStableDelegateSendResult call and restores it afterward, the same way
// it already does for the durable REASON. It fails RED today (only durableReason
// is saved) and will pass GREEN once the fix adds the durableStatus save/restore.
func TestStableSendFailurePreservesDurableStatus(t *testing.T) {
	body := extractFuncBodyByName(t, "delegate_runtime.go", "stableSendFailureOutcomeAfterDispatch")
	if body == "" {
		t.Fatal("could not locate stableSendFailureOutcomeAfterDispatch function body")
	}

	// The populate call that overwrites result.Status from the packet.
	const populateCall = "populateStableDelegateSendResult("
	populateIdx := strings.Index(body, populateCall)
	if populateIdx < 0 {
		t.Fatalf("stableSendFailureOutcomeAfterDispatch does not call %s;\nbody:\n%s",
			strings.TrimRight(populateCall, "("), body)
	}
	beforePopulate := body[:populateIdx]
	afterPopulate := body[populateIdx+len(populateCall):]

	// The fix must save the durable STATUS before the populate call, mirroring the
	// existing `durableReason := result.Reason` save. Accept any form that
	// assigns result.Status into an identifier containing "durableStatus"
	// (a `:=` short decl, an `=` assignment, or a multi-value tuple such as
	// `durableStatus, durableReason := result.Status, result.Reason`).
	if !savesDurableStatus(beforePopulate) {
		t.Fatalf(`issue #184: stableSendFailureOutcomeAfterDispatch saves the durable
REASON (durableReason := result.Reason) before calling populateStableDelegateSendResult,
but does NOT save the durable STATUS. populateStableDelegateSendResult then overwrites
result.Status from the terminal packet's outcome, so an externally STOPPED delegate
(lastOutcome.Status == OutcomeStopped -> result.Status == StatusStopped) has its status
clobbered to StatusCancelled when the packet reports OutcomeCancelled. The durable
status is lost and delegate_send reports cancelled/failed instead of stopped.

Fix: save result.Status before populateStableDelegateSendResult and restore it after,
the same way durableReason is already preserved, e.g.:

	durableStatus := result.Status
	durableReason := result.Reason
	populateStableDelegateSendResult(&result, *resolution.packet)
	if durableReason != "" {
		result.Reason = durableReason
	}
	if durableStatus != "" {
		result.Status = durableStatus
	}

Function body before populateStableDelegateSendResult:
%s`, beforePopulate)
	}

	// The fix must restore the durable STATUS after the populate call.
	if !strings.Contains(afterPopulate, "result.Status = durableStatus") {
		t.Fatalf(`issue #184: stableSendFailureOutcomeAfterDispatch saves the durable
STATUS before populateStableDelegateSendResult but does NOT restore it afterward, so
the save is ineffective and result.Status is still clobbered from StatusStopped to
StatusCancelled for an externally stopped delegate whose packet reports OutcomeCancelled.

Add the restore after the populate call (mirroring the existing durableReason restore):

	if durableStatus != "" {
		result.Status = durableStatus
	}

Function body after populateStableDelegateSendResult:
%s`, afterPopulate)
	}
}

// savesDurableStatus reports whether the code fragment (the portion of the
// function body preceding populateStableDelegateSendResult) assigns
// result.Status into an identifier containing "durableStatus". This tolerates a
// `:=` short declaration, an `=` assignment, or a multi-value tuple such as
// `durableStatus, durableReason := result.Status, result.Reason`.
func savesDurableStatus(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "durableStatus") && strings.Contains(line, "result.Status") {
			return true
		}
	}
	return false
}

// extractFuncBodyByName parses path as a Go source file and returns the raw
// text of the top-level function named funcName, including its opening and
// closing braces. It uses go/parser so brace/string/comment handling is exact.
func extractFuncBodyByName(t *testing.T, path, funcName string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName || fn.Body == nil {
			continue
		}
		body = fn.Body
		break
	}
	if body == nil {
		return ""
	}
	start := fset.Position(body.Pos()).Offset
	end := fset.Position(body.End()).Offset
	return string(src[start:end])
}
