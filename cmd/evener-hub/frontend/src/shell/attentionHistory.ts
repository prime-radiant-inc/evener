import { createSecureUUID } from "../stores/secureUUID";
import {
  type AttentionPlacementEntry,
  attentionPlacementIsValid,
  captureAttentionPlacement,
  onAttentionPaneRetype,
  presentAttentionTarget,
  refreshAttentionPlacement,
  releaseUnreferencedAttentionPanes,
  restoreAttentionPlacement,
} from "./attentionPlacement";
import {
  navigate,
  observeRouteHistory,
  paneToURL,
  routeHistoryEntryId,
  ATTENTION_ENTRY_STATE_KEY as STATE_KEY,
} from "./routing";
import { workspaceStore } from "./workspace";

// Not crypto.randomUUID(): an ordinary HTTP origin outside localhost is not a
// secure context, so that call does not exist there - and this one runs at
// module load, before any route renders.
const generation = createSecureUUID();
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
  routeEntryId: string | null;
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
function branchIndex(token: Token | null, url: string, routeEntryId: string | null = null): number {
  return branch.findIndex(
    (entry) =>
      entry.url === url &&
      (token
        ? entry.token?.generation === token.generation && entry.token.id === token.id
        : routeEntryId !== null
          ? entry.routeEntryId === routeEntryId
          : entry.token === null && entry.routeEntryId === null),
  );
}
function onPopState(): void {
  cursor = branchIndex(tokenAt(history.state), currentURL(), routeHistoryEntryId(history.state));
}
window.addEventListener("popstate", onPopState);

observeRouteHistory((previousState, nextState, previousURL, nextURL, transition) => {
  const previousToken = tokenAt(previousState);
  const previousEntryId = routeHistoryEntryId(previousState);
  const previousIndex = branchIndex(previousToken, previousURL, previousEntryId);
  if (transition === "replace") {
    if (previousIndex >= 0) {
      const nextToken = tokenAt(nextState);
      branch[previousIndex] = {
        token: nextToken?.generation === generation ? nextToken : null,
        routeEntryId: routeHistoryEntryId(nextState),
        url: nextURL,
      };
      cursor = previousIndex;
      retainBranchEntries();
    }
    return;
  }
  if (previousIndex >= 0) {
    cursor = previousIndex;
  } else if (previousToken?.generation === generation && entries.has(key(previousToken))) {
    branch = [{ token: previousToken, routeEntryId: previousEntryId, url: previousURL }];
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
  branch.push({
    token: nextToken?.generation === generation ? nextToken : null,
    routeEntryId: routeHistoryEntryId(nextState),
    url: nextURL,
  });
  cursor = branch.length - 1;
  retainBranchEntries();
});

// Closing the reader an attention entry owns retires that entry: the route
// still names the closed conversation, and ordinary reconciliation would reopen
// it - replacing the primary slot and taking parked source readers, and their
// staged work, with it. Retiring first leaves the address bar naming the reader
// the host actually kept.
workspaceStore.subscribe((state, previous) => {
  if (state.panes === previous.panes) return;
  const token = tokenAt(history.state);
  if (!token || token.generation !== generation) return;
  const binding = entries.get(key(token));
  if (!binding || state.panes.includes(binding.placement.pane)) return;
  retireAttentionEntry(binding);
});

function retireAttentionEntry(binding: HistoryBinding): void {
  const state = workspaceStore.getState();
  const survivor =
    state.panes.find((pane) => pane.id === state.focusedPaneId && pane.slot !== "parked") ??
    state.panes.find((pane) => pane.slot === "main") ??
    null;
  const url = survivor ? paneToURL(survivor.type, survivor.params) : null;
  // The branch entry keeps the retiring token until navigate()'s replacement
  // observer matches it by that token and the previous URL. Clearing it first
  // makes the observer treat this as a fresh branch, which drops every other
  // entry and releases their parked readers - including a source Back can
  // still reach, with its staged attachments.
  navigate(url ?? "/", { replace: true });
  entries.delete(key(binding.token));
}

// A drill inside the cascade an attention entry owns keeps the same pane and
// lifetime: refresh that entry instead of letting it fall invalid, or the
// address bar turns into an ordinary route that reopens the pre-drill reader
// and steals focus from the drill's leaf. Other entries keep their own
// snapshots and stay invalid, so a stale historical reader is never revived.
onAttentionPaneRetype((_previous, replacement) => {
  const token = tokenAt(history.state);
  if (!token || token.generation !== generation) return;
  const binding = entries.get(key(token));
  if (!binding || binding.placement.pane !== replacement) return;
  refreshAttentionPlacement(binding.placement);
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
  const index = branchIndex(binding?.token ?? null, currentURL(), routeHistoryEntryId(history.state));
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
      const entryId = routeHistoryEntryId(history.state);
      const index = branchIndex(token, currentURL(), entryId);
      const currentEntry = branch[cursor];
      if (index >= 0) cursor = index;
      else if (currentEntry?.url === currentURL() && currentEntry.token === null) {
        branch[cursor] = { token, routeEntryId: entryId, url: currentURL() };
      } else if (prior) branch[cursor] = { token, routeEntryId: entryId, url: currentURL() };
    }
  }
  const targetToken = { generation, id: `entry-${++serial}` };
  const destination = presentAttentionTarget(ref);
  if (!destination) return;
  entries.set(key(targetToken), { token: targetToken, url: target, placement: destination });
  navigate(target, { attentionEntry: targetToken });
}
