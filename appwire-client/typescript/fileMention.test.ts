// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";
import rankCasesRaw from "../../testdata/filesearch/rank_cases.json?raw";
import type { AppwireClientLike } from "./clientLike";
import { WireError } from "./errors";
import {
  clearUnsupportedFileSearchHost,
  createFileMentionStore,
  FILE_MENTION_DEBOUNCE_MS,
  FILE_MENTION_DEFAULT_LIMIT,
  FILE_MENTION_MAX_QUERY_LENGTH,
  type FileMentionToken,
  matchFileMentionPositions,
  parseFileMentionToken,
  rankFileMentions,
  scoreFileMention,
  spliceFileMention,
} from "./fileMention";
import type { FileSearchItem, FilesSearchResponse, HostRequestParams, MethodName, MethodTypes } from "./types.gen";

interface RankFixture {
  cases: Array<{
    name: string;
    query: string;
    limit: number;
    items: FileSearchItem[];
    wantPaths: string[];
    wantPositions: Record<string, number[]>;
  }>;
}

const rankFixture = JSON.parse(rankCasesRaw) as RankFixture;

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (err: unknown) => void;
}

function makeDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeFakeRequestClient(
  impl: (method: MethodName, params: unknown) => Promise<unknown>,
): Pick<AppwireClientLike, "request"> & { calls: Array<{ method: MethodName; params: unknown }> } {
  const calls: Array<{ method: MethodName; params: unknown }> = [];
  return {
    calls,
    request<M extends MethodName>(method: M, params: MethodTypes[M]["params"]): Promise<MethodTypes[M]["result"]> {
      calls.push({ method, params });
      return impl(method, params) as Promise<MethodTypes[M]["result"]>;
    },
  };
}

describe("fileMention ranker parity", () => {
  it("matches every Go rank_cases.json ordering and highlight positions", () => {
    expect(rankFixture.cases.length).toBeGreaterThan(0);
    for (const tc of rankFixture.cases) {
      const got = rankFileMentions(tc.items, tc.query, tc.limit);
      expect(
        got.map((item) => item.path),
        tc.name,
      ).toEqual(tc.wantPaths);
      for (const [path, wantPos] of Object.entries(tc.wantPositions)) {
        expect(matchFileMentionPositions(path, tc.query), `${tc.name}:${path}`).toEqual(wantPos);
      }
    }
  });

  it("scoreFileMention returns null when query is not a subsequence", () => {
    expect(scoreFileMention("src/main.go", "zzz")).toBeNull();
    expect(matchFileMentionPositions("src/main.go", "zzz")).toEqual([]);
    expect(matchFileMentionPositions("src/main.go", "")).toEqual([]);
  });
});

