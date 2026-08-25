import type { SessionState } from "../../core/model";
import { Icon, type IconName } from "./Icon";

export type StatusLabelState =
  | "attention"
  | "running"
  | "complete"
  | "waiting"
  | "failed";

export type StatusLabelInput = StatusLabelState | SessionState;

const presentation: Readonly<
  Record<
    StatusLabelInput,
    { icon: IconName; text: string; state: StatusLabelState }
  >
> = {
  attention: { icon: "warning", text: "Needs attention", state: "attention" },
  "needs-answer": {
    icon: "warning",
    text: "Needs answer",
    state: "attention",
  },
  "needs-permission": {
    icon: "warning",
    text: "Needs permission",
    state: "attention",
  },
  running: { icon: "running", text: "Running", state: "running" },
  complete: { icon: "complete", text: "Complete", state: "complete" },
  completed: { icon: "complete", text: "Completed", state: "complete" },
  waiting: { icon: "waiting", text: "Waiting", state: "waiting" },
  failed: { icon: "failed", text: "Failed", state: "failed" },
};

export interface StatusLabelProps {
  state: StatusLabelInput;
}

export function StatusLabel({ state }: StatusLabelProps) {
  const status = presentation[state];
  return (
    <span data-status-state={status.state}>
      <Icon name={status.icon} decorative />
      <span>{status.text}</span>
    </span>
  );
}
