import type { ThreadModel } from "@evener/appwire-client";
import { sendQueueDecisionFor } from "../../../stores/liveControls";
import type { ResumeOnlySignals } from "../../../stores/threads";

/** Presentation only: liveControls remains the browser's admission owner. */
export function composerAvailability(
  target: ThreadModel,
  pendingSend: boolean,
  restartObligated: boolean,
  signals: ResumeOnlySignals,
): { canSend: boolean; canQueue: boolean } {
  const decision = sendQueueDecisionFor(target, pendingSend, restartObligated, signals);
  return { canSend: decision.action === "send", canQueue: decision.action === "queue" };
}