describe("parseFileMentionToken", () => {
  it("parses valid unquoted boundaries and mid-token carets", () => {
    expect(parseFileMentionToken("@src/app", 8)).toEqual({
      start: 0,
      end: 8,
      query: "src/app",
      quoted: false,
    });
    expect(parseFileMentionToken("@", 1)).toEqual({
      start: 0,
      end: 1,
      query: "",
      quoted: false,
    });
    expect(parseFileMentionToken("check @foo/bar.ts", 17)).toEqual({
      start: 6,
      end: 17,
      query: "foo/bar.ts",
      quoted: false,
    });
    expect(parseFileMentionToken("line1\n@readme", 13)).toEqual({
      start: 6,
      end: 13,
      query: "readme",
      quoted: false,
    });
    for (const delim of ["(", "[", "{", "<", '"', "'", "`"]) {
      expect(parseFileMentionToken(`${delim}@foo`, 5)).toEqual({
        start: 1,
        end: 5,
        query: "foo",
        quoted: false,
      });
    }
    // Mid-token caret keeps query before caret while spanning the full token end.
    expect(parseFileMentionToken("open @src/file.ts later", 10)).toEqual({
      start: 5,
      end: 17,
      query: "src/",
      quoted: false,
    });
    expect(parseFileMentionToken("@src/main.go", 7)).toEqual({
      start: 0,
      end: 12,
      query: "src/ma",
      quoted: false,
    });
    expect(parseFileMentionToken("see @src/main.go, please", 11)).toEqual({
      start: 4,
      end: 16,
      query: "src/ma",
      quoted: false,
    });
    expect(parseFileMentionToken("see (@src/main.go.)", 12)).toEqual({
      start: 5,
      end: 17,
      query: "src/ma",
      quoted: false,
    });
    expect(parseFileMentionToken("edit @src/routes/[id]/page.tsx now", 20)).toEqual({
      start: 5,
      end: 30,
      query: "src/routes/[id",
      quoted: false,
    });
  });

  it('parses quoted @"... tokens including spaces and rejects invalid chars', () => {
    expect(parseFileMentionToken('@"my notes', 10)).toEqual({
      start: 0,
      end: 10,
      query: "my notes",
      quoted: true,
    });
    expect(parseFileMentionToken('open @"my notes"', 15)).toEqual({
      start: 5,
      end: 16,
      query: "my notes",
      quoted: true,
    });
    expect(parseFileMentionToken('open @"my notes" "other"', 10)).toEqual({
      start: 5,
      end: 16,
      query: "my ",
      quoted: true,
    });
    // Past closing quote is no longer active.
    expect(parseFileMentionToken('open @"my notes"', 16)).toBeNull();
    expect(parseFileMentionToken('open @"my\nnotes', 15)).toBeNull();
    expect(parseFileMentionToken('open @"my\rnotes', 15)).toBeNull();
    expect(parseFileMentionToken('foo@"bar', 8)).toBeNull();
    // Unclosed quoted query must end at caret and never consume trailing prose or cross a newline.
    const unclosedWithProse = 'check @"release existing prose';
    expect(parseFileMentionToken(unclosedWithProse, 'check @"release'.length)).toEqual({
      start: 6,
      end: 15,
      query: "release",
      quoted: true,
    });
    expect(parseFileMentionToken('check @"release\n"later"', 'check @"release'.length)).toEqual({
      start: 6,
      end: 15,
      query: "release",
      quoted: true,
    });
    // Unfinished quoted mention before another quoted phrase on the same line keeps end = caret.
    expect(parseFileMentionToken('check @"release and check "v2"', 'check @"release'.length)).toEqual({
      start: 6,
      end: 15,
      query: "release",
      quoted: true,
    });
    // Valid closed mentions before prose or punctuation still span through the closing quote.
    expect(parseFileMentionToken('check @"my notes" and "v2"', 11)).toEqual({
      start: 6,
      end: 17,
      query: "my ",
      quoted: true,
    });
    expect(parseFileMentionToken('check @"my notes", next', 11)).toEqual({
      start: 6,
      end: 17,
      query: "my ",
      quoted: true,
    });
    // Unfinished quoted mention before another @"..." reference preserves the trailing @" reference.
    expect(parseFileMentionToken('@"release @".env backups/config"', 9)).toEqual({
      start: 0,
      end: 9,
      query: "release",
      quoted: true,
    });
    expect(parseFileMentionToken('@"release @".env', 9)).toEqual({
      start: 0,
      end: 9,
      query: "release",
      quoted: true,
    });
    // Closed quoted mention before another @"..." reference still replaces [start, closeQuote + 1).
    expect(parseFileMentionToken('@"my notes" @".env backups/config"', 5)).toEqual({
      start: 0,
      end: 11,
      query: "my ",
      quoted: true,
    });
  });

  it('decodes escaped quotes and backslashes inside @"..." and supports dir drill-in round-trips', () => {
    expect(parseFileMentionToken('@"Q1 \\"drafts\\"/"', 16)).toEqual({
      start: 0,
      end: 17,
      query: 'Q1 "drafts"/',
      quoted: true,
    });
    expect(parseFileMentionToken('@"dir\\\\a \\"b\\""', 14)).toEqual({
      start: 0,
      end: 15,
      query: 'dir\\a "b"',
      quoted: true,
    });
    expect(parseFileMentionToken('@"Q1 \\"drafts\\""', 10)).toEqual({
      start: 0,
      end: 16,
      query: 'Q1 "dra',
      quoted: true,
    });
    // Past unescaped closing quote on escaped string is inactive.
    expect(parseFileMentionToken('@"Q1 \\"drafts\\""', 16)).toBeNull();
    // Paths containing @ (unquoted and quoted) remain active from their opening @.
    expect(parseFileMentionToken("see @app/@modal/page", 20)).toEqual({
      start: 4,
      end: 20,
      query: "app/@modal/page",
      quoted: false,
    });
    expect(parseFileMentionToken("see @app/@modal/page, next", 15)).toEqual({
      start: 4,
      end: 20,
      query: "app/@modal",
      quoted: false,
    });
    expect(parseFileMentionToken('@"app/@modal/"', 13)).toEqual({
      start: 0,
      end: 14,
      query: "app/@modal/",
      quoted: true,
    });
    expect(parseFileMentionToken('open @"my @notes"', 16)).toEqual({
      start: 5,
      end: 17,
      query: "my @notes",
      quoted: true,
    });
    expect(parseFileMentionToken('open @"@modal/"', 14)).toEqual({
      start: 5,
      end: 15,
      query: "@modal/",
      quoted: true,
    });
  });

  it("rejects email addresses, URLs, double-@, mid-word @, spaces, and >256 queries", () => {
    expect(parseFileMentionToken("user@example.com", 16)).toBeNull();
    expect(parseFileMentionToken("git@github.com:org/repo", 23)).toBeNull();
    expect(parseFileMentionToken("https://u:p@host", 16)).toBeNull();
    expect(parseFileMentionToken("@@foo", 5)).toBeNull();
    expect(parseFileMentionToken("@@", 2)).toBeNull();
    expect(parseFileMentionToken("foo@bar", 7)).toBeNull();
    expect(parseFileMentionToken("@ ", 2)).toBeNull();
    expect(parseFileMentionToken("user@example.com/@modal", 23)).toBeNull();
    expect(parseFileMentionToken("@@app/@modal", 12)).toBeNull();
    expect(parseFileMentionToken("@@modal/", 8)).toBeNull();
    expect(
      parseFileMentionToken(`@${"a".repeat(FILE_MENTION_MAX_QUERY_LENGTH + 1)}`, FILE_MENTION_MAX_QUERY_LENGTH + 2),
    ).toBeNull();
    expect(
      parseFileMentionToken(`@${"a".repeat(FILE_MENTION_MAX_QUERY_LENGTH)}`, FILE_MENTION_MAX_QUERY_LENGTH + 1),
    ).not.toBeNull();
  });
});

