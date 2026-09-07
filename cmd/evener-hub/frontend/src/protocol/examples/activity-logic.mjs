import assert from "node:assert/strict";
import { ActivityList } from "@evener/appwire-client";

const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;

function validateInput(input) {
  assert.ok(
    isRecord(input) &&
      Object.keys(input).every((key) => ["ref", "threadId", "maxPages"].includes(key)) &&
      nonempty(input.ref) &&
      nonempty(input.threadId) &&
      (input.maxPages === undefined ||
        (Number.isSafeInteger(input.maxPages) && input.maxPages >= 1 && input.maxPages <= 100)),
    "Invalid activity parameters.",
  );
}

function branchKey(branch) {
  return `${branch.id}\u0000${branch.continuation}`;
}

function classifyBranches(list, pagesRead) {
  const branches = list.branches();
  if (branches.length === 0) {
    const tree = list.getSnapshot().tree;
    return resultFrom(list, tree && tree.root.counts.complete === false ? "incomplete" : "read", pagesRead);
  }
  const branchError = branches.find((branch) => branch.error)?.error;
  return resultFrom(list, branchError ? "failed" : "incomplete", pagesRead, branchError);
}

function resultFrom(list, outcome, pagesRead, error) {
  const branches = list.branches();
  return {
    outcome,
    readback: list.getSnapshot().tree ? structuredClone(list.getSnapshot().tree) : null,
    pagesRead,
    remainingBranches: branches.map(({ id, label, error: branchError, truncated, continuation }) => ({
      id,
      label,
      ...(branchError ? { error: branchError } : {}),
      ...(truncated !== undefined ? { truncated } : {}),
      ...(continuation !== undefined ? { continuation } : {}),
    })),
    ...(error ? { error } : {}),
  };
}

/** Read a bounded activity tree, explicitly consuming advertised continuations. */
export async function runActivity(hub, input) {
  validateInput(input);
  const captured = structuredClone(input);
  const list = new ActivityList(hub, captured.ref, captured.threadId);
  const budget = captured.maxPages ?? 10;
  const seen = new Set();
  let pagesRead = 0;
  try {
    await hub.connect();
    await list.refresh();
    pagesRead++;
    let snapshot = list.getSnapshot();
    if (snapshot.unsupported) return resultFrom(list, "unavailable", pagesRead);
    if (snapshot.ended) return resultFrom(list, "ended", pagesRead);
    if (snapshot.error) return resultFrom(list, "failed", pagesRead, snapshot.error);

    while (pagesRead < budget) {
      const branch = list.branches().find((candidate) => candidate.continuation);
      if (!branch) return classifyBranches(list, pagesRead);
      const key = branchKey(branch);
      if (seen.has(key)) return resultFrom(list, "incomplete", pagesRead);
      seen.add(key);
      await list.loadMore(branch.id, branch.continuation);
      pagesRead++;
      snapshot = list.getSnapshot();
      if (snapshot.unsupported) return resultFrom(list, "unavailable", pagesRead);
      if (snapshot.ended) return resultFrom(list, "ended", pagesRead);
      if (snapshot.error) return resultFrom(list, "failed", pagesRead, snapshot.error);
    }
    return classifyBranches(list, pagesRead);
  } catch (error) {
    return resultFrom(list, "failed", pagesRead, error instanceof Error ? error.message : undefined);
  } finally {
    list.dispose();
  }
}

export function summarizeActivity(result) {
  const branches = result.remainingBranches ?? [];
  return {
    outcome: result.outcome,
    pagesRead: result.pagesRead,
    remainingBranches: branches.length,
  };
}
