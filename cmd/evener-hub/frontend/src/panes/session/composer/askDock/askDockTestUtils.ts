// Shared building blocks for tests that drive the ask-pending wire sequence:
// an ask_user call's item/started + item/completed pair, followed by the
// thread/status/changed frame that carries askPending. Used by
// askDockStore.test.ts, AskDock.test.tsx and Composer.integration.test.tsx,
// which otherwise each grow their own copy of "what the hub sends when a
// turn ends on an ask_user call". Callers that render a component still
// wrap these calls in their own `act()` after mount, exactly as before this
// file existed - this module only builds and emits notifications.
import type { AnyNotification } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";

export function askArgs(questions: Array<Record<string, unknown>>): string {
  return JSON.stringify({ questions });
}

export const ONE_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

// The hub stamps askPending onto the thread/status/changed frame that goes
// with the turn ending on an ask_user call (server/appwire_runtime.go's
// stampAskPendingOnStatusChange) - the wire is the only source for the flag
// (deriveAskQuestions.ts), so a fixture that never sends this frame can
// never show a pending batch/dock.
export function askPendingStatusChanged(ref: string): AnyNotification {
  return {
    method: "thread/status/changed",
    params: { threadId: `thr_${ref}`, ref, status: { type: "awaiting" }, askPending: true },
  };
}

// ackAskUserCall assumes `turnId` has already been started (startTurn).
export function ackAskUserCall(
  fake: FakeClient,
  ref: string,
  turnId: string,
  itemId: string,
  callId: string,
  questions: Array<Record<string, unknown>> = ONE_QUESTION,
): void {
  const base = {
    threadId: `thr_${ref}`,
    ref,
    turnId,
    item: {
      type: "commandExecution",
      id: itemId,
      turnId,
      toolName: "ask_user",
      callId,
      argumentsJson: askArgs(questions),
    },
  };
  fake.emitNotification({
    method: "history/updated",
    params: {
      threadId: base.threadId,
      ref: base.ref,
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [{ ...base.item, turnId: base.turnId, status: "inProgress" }],
    },
  });
  fake.emitNotification({
    method: "history/updated",
    params: {
      threadId: base.threadId,
      ref: base.ref,
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [{ ...base.item, turnId: base.turnId, status: "completed" }],
    },
  });
  fake.emitNotification(askPendingStatusChanged(ref));
}