describe("spliceFileMention", () => {
  it("inserts plain file with trailing space or omits space before punctuation/whitespace", () => {
    const token: FileMentionToken = { start: 6, end: 11, query: "main", quoted: false };
    expect(spliceFileMention("check @main", token, { path: "cmd/main.go", kind: "file" })).toEqual({
      text: "check @cmd/main.go ",
      caret: 19,
      keepOpen: false,
    });
    expect(spliceFileMention("check @main, please", token, { path: "cmd/main.go", kind: "file" })).toEqual({
      text: "check @cmd/main.go, please",
      caret: 18,
      keepOpen: false,
    });
    expect(spliceFileMention("check @main next", token, { path: "cmd/main.go", kind: "file" })).toEqual({
      text: "check @cmd/main.go next",
      caret: 18,
      keepOpen: false,
    });

    // Mid-token splice replaces the entire reference without leaving tail fragments.
    const midPlain = parseFileMentionToken("see @src/main.go, please", 11)!;
    expect(spliceFileMention("see @src/main.go, please", midPlain, { path: "src/main.go", kind: "file" })).toEqual({
      text: "see @src/main.go, please",
      caret: 16,
      keepOpen: false,
    });
    const midStandalone = parseFileMentionToken("@src/main.go", 7)!;
    expect(spliceFileMention("@src/main.go", midStandalone, { path: "src/main.go", kind: "file" })).toEqual({
      text: "@src/main.go ",
      caret: 13,
      keepOpen: false,
    });
    const midQuoted = parseFileMentionToken('read @"my notes" "other"', 10)!;
    expect(spliceFileMention('read @"my notes" "other"', midQuoted, { path: "my notes.txt", kind: "file" })).toEqual({
      text: 'read @"my notes.txt" "other"',
      caret: 20,
      keepOpen: false,
    });

    // Unclosed quoted query preserves existing prose after caret.
    const unclosedToken = parseFileMentionToken('check @"release existing prose', 'check @"release'.length)!;
    expect(
      spliceFileMention('check @"release existing prose', unclosedToken, {
        path: "docs/release notes.md",
        kind: "file",
      }),
    ).toEqual({
      text: 'check @"docs/release notes.md" existing prose',
      caret: 30,
      keepOpen: false,
    });

    const unfinishedBeforeQuoted = parseFileMentionToken('check @"release and check "v2"', 'check @"release'.length)!;
    expect(
      spliceFileMention('check @"release and check "v2"', unfinishedBeforeQuoted, {
        path: "docs/release notes.md",
        kind: "file",
      }),
    ).toEqual({
      text: 'check @"docs/release notes.md" and check "v2"',
      caret: 30,
      keepOpen: false,
    });

    const closedBeforeQuoted = parseFileMentionToken('check @"my notes" and "v2"', 11)!;
    expect(
      spliceFileMention('check @"my notes" and "v2"', closedBeforeQuoted, { path: "my notes.md", kind: "file" }),
    ).toEqual({
      text: 'check @"my notes.md" and "v2"',
      caret: 20,
      keepOpen: false,
    });

    const beforeAtQuotedClosed = parseFileMentionToken('@"release @".env backups/config"', 9)!;
    expect(
      spliceFileMention('@"release @".env backups/config"', beforeAtQuotedClosed, {
        path: "docs/release notes.md",
        kind: "file",
      }),
    ).toEqual({
      text: '@"docs/release notes.md" @".env backups/config"',
      caret: 24,
      keepOpen: false,
    });

    const beforeAtQuotedUnclosed = parseFileMentionToken('@"release @".env', 9)!;
    expect(
      spliceFileMention('@"release @".env', beforeAtQuotedUnclosed, {
        path: "docs/release notes.md",
        kind: "file",
      }),
    ).toEqual({
      text: '@"docs/release notes.md" @".env',
      caret: 24,
      keepOpen: false,
    });

    const closedBeforeAtQuoted = parseFileMentionToken('@"my notes" @".env backups/config"', 5)!;
    expect(
      spliceFileMention('@"my notes" @".env backups/config"', closedBeforeAtQuoted, {
        path: "my notes.md",
        kind: "file",
      }),
    ).toEqual({
      text: '@"my notes.md" @".env backups/config"',
      caret: 14,
      keepOpen: false,
    });
  });

  it("quotes paths with spaces or double quotes and escapes backslashes/quotes", () => {
    const token: FileMentionToken = { start: 0, end: 4, query: "not", quoted: false };
    expect(spliceFileMention("@not", token, { path: "docs/my notes.md", kind: "file" })).toEqual({
      text: '@"docs/my notes.md" ',
      caret: 20,
      keepOpen: false,
    });
    expect(spliceFileMention("@not", token, { path: 'dir\\a "b".txt', kind: "file" })).toEqual({
      text: '@"dir\\\\a \\"b\\".txt" ',
      caret: 20,
      keepOpen: false,
    });
  });

  it("handles directories with trailing slash, keepOpen=true, and consumes existing closing quote", () => {
    const plainDirToken: FileMentionToken = { start: 5, end: 10, query: "exec", quoted: false };
    expect(spliceFileMention("open @exec", plainDirToken, { path: "agent/execenv", kind: "dir" })).toEqual({
      text: "open @agent/execenv/",
      caret: 20,
      keepOpen: true,
    });

    const quotedDirToken: FileMentionToken = { start: 0, end: 5, query: "my", quoted: true };
    expect(spliceFileMention('@"my"', quotedDirToken, { path: "my dir", kind: "dir" })).toEqual({
      text: '@"my dir/"',
      caret: 9,
      keepOpen: true,
    });

    const quotedFileToken: FileMentionToken = { start: 5, end: 15, query: "my file", quoted: true };
    expect(spliceFileMention('read @"my file" now', quotedFileToken, { path: "my file.txt", kind: "file" })).toEqual({
      text: 'read @"my file.txt" now',
      caret: 19,
      keepOpen: false,
    });

    const escapedDirSpliced = spliceFileMention(
      "@Q1",
      { start: 0, end: 3, query: "Q1", quoted: false },
      { path: 'Q1 "drafts"', kind: "dir" },
    );
    expect(escapedDirSpliced).toEqual({
      text: '@"Q1 \\"drafts\\"/"',
      caret: 16,
      keepOpen: true,
    });
    const reparsed = parseFileMentionToken(escapedDirSpliced.text, escapedDirSpliced.caret);
    expect(reparsed).toEqual({ start: 0, end: 17, query: 'Q1 "drafts"/', quoted: true });

    const atDirSpliced = spliceFileMention(
      "edit @app",
      { start: 5, end: 9, query: "app", quoted: false },
      { path: "app/@modal", kind: "dir" },
    );
    expect(atDirSpliced).toEqual({
      text: "edit @app/@modal/",
      caret: 17,
      keepOpen: true,
    });
    expect(parseFileMentionToken(atDirSpliced.text, atDirSpliced.caret)).toEqual({
      start: 5,
      end: 17,
      query: "app/@modal/",
      quoted: false,
    });

    // Leading-@ paths are quoted so selecting @modal produces @"@modal/" instead of invalid @@modal/.
    const leadingAtSpliced = spliceFileMention(
      "@m",
      { start: 0, end: 2, query: "m", quoted: false },
      { path: "@modal", kind: "dir" },
    );
    expect(leadingAtSpliced).toEqual({
      text: '@"@modal/"',
      caret: 9,
      keepOpen: true,
    });
    expect(parseFileMentionToken(leadingAtSpliced.text, leadingAtSpliced.caret)).toEqual({
      start: 0,
      end: 10,
      query: "@modal/",
      quoted: true,
    });
  });
});

