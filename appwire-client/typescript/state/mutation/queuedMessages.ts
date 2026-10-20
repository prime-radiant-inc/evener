import type { InputItem } from "../../types.gen";
import type { MutationOutboxRecord } from "./records";

const REMOVABLE_MESSAGE_METHODS = new Set([
  "turn/start",
  "turn/steer",
  "turn/queue",
  "turn/drainAsSteer",
  "turn/promoteQueuedAsSteer",
]);

function hasContradictoryAttemptEvidence(record: MutationOutboxRecord): boolean {
  return record.attempted === true || (record.unresolvedAttempts !== undefined && record.unresolvedAttempts > 0);
}

function isSafelyWaiting(record: MutationOutboxRecord): boolean {
  if (hasContradictoryAttemptEvidence(record)) return false;
  if (record.state === "canceled") return true;
  if (record.state !== "submitting") return false;
  return record.attempted === false || record.unresolvedAttempts === 0;
}

function recordInput(record: MutationOutboxRecord): InputItem[] {
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
}

function hasComposerInput(record: MutationOutboxRecord): boolean {
  return recordInput(record).some((item) => {
    if (item.type === "image") return true;
    if (item.type === "text") return typeof item.text === "string" && item.text.trim() !== "";
    if (item.type === "skill" || item.type === "command")
      return typeof item.name === "string" && item.name.trim() !== "";
    return false;
  });
}

export function canEditQueuedMessage(record: MutationOutboxRecord): boolean {
  if (!isSafelyWaiting(record)) return false;
  if (record.method === "turn/start" || record.method === "turn/queue" || record.method === "turn/steer") return true;
  return record.method === "turn/drainAsSteer" && hasComposerInput(record);
}

export function canRemoveQueuedMessage(record: MutationOutboxRecord): boolean {
  return isSafelyWaiting(record) && REMOVABLE_MESSAGE_METHODS.has(record.method);
}
