// Client access to the daemon's declared lifecycle machines. The catalog is
// generated from appwire/lifecycle.go into types.gen.ts, so a state's class,
// terminality and resting polarity reach the web and native clients from the
// same declaration the session daemon obeys. Clients ask here instead of
// carrying their own status lists.
import { type LIFECYCLE_CLASSES, LIFECYCLE_MACHINES } from "./types.gen";

export type LifecycleClass = (typeof LIFECYCLE_CLASSES)[number];

export interface LifecycleStateFacts {
  class: LifecycleClass;
  terminal: boolean;
  resting?: boolean;
  summary?: string;
}

export interface LifecycleMachineFacts {
  initial?: string;
  states: Record<string, LifecycleStateFacts>;
}

// The generated literal type is readonly and keys are exact; runtime callers
// carry plain wire strings, so this cast gives the lookups one checked shape.
const MACHINES = LIFECYCLE_MACHINES as unknown as Record<string, LifecycleMachineFacts>;

/** The declared machine for kind, or undefined when this build does not know it. */
export function lifecycleMachine(kind: string): LifecycleMachineFacts | undefined {
  return Object.hasOwn(MACHINES, kind) ? MACHINES[kind] : undefined;
}

/** The declared state facts for kind/name, or undefined when either is unknown. */
export function lifecycleState(kind: string, name: string): LifecycleStateFacts | undefined {
  const machine = lifecycleMachine(kind);
  return machine && Object.hasOwn(machine.states, name) ? machine.states[name] : undefined;
}

/** The state's presentation family. An unknown state reads active, matching
 * the Go Class fallback, so a newer daemon's state never disappears as settled. */
export function lifecycleStateClass(kind: string, name: string): LifecycleClass {
  return lifecycleState(kind, name)?.class ?? "active";
}

/** Whether the declared state ends the entity. An unknown state is not terminal. */
export function lifecycleStateIsTerminal(kind: string, name: string): boolean {
  return lifecycleState(kind, name)?.terminal === true;
}

/** Whether the declared state means "not processing", including a failed rest. */
export function lifecycleStateIsResting(kind: string, name: string): boolean {
  return lifecycleState(kind, name)?.resting === true;
}

/** Whether the state reads as a failure in its machine. */
export function lifecycleStateIsFailed(kind: string, name: string): boolean {
  return lifecycleState(kind, name)?.class === "failed";
}