describe("createFileMentionStore", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("debounces rapid typing at 80ms and runs immediate=true at 0ms", async () => {
    vi.useFakeTimers();
    const store = createFileMentionStore();
    const client = makeFakeRequestClient(async () => ({
      items: [{ path: "src/main.go", kind: "file" }],
    }));

    const stop1 = store.search({
      client,
      target: { kind: "session", ref: "local:s1" },
      query: "m",
      enabled: true,
    });
    await vi.advanceTimersByTimeAsync(FILE_MENTION_DEBOUNCE_MS - 10);
    expect(client.calls).toHaveLength(0);
    stop1();

    const stop2 = store.search({
      client,
      target: { kind: "session", ref: "local:s1" },
      query: "ma",
      enabled: true,
    });
    await vi.advanceTimersByTimeAsync(FILE_MENTION_DEBOUNCE_MS);
    expect(client.calls).toHaveLength(1);
    expect(client.calls[0]).toEqual({
      method: "evener/files/search",
      params: { ref: "local:s1", query: "ma", limit: FILE_MENTION_DEFAULT_LIMIT },
    });
    expect(store.getState().settledQuery).toBe("ma");
    stop2();

    store.search({
      client,
      target: { kind: "session", ref: "local:s1" },
      query: "",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(client.calls).toHaveLength(2);
    expect(store.getState().settledQuery).toBe("");
  });

  it("fences out-of-order responses and narrows optimistically without switching dotfile mode", async () => {
    vi.useFakeTimers();
    const store = createFileMentionStore();
    const first = makeDeferred<FilesSearchResponse>();
    const second = makeDeferred<FilesSearchResponse>();
    const third = makeDeferred<FilesSearchResponse>();
    let callIdx = 0;
    const client = makeFakeRequestClient(() => {
      callIdx += 1;
      if (callIdx === 1) return first.promise;
      if (callIdx === 2) return second.promise;
      return third.promise;
    });

    // 1. Seed settled results for "app".
    store.search({ client, target: { kind: "session", ref: "local:s1" }, query: "app", enabled: true });
    await vi.advanceTimersByTimeAsync(FILE_MENTION_DEBOUNCE_MS);
    first.resolve({
      items: [
        { path: "app/index.ts", kind: "file" },
        { path: "app/main.ts", kind: "file" },
      ],
      truncated: true,
    });
    await Promise.resolve();
    expect(store.getState().items.map((i) => i.path)).toEqual(["app/index.ts", "app/main.ts"]);

    // 2. Narrow to "app/m" -> optimistic re-rank immediately keeps app/main.ts without flashing empty.
    store.search({ client, target: { kind: "session", ref: "local:s1" }, query: "app/m", enabled: true });
    expect(store.getState().loading).toBe(true);
    expect(store.getState().items.map((i) => i.path)).toEqual(["app/main.ts"]);
    await vi.advanceTimersByTimeAsync(FILE_MENTION_DEBOUNCE_MS);

    // 3. Narrow further to "app/ma" before second resolves.
    store.search({ client, target: { kind: "session", ref: "local:s1" }, query: "app/ma", enabled: true });
    await vi.advanceTimersByTimeAsync(FILE_MENTION_DEBOUNCE_MS);

    // Stale second response arrives after third was dispatched and must be ignored.
    second.resolve({ items: [{ path: "stale.ts", kind: "file" }] });
    await Promise.resolve();
    expect(store.getState().loading).toBe(true);
    expect(store.getState().items.map((i) => i.path)).toEqual(["app/main.ts"]);

    third.resolve({ items: [{ path: "app/main.ts", kind: "file" }], truncated: false });
    await Promise.resolve();
    expect(store.getState()).toEqual({
      items: [{ path: "app/main.ts", kind: "file" }],
      loading: false,
      truncated: false,
      settledQuery: "app/ma",
    });

    // Switching dotfile mode clears optimistic cache rather than showing non-dotfile rows.
    store.search({ client, target: { kind: "session", ref: "local:s1" }, query: ".env", enabled: true });
    expect(store.getState().items).toEqual([]);

    // clear() and scope changes drop optimistic cache.
    store.clear();
    expect(store.getState()).toEqual({ items: [], loading: false, truncated: false, settledQuery: null });
  });

  it("applies exact directory-prefix scoping during optimistic narrowing from src to src/", async () => {
    vi.useFakeTimers();
    const store = createFileMentionStore();
    const dirResp = makeDeferred<FilesSearchResponse>();
    let callCount = 0;
    const client = makeFakeRequestClient(() => {
      callCount += 1;
      if (callCount === 1) {
        return Promise.resolve({
          items: [
            { path: "src", kind: "dir" },
            { path: "src/index.ts", kind: "file" },
            { path: "src-old/index.ts", kind: "file" },
          ],
          truncated: false,
        });
      }
      return dirResp.promise;
    });

    store.search({
      client,
      target: { kind: "session", ref: "local:s1" },
      query: "src",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();

    // Narrowing to "src/" must immediately drop src-old/index.ts from optimistic rows.
    store.search({
      client,
      target: { kind: "session", ref: "local:s1" },
      query: "src/",
      enabled: true,
      immediate: true,
    });
    expect(store.getState().loading).toBe(true);
    expect(store.getState().items).toEqual([{ path: "src/index.ts", kind: "file" }]);

    // Narrowing to "src/in" with only exact dir + sibling in cache never flashes src-old/index.ts.
    dirResp.resolve({
      items: [
        { path: "src", kind: "dir" },
        { path: "src-old/index.ts", kind: "file" },
      ],
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    store.search({ client, target: { kind: "session", ref: "local:s1" }, query: "src/in", enabled: true });
    expect(store.getState().items).toEqual([]);

    // Narrowing abbreviated fuzzy slash query sr -> sr/ without an sr dir keeps src/main.go.
    const srStore = createFileMentionStore();
    const srClient = makeFakeRequestClient(async () => ({
      items: [
        { path: "src", kind: "dir" },
        { path: "src/main.go", kind: "file" },
      ],
      truncated: false,
    }));
    srStore.search({
      client: srClient,
      target: { kind: "session", ref: "local:s1" },
      query: "sr",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    srStore.search({ client: srClient, target: { kind: "session", ref: "local:s1" }, query: "sr/", enabled: true });
    expect(srStore.getState().items).toEqual([{ path: "src/main.go", kind: "file" }]);
  });

  it("degrades quietly per (client, host) on MethodNotFound while keeping local and sibling hosts working", async () => {
    vi.useFakeTimers();
    const store = createFileMentionStore();
    const client1 = makeFakeRequestClient(async (method, params) => {
      if (method === "evener/host/request") {
        const p = params as HostRequestParams;
        if (p.host === "old-box") {
          throw new WireError("method not found", -32601, { evenerErrorInfo: "methodNotFound" });
        }
        return { items: [{ path: `${p.host}/ok.go`, kind: "file" }] };
      }
      const fp = params as { ref?: string; cwd?: string };
      if (fp.ref?.startsWith("old-box:")) {
        throw new WireError("method not found", -32601, { evenerErrorInfo: "methodNotFound" });
      }
      return { items: [{ path: "local/ok.go", kind: "file" }] };
    });

    // Remote host old-box returns -32601 -> settles quietly and suppresses future requests for old-box on client1.
    store.search({
      client: client1,
      target: { kind: "session", ref: "old-box:s1" },
      query: "a",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(store.getState()).toEqual({ items: [], loading: false, truncated: false, settledQuery: "a" });
    const countAfterFail = client1.calls.length;

    store.search({
      client: client1,
      target: { kind: "cwd", host: "old-box", cwd: "/repo" },
      query: "b",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(client1.calls.length).toBe(countAfterFail);

    // Local and sibling host new-box still issue requests and work normally on client1.
    store.search({
      client: client1,
      target: { kind: "session", ref: "local:s1" },
      query: "a",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(store.getState().items).toEqual([{ path: "local/ok.go", kind: "file" }]);

    store.search({
      client: client1,
      target: { kind: "cwd", host: "new-box", cwd: "/repo" },
      query: "a",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(store.getState().items).toEqual([{ path: "new-box/ok.go", kind: "file" }]);

    // New client instance or clearUnsupportedFileSearchHost recovers old-box automatically.
    clearUnsupportedFileSearchHost(client1, "old-box");
    store.search({
      client: client1,
      target: { kind: "cwd", host: "old-box", cwd: "/repo" },
      query: "a",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(client1.calls.length).toBeGreaterThan(countAfterFail + 2);

    const client2 = makeFakeRequestClient(async () => ({ items: [{ path: "upgraded/file.go", kind: "file" }] }));
    store.search({
      client: client2,
      target: { kind: "session", ref: "old-box:s1" },
      query: "a",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(store.getState().items).toEqual([{ path: "upgraded/file.go", kind: "file" }]);
  });

  it("settles quietly to empty on transport or unavailable errors", async () => {
    vi.useFakeTimers();
    const store = createFileMentionStore();
    const client = makeFakeRequestClient(async () => {
      throw new Error("socket closed");
    });

    store.search({
      client,
      target: { kind: "session", ref: "local:s1" },
      query: "foo",
      enabled: true,
      immediate: true,
    });
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(store.getState()).toEqual({ items: [], loading: false, truncated: false, settledQuery: "foo" });
  });
});
