// Shared @file mention token parsing, plain-text splicing, path-aware fuzzy
// ranking, and debounced search state for browser and native composers.

import type { AppwireClientLike } from "./clientLike";
import { isMethodNotFound } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { hostRequest, normalizeHost } from "./hostRouting";
import { hostIdOf } from "./sessionFacts";
import type { FileSearchItem, FilesSearchParams } from "./types.gen";

export const FILE_MENTION_DEBOUNCE_MS = 80;
export const FILE_MENTION_MAX_QUERY_LENGTH = 256;
export const FILE_MENTION_DEFAULT_LIMIT = 25;

const FILE_MENTION_MAX_LIMIT = 50;
const FILE_MENTION_DP_MAX_LEN = 512;
const NEG_INF = -1_000_000_000;

const VALID_AT_BOUNDARY_RE = /^[\s([{<"'`]$/u;
const PUNCTUATION_OR_SPACE_HEAD_RE = /^(?:[\s.,;:!?)\]}'"])/u;
const UNQUOTED_STOP_RE = /[\s",;:!?]/u;
const UNQUOTED_TRAILING_PUNCT_RE = /[.,;:!?)\]}'"]/u;

export interface FileMentionToken {
  start: number;
  end: number;
  query: string;
  quoted: boolean;
}

export interface FileMentionSpliceResult {
  text: string;
  caret: number;
  keepOpen: boolean;
}

export interface FileMentionMatch {
  score: number;
  positions: number[];
}

export type FileMentionTarget =
  | { kind: "session"; ref: string }
  | { kind: "cwd"; host: string | null | undefined; cwd: string };

export interface FileMentionSearchInputs {
  client: Pick<AppwireClientLike, "request"> | null;
  target: FileMentionTarget | null;
  query: string;
  enabled: boolean;
  immediate?: boolean;
  limit?: number;
}

export interface FileMentionSnapshot {
  items: FileSearchItem[];
  loading: boolean;
  truncated: boolean;
  settledQuery: string | null;
}

export interface FileMentionStore extends FrameworkFreeStore<FileMentionSnapshot> {
  search(inputs: FileMentionSearchInputs): () => void;
  clear(): void;
}

// Tracks (client, host) pairs where the remote/local hub rejected
// evener/files/search with CodeMethodNotFound so older hubs degrade quietly.
const unsupportedHostsByClient = new WeakMap<object, Set<string>>();

export function clearUnsupportedFileSearchHost(client: object, host?: string): void {
  if (host === undefined) {
    unsupportedHostsByClient.delete(client);
    return;
  }
  unsupportedHostsByClient.get(client)?.delete(normalizeHost(host));
}

function isFileSearchSupported(client: object, host: string): boolean {
  return !unsupportedHostsByClient.get(client)?.has(host);
}

function markUnsupportedFileSearchHost(client: object, host: string): void {
  let set = unsupportedHostsByClient.get(client);
  if (!set) {
    set = new Set<string>();
    unsupportedHostsByClient.set(client, set);
  }
  set.add(host);
}

export function parseFileMentionToken(text: string, caret: number): FileMentionToken | null {
  if (caret <= 0 || caret > text.length) return null;

  let i = 0;
  while (i < caret) {
    if (text.charAt(i) !== "@" || (i > 0 && !VALID_AT_BOUNDARY_RE.test(text.charAt(i - 1)))) {
      i += 1;
      continue;
    }

    const openPos = i + 1;
    if (openPos < caret && text.charAt(openPos) === '"') {
      let query = "";
      let escaped = false;
      let closedBeforeCaret = false;
      let abortedAt = -1;
      let end = caret;
      for (let j = openPos + 1; j < text.length; j += 1) {
        const ch = text.charAt(j);
        if (ch === "\r" || ch === "\n") {
          if (j < caret) abortedAt = j;
          break;
        }
        if (escaped) {
          escaped = false;
          if (j < caret) {
            query += ch === '"' || ch === "\\" ? ch : `\\${ch}`;
          }
          continue;
        }
        if (ch === "\\") {
          escaped = true;
          if (j + 1 === caret) {
            query += "\\";
          }
          continue;
        }
        if (ch === '"') {
          if (j < caret) {
            closedBeforeCaret = true;
            abortedAt = j;
          } else {
            end = j + 1;
          }
          break;
        }
        if (j < caret) {
          query += ch;
        }
      }
      if (closedBeforeCaret || abortedAt >= 0) {
        i = abortedAt + 1;
        continue;
      }
      if (query.length > FILE_MENTION_MAX_QUERY_LENGTH) return null;
      return { start: i, end, query, quoted: true };
    }

    if (openPos < caret && text.charAt(openPos) === "@") {
      i += 1;
      continue;
    }

    let breakAt = -1;
    for (let j = openPos; j < caret; j += 1) {
      const ch = text.charAt(j);
      if (ch === '"' || /\s/u.test(ch)) {
        breakAt = j;
        break;
      }
    }
    if (breakAt >= 0) {
      i = breakAt + 1;
      continue;
    }

    const query = text.slice(openPos, caret);
    if (query.length > FILE_MENTION_MAX_QUERY_LENGTH) return null;
    let end = caret;
    while (end < text.length && !UNQUOTED_STOP_RE.test(text.charAt(end))) {
      end += 1;
    }
    while (end > caret && UNQUOTED_TRAILING_PUNCT_RE.test(text.charAt(end - 1))) {
      end -= 1;
    }
    return { start: i, end, query, quoted: false };
  }

  return null;
}

export function spliceFileMention(
  text: string,
  token: FileMentionToken,
  item: FileSearchItem,
): FileMentionSpliceResult {
  const before = text.slice(0, token.start);
  const suffix = text.slice(token.end);

  const isDir = item.kind === "dir";
  const rawPath = isDir ? `${item.path.replace(/\/+$/, "")}/` : item.path;
  const needsQuotes = /[\s"]/u.test(item.path);
  const formatted = needsQuotes ? `@"${rawPath.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"` : `@${rawPath}`;

  if (isDir) {
    const caretOffset = needsQuotes ? formatted.length - 1 : formatted.length;
    return {
      text: before + formatted + suffix,
      caret: before.length + caretOffset,
      keepOpen: true,
    };
  }

  const inserted = PUNCTUATION_OR_SPACE_HEAD_RE.test(suffix) ? formatted : `${formatted} `;
  return {
    text: before + inserted + suffix,
    caret: before.length + inserted.length,
    keepOpen: false,
  };
}

function normalizeQuery(query: string): string {
  let q = query;
  while (q.startsWith("./")) q = q.slice(2);
  return q;
}

function queryIncludesDotfiles(normalized: string): boolean {
  const q = normalizeQuery(normalized);
  return q.startsWith(".") || q.includes("/.");
}

function foldCharCode(ch: string, code: number): string {
  if (code < 128) {
    return code >= 65 && code <= 90 ? String.fromCharCode(code + 32) : ch;
  }
  return ch.toLowerCase();
}

function isLowerChar(ch: string, code: number): boolean {
  if (code < 128) return code >= 97 && code <= 122;
  const lower = ch.toLowerCase();
  return lower !== ch.toUpperCase() && ch === lower;
}

function isUpperChar(ch: string, code: number): boolean {
  if (code < 128) return code >= 65 && code <= 90;
  const upper = ch.toUpperCase();
  return ch.toLowerCase() !== upper && ch === upper;
}

function isSubsequenceFold(path: string, query: string): boolean {
  if (query.length === 0) return true;
  let qi = 0;
  let qFold = foldCharCode(query.charAt(0), query.charCodeAt(0));
  for (let j = 0; j < path.length; j += 1) {
    const pFold = foldCharCode(path.charAt(j), path.charCodeAt(j));
    if (pFold === qFold) {
      qi += 1;
      if (qi >= query.length) return true;
      qFold = foldCharCode(query.charAt(qi), query.charCodeAt(qi));
    }
  }
  return false;
}

function splitDirPrefixQuery(norm: string): { prefix: string; dirNoSlash: string; frag: string } | null {
  const lastSlash = norm.lastIndexOf("/");
  if (lastSlash < 0) return null;
  return {
    prefix: norm.slice(0, lastSlash + 1),
    dirNoSlash: norm.slice(0, lastSlash),
    frag: norm.slice(lastSlash + 1),
  };
}

function hasExactDirPrefixScope(
  items: readonly FileSearchItem[],
  norm: string,
): { prefix: string; frag: string } | null {
  const split = splitDirPrefixQuery(norm);
  if (!split) return null;
  if (split.frag === "") {
    return { prefix: split.prefix, frag: "" };
  }
  for (const item of items) {
    if (
      item.path.startsWith(split.prefix) ||
      (split.dirNoSlash !== "" && item.kind === "dir" && item.path === split.dirNoSlash)
    ) {
      return { prefix: split.prefix, frag: split.frag };
    }
  }
  return null;
}

export function scoreFileMention(path: string, query: string): FileMentionMatch | null {
  const norm = normalizeQuery(query);
  if (norm.length === 0) {
    return { score: 0, positions: [] };
  }
  const split = splitDirPrefixQuery(norm);
  if (split) {
    if (path.startsWith(split.prefix)) {
      const prefixPositions = Array.from({ length: split.prefix.length }, (_, k) => k);
      if (split.frag === "") {
        return { score: 0, positions: prefixPositions };
      }
      const relMatch = scoreNormalizedFileMention(path.slice(split.prefix.length), split.frag);
      if (!relMatch) return null;
      return {
        score: relMatch.score,
        positions: [...prefixPositions, ...relMatch.positions.map((pos) => split.prefix.length + pos)],
      };
    }
    if (split.frag === "") return null;
  }
  return scoreNormalizedFileMention(path, norm);
}

function scoreNormalizedFileMention(path: string, norm: string): FileMentionMatch | null {
  if (norm.length === 0) {
    return { score: 0, positions: [] };
  }
  if (norm.length > path.length || !isSubsequenceFold(path, norm)) {
    return null;
  }

  const n = path.length;
  const m = norm.length;
  const pFold = new Array<string>(n);
  let slashCount = 0;
  let basenameStart = 0;
  for (let j = 0; j < n; j += 1) {
    const ch = path.charAt(j);
    const code = path.charCodeAt(j);
    pFold[j] = foldCharCode(ch, code);
    if (code === 47) {
      slashCount += 1;
      basenameStart = j + 1;
    }
  }

  const qFold = new Array<string>(m);
  for (let i = 0; i < m; i += 1) {
    qFold[i] = foldCharCode(norm.charAt(i), norm.charCodeAt(i));
  }

  const posBonus = new Int32Array(n);
  for (let j = 0; j < n; j += 1) {
    let b = 1;
    if (j === 0) {
      b += 24;
    } else {
      const prevCode = path.charCodeAt(j - 1);
      if (prevCode === 47) {
        b += 24;
      } else if (
        prevCode === 95 ||
        prevCode === 45 ||
        prevCode === 46 ||
        prevCode === 32 ||
        (isLowerChar(path.charAt(j - 1), prevCode) && isUpperChar(path.charAt(j), path.charCodeAt(j)))
      ) {
        b += 14;
      }
    }
    if (j >= basenameStart) {
      b += 12;
    }
    posBonus[j] = b;
  }

  let positions: number[];
  let alignScore: number;

  if (n > FILE_MENTION_DP_MAX_LEN) {
    positions = [];
    let qi = 0;
    for (let j = 0; j < n && qi < m; j += 1) {
      if (pFold[j] === qFold[qi]) {
        positions.push(j);
        qi += 1;
      }
    }
    if (positions.length !== m) return null;
    alignScore = scoreAlignmentPositions(path, norm, posBonus, positions);
  } else {
    const dp = alignFileMentionDP(path, pFold, norm, qFold, posBonus);
    if (!dp) return null;
    positions = dp.positions;
    alignScore = dp.alignScore;
  }

  const baseFold = pFold.slice(basenameStart).join("");
  const lowerQuery = qFold.join("");
  const exactBase = baseFold === lowerQuery;
  const basePrefix = baseFold.startsWith(lowerQuery);
  const fullPrefix = pFold.join("").startsWith(lowerQuery);
  const hasQuerySlash = norm.includes("/");
  const firstPos = positions[0];
  const allInBase = !hasQuerySlash && firstPos !== undefined && firstPos >= basenameStart;

  let score = alignScore;
  if (exactBase) score += 200;
  if (basePrefix) score += 100;
  if (fullPrefix) score += 80;
  if (allInBase) score += 60;
  score -= 2 * slashCount;
  score -= Math.floor(n / 8);
  return { score, positions };
}

function alignFileMentionDP(
  path: string,
  pFold: readonly string[],
  query: string,
  qFold: readonly string[],
  posBonus: Int32Array,
): { positions: number[]; alignScore: number } | null {
  const m = query.length;
  const n = path.length;

  let prevScore = new Int32Array(n);
  let currScore = new Int32Array(n);
  const back = new Int16Array(m * n);

  const q0 = query.charAt(0);
  const qFold0 = qFold[0] ?? "";
  for (let j = 0; j < n; j += 1) {
    if (pFold[j] === qFold0) {
      let s = posBonus[j] ?? 0;
      if (path.charAt(j) === q0) s += 2;
      prevScore[j] = s;
    } else {
      prevScore[j] = NEG_INF;
    }
  }

  for (let i = 1; i < m; i += 1) {
    let bestGapScore = NEG_INF;
    let bestGapIdx = -1;
    const qi = query.charAt(i);
    const qFoldI = qFold[i] ?? "";
    const rowOffset = i * n;

    for (let j = 0; j < n; j += 1) {
      currScore[j] = NEG_INF;
      back[rowOffset + j] = -1;

      if (j >= 2) {
        const prevIdx = j - 2;
        const prevVal = prevScore[prevIdx] ?? NEG_INF;
        if (prevVal > NEG_INF / 2) {
          const cand = prevVal + prevIdx;
          if (cand > bestGapScore) {
            bestGapScore = cand;
            bestGapIdx = prevIdx;
          }
        }
      }

      if (j < i || pFold[j] !== qFoldI) continue;

      let bestPrevScore = NEG_INF;
      let bestPrev = -1;
      if (bestGapIdx >= 0) {
        bestPrevScore = bestGapScore - j - 1;
        bestPrev = bestGapIdx;
      }

      const adjVal = prevScore[j - 1] ?? NEG_INF;
      if (adjVal > NEG_INF / 2) {
        const consecScore = adjVal + 16;
        if (consecScore > bestPrevScore) {
          bestPrevScore = consecScore;
          bestPrev = j - 1;
        }
      }
      if (bestPrev < 0) continue;

      let s = bestPrevScore + (posBonus[j] ?? 0);
      if (path.charAt(j) === qi) s += 2;
      currScore[j] = s;
      back[rowOffset + j] = bestPrev;
    }

    const tmp = prevScore;
    prevScore = currScore;
    currScore = tmp;
  }

  let bestAlignScore = NEG_INF;
  let endJ = -1;
  for (let j = m - 1; j < n; j += 1) {
    const val = prevScore[j] ?? NEG_INF;
    if (val > bestAlignScore) {
      bestAlignScore = val;
      endJ = j;
    }
  }
  if (endJ < 0) return null;

  const positions = new Array<number>(m);
  let cur = endJ;
  for (let i = m - 1; i >= 0; i -= 1) {
    positions[i] = cur;
    if (i > 0) {
      cur = back[i * n + cur] ?? -1;
    }
  }
  return { positions, alignScore: bestAlignScore };
}

function scoreAlignmentPositions(
  path: string,
  query: string,
  posBonus: Int32Array,
  positions: readonly number[],
): number {
  let score = 0;
  for (let i = 0; i < positions.length; i += 1) {
    const j = positions[i] ?? 0;
    score += posBonus[j] ?? 0;
    if (path.charAt(j) === query.charAt(i)) {
      score += 2;
    }
    if (i > 0) {
      const prev = positions[i - 1] ?? 0;
      if (j === prev + 1) {
        score += 16;
      } else {
        const skipped = j - prev - 1;
        score -= 3 + (skipped - 1);
      }
    }
  }
  return score;
}

export function matchFileMentionPositions(path: string, query: string): number[] {
  return scoreFileMention(path, query)?.positions ?? [];
}

function countSlashes(path: string): number {
  let count = 0;
  for (let i = 0; i < path.length; i += 1) {
    if (path.charCodeAt(i) === 47) count += 1;
  }
  return count;
}

function kindRank(kind: string): number {
  return kind === "file" ? 0 : 1;
}

function compareStringsLexical(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function rankEmptyFileMentions(items: readonly FileSearchItem[], limit?: number): FileSearchItem[] {
  const out = [...items].sort((a, b) => {
    const segDiff = countSlashes(a.path) - countSlashes(b.path);
    if (segDiff !== 0) return segDiff;
    const kDiff = kindRank(a.kind) - kindRank(b.kind);
    if (kDiff !== 0) return kDiff;
    return compareStringsLexical(a.path, b.path);
  });
  return limit !== undefined && limit > 0 ? out.slice(0, limit) : out;
}

export function rankFileMentions(items: readonly FileSearchItem[], query: string, limit?: number): FileSearchItem[] {
  const norm = normalizeQuery(query);
  if (norm === "") {
    return rankEmptyFileMentions(items, limit);
  }
  const dirScope = hasExactDirPrefixScope(items, norm);
  if (dirScope && dirScope.frag === "") {
    const underDir = items.filter((item) => item.path.startsWith(dirScope.prefix));
    return rankEmptyFileMentions(underDir, limit);
  }

  const scored: Array<{ item: FileSearchItem; score: number; segments: number }> = [];
  for (const item of items) {
    if (dirScope && !item.path.startsWith(dirScope.prefix)) {
      continue;
    }
    const match = scoreFileMention(item.path, norm);
    if (!match) continue;
    scored.push({
      item,
      score: match.score,
      segments: countSlashes(item.path),
    });
  }
  scored.sort((a, b) => {
    if (a.score !== b.score) return b.score - a.score;
    const kDiff = kindRank(a.item.kind) - kindRank(b.item.kind);
    if (kDiff !== 0) return kDiff;
    if (a.segments !== b.segments) return a.segments - b.segments;
    if (a.item.path.length !== b.item.path.length) return a.item.path.length - b.item.path.length;
    return compareStringsLexical(a.item.path, b.item.path);
  });

  const sortedItems = scored.map((entry) => entry.item);
  return limit !== undefined && limit > 0 ? sortedItems.slice(0, limit) : sortedItems;
}

function hostKeyOfTarget(target: FileMentionTarget): string {
  return target.kind === "session" ? hostIdOf(target.ref) : normalizeHost(target.host ?? null);
}

function scopeKeyOfTarget(target: FileMentionTarget): string {
  if (target.kind === "session") {
    return `session\u0000${target.ref}`;
  }
  return `cwd\u0000${normalizeHost(target.host ?? null)}\u0000${target.cwd.trim()}`;
}

export function createFileMentionStore(): FileMentionStore {
  const store = createFrameworkFreeStore<FileMentionSnapshot>(() => ({
    items: [],
    loading: false,
    truncated: false,
    settledQuery: null,
  }));

  let requestSeq = 0;
  let pendingTimer: ReturnType<typeof setTimeout> | null = null;
  let lastCache: {
    client: object;
    scopeKey: string;
    query: string;
    items: FileSearchItem[];
    truncated: boolean;
  } | null = null;

  const cancelPendingTimer = () => {
    if (pendingTimer !== null) {
      clearTimeout(pendingTimer);
      pendingTimer = null;
    }
  };

  return {
    ...store,
    clear() {
      cancelPendingTimer();
      requestSeq += 1;
      lastCache = null;
      const cur = store.getState();
      if (cur.items.length > 0 || cur.loading || cur.truncated || cur.settledQuery !== null) {
        store.setState({ items: [], loading: false, truncated: false, settledQuery: null });
      }
    },
    search(inputs) {
      cancelPendingTimer();
      requestSeq += 1;
      const seq = requestSeq;

      const { client, target, query, enabled, immediate = false, limit } = inputs;
      if (!enabled || client === null || target === null) {
        lastCache = null;
        store.setState({ items: [], loading: false, truncated: false, settledQuery: null });
        return () => {};
      }

      const hostKey = hostKeyOfTarget(target);
      if (!isFileSearchSupported(client, hostKey)) {
        lastCache = null;
        store.setState({ items: [], loading: false, truncated: false, settledQuery: null });
        return () => {};
      }

      const normQuery = normalizeQuery(query);
      if (normQuery.length > FILE_MENTION_MAX_QUERY_LENGTH) {
        lastCache = null;
        store.setState({ items: [], loading: false, truncated: false, settledQuery: normQuery });
        return () => {};
      }

      const resolvedLimit =
        limit !== undefined && limit > 0 ? Math.min(limit, FILE_MENTION_MAX_LIMIT) : FILE_MENTION_DEFAULT_LIMIT;
      const scopeKey = scopeKeyOfTarget(target);

      let optimisticItems: FileSearchItem[] = [];
      let optimisticTruncated = false;
      if (
        lastCache &&
        lastCache.client === client &&
        lastCache.scopeKey === scopeKey &&
        queryIncludesDotfiles(lastCache.query) === queryIncludesDotfiles(normQuery) &&
        normQuery.toLowerCase().startsWith(lastCache.query.toLowerCase())
      ) {
        optimisticItems = rankFileMentions(lastCache.items, normQuery, resolvedLimit);
        optimisticTruncated = lastCache.truncated;
      } else if (lastCache && (lastCache.client !== client || lastCache.scopeKey !== scopeKey)) {
        lastCache = null;
      }

      store.setState({
        items: optimisticItems,
        loading: true,
        truncated: optimisticTruncated,
        settledQuery: null,
      });

      let active = true;
      const delayMs = immediate ? 0 : FILE_MENTION_DEBOUNCE_MS;
      const timer = setTimeout(() => {
        if (pendingTimer === timer) pendingTimer = null;
        if (!active || seq !== requestSeq) return;

        const params: FilesSearchParams =
          target.kind === "session"
            ? { ref: target.ref, query: normQuery, limit: resolvedLimit }
            : { cwd: target.cwd, query: normQuery, limit: resolvedLimit };

        const req =
          target.kind === "session"
            ? client.request("evener/files/search", params)
            : hostRequest(client, target.host, "evener/files/search", params);

        void req.then(
          (resp) => {
            if (!active || seq !== requestSeq) return;
            const items = Array.isArray(resp?.items) ? resp.items : [];
            const truncated = Boolean(resp?.truncated);
            lastCache = { client, scopeKey, query: normQuery, items, truncated };
            store.setState({ items, loading: false, truncated, settledQuery: normQuery });
          },
          (err: unknown) => {
            if (!active || seq !== requestSeq) return;
            if (isMethodNotFound(err)) {
              markUnsupportedFileSearchHost(client, hostKey);
            }
            lastCache = null;
            store.setState({ items: [], loading: false, truncated: false, settledQuery: normQuery });
          },
        );
      }, delayMs);
      pendingTimer = timer;

      return () => {
        active = false;
        clearTimeout(timer);
        if (pendingTimer === timer) pendingTimer = null;
      };
    },
  };
}
