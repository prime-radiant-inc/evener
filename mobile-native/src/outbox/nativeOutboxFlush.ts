import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import { OutboxFlush } from "./outboxFlush";

/** The app's one flush. App.tsx binds it to the connection; a session screen
 * asks it to look again when it lets go of its target. */
export const outboxFlush = new OutboxFlush(getNativeMutationRuntime);
