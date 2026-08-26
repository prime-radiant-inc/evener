import type { DisplayTone } from "../model";
import { Icon, type IconName } from "./Icon";

// StatusLabelState is the reduced presentation set used as the non-color
// marker (data-status-state). DisplayTone is the live-concept contract input.
export type StatusLabelState = DisplayTone;

export type StatusLabelInput = StatusLabelState;

const presentation: Readonly<
  Record<
    StatusLabelInput,
    { icon: IconName; text: string; state: StatusLabelState }
  >
> = {
  attention: { icon: "warning", text: "Needs attention", state: "attention" },
  running: { icon: "running", text: "Running", state: "running" },
  success: { icon: "complete", text: "Complete", state: "success" },
  failed: { icon: "failed", text: "Failed", state: "failed" },
  idle: { icon: "waiting", text: "Idle", state: "idle" },
  unknown: { icon: "waiting", text: "Unknown", state: "unknown" },
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
