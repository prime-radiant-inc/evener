import {
  type AttentionPlacementEntry,
  attentionPlacementIsValid,
  captureAttentionPlacement,
  presentAttentionTarget,
  releaseUnreferencedAttentionPanes,
  restoreAttentionPlacement,
} from "./attentionPlacement";
import { navigate, observeRouteHistory, paneToURL } from "./routing";

const STATE_KEY = "__evenerAttention";
const generation = crypto.randomUUID();
interface Token {
  generation: string;
  id: string;
}
interface HistoryBinding {
  token: Token;
  url: string;
  placement: AttentionPlacementEntry;
}
interface BranchEntry {
  token: Token | null;
  url: string;
}
const entries = new Map<string, HistoryBinding>();
let branch: BranchEntry[] = [];
let cursor = -1;
let serial = 0;

function key(token: Token): string {
  return `${token.generation}:${token.id}`;
}
function tokenAt(state: unknown): Token | null {
  if (!state || typeof state !== "object") return null;
  const token = (state as Record<string, unknown>)[STATE_KEY];
  if (!token || typeof token !== "object") return null;
  const { generation: tokenGeneration, id } = token as Record<string, unknown>;
  return typeof tokenGeneration === "string" && typeof id === "string" ? { generation: tokenGeneration, id } : null;
}
function currentURL(): string {
  return `${location.pathname}${location.search}${location.hash}`;
}
function writeToken(token: Token): void {
  const state = history.state && typeof history.state === "object" ? history.state : {};
  history.replaceState({ ...state, [STATE_KEY]: token }, "", currentURL());
}
function bindingAt(token: Token | null, url: string): HistoryBinding | null {
  if (!token || token.generation !== generation) return null;
  const binding = entries.get(key(token));
  return binding?.url === url && attentionPlacementIsValid(binding.placement) ? binding : null;
}
function bindingFor(token: Token | null): HistoryBinding | null {
  return bindingAt(token, currentURL());
}
function retainBranchEntries(): void {
  const retainedKeys = new Set(branch.flatMap((entry) => (entry.token ? [key(entry.token)] : [])));
  for (const entry of entries.values()) {
    if (!retainedKeys.has(key(entry.token))) entries.delete(key(entry.token));
  }
  const panes = new Set([...entries.values()].map((entry) => entry.placement.pane));
  releaseUnreferencedAttentionPanes(panes);
}
function branchIndex(token: Token | null, url: string): number {
  return branch.findIndex(
    (entry) => entry.url === url && entry.token?.generation === token?.generation && entry.token?.id === token?.id,
  );
}
function onPopState(): void {
  cursor = branchIndex(tokenAt(history.state), currentURL());
}
window.addEventListener("popstate", onPopState);

observeRouteHistory((previousState, nextState, previousURL, nextURL) => {
  const previousToken = tokenAt(previousState);
  const previousIndex = branchIndex(previousToken, previousURL);
  if (previousIndex >= 0) {
    cursor = previousIndex;
  } else if (previousToken?.generation === generation && entries.has(key(previousToken))) {
    branch = [{ token: previousToken, url: previousURL }];
    cursor = 0;
  } else {
    branch = [];
    cursor = -1;
  }
  if (cursor >= 0) {
    branch = branch.slice(0, cursor + 1);
  } else {
    branch = [];
  }
  const nextToken = tokenAt(nextState);
  branch.push({ token: nextToken?.generation === generation ? nextToken : null, url: nextURL });
  cursor = branch.length - 1;
  retainBranchEntries();
});

/** Runtime token for the current, still-valid attention-owned browser entry. */
export function currentAttentionEntryToken(): string | null {
  const binding = bindingFor(tokenAt(history.state));
  return binding ? key(binding.token) : null;
}

/** Whether the current exact runtime entry owns this reader. */
export function attentionEntryOwnsReader(paneId: string, sessionRef: string): boolean {
  const binding = bindingFor(tokenAt(history.state));
  return binding !== null && binding.placement.pane.id === paneId && binding.placement.requestedRef === sessionRef;
}

/** Restore a current attention entry before generic route placement. */
export function restoreCurrentAttentionEntry(): boolean {
  const binding = bindingFor(tokenAt(history.state));
  return binding !== null && restoreAttentionPlacement(binding.placement);
}

/** Request browser Back only when this entry has a known immediate predecessor. */
export function goBackInAttentionHistory(): boolean {
  const binding = bindingFor(tokenAt(history.state));
  const index = branchIndex(binding?.token ?? null, currentURL());
  if (!binding || index <= 0) return false;
  const predecessor = branch[index - 1];
  if (!predecessor?.token || !bindingAt(predecessor.token, predecessor.url)) return false;
  history.back();
  return true;
}

/** Navigate to a needs-you reader and retain exact source/destination placement. */
export function openAttentionSession(ref: string): void {
  const target = paneToURL("session", { ref });
  if (!target) return;
  const departure = captureAttentionPlacement();
  if (departure) {
    const existingToken = tokenAt(history.state);
    const token = existingToken?.generation === generation ? existingToken : { generation, id: `entry-${++serial}` };
    const prior = entries.get(key(token));
    entries.set(key(token), { token, url: currentURL(), placement: departure });
    writeToken(token);
    // Keep a prior branch position when focus or a cascade leaf changed in place.
    if (cursor >= 0) {
      const index = branchIndex(token, currentURL());
      if (index >= 0) cursor = index;
      else if (prior) branch[cursor] = { token, url: currentURL() };
    }
  }
  const targetToken = { generation, id: `entry-${++serial}` };
  const destination = presentAttentionTarget(ref);
  if (!destination) return;
  entries.set(key(targetToken), { token: targetToken, url: target, placement: destination });
  navigate(target, { attentionEntry: targetToken });
}
