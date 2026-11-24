import { type FilesSearchParams, type FilesSearchResponse, WireError } from "@evener/appwire-client";
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { selectEditorText } from "../testing/editor";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import { installCanvasStubs, mountComposerWithHandle, testThread, textarea } from "./composerTestUtils";
import { readComposerDraft, writeComposerDraft } from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { resetQuoteInsertStoreForTests } from "./quoteInsert";

beforeAll(async () => {
  installLocalStorage(new MemoryStorage());
  await import("../index");
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetWorkspaceStoreForTests();
});

function fileMenu() {
  return screen.getByTestId("composer-file-menu");
}

function fileOptions() {
  return within(fileMenu()).getAllByRole("option");
}

function queryFileMenu() {
  return screen.queryByTestId("composer-file-menu");
}

test("typing @ queries evener/files/search, supports keyboard navigation, and Enter commits without submitting", async () => {
  const user = userEvent.setup();
  const ref = "ref_file_nav";
  const { fake } = await mountComposerWithHandle(
    ref,
    {},
    {
      prepare: (client) => {
        client.on("evener/files/search", () => ({
          items: [
            { path: "cmd/main.go", kind: "file" },
            { path: "agent/execenv/project_files.go", kind: "file" },
          ],
        }));
        client.on("turn/start", (params) => ({
          receipt: appliedReceipt(params.clientMutationId, "thread_a"),
          turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        }));
      },
    },
  );

  const editor = textarea();
  await user.type(editor, "check @");

  await waitFor(() => expect(fileOptions()).toHaveLength(2));
  const searchCall = fake.calls.find((call) => call.method === "evener/files/search");
  expect(searchCall?.params).toEqual({ ref, query: "", limit: 25 });

  expect(editor.getAttribute("aria-controls")).toBe(fileMenu().id);
  expect(editor.getAttribute("aria-activedescendant")).toBe(fileOptions()[0]?.id);
  expect(fileOptions()[0]?.getAttribute("aria-selected")).toBe("true");

  // ArrowDown moves highlight to second option; ArrowUp wraps back to first then second.
  await user.keyboard("{ArrowDown}");
  expect(fileOptions()[1]?.getAttribute("aria-selected")).toBe("true");
  expect(editor.getAttribute("aria-activedescendant")).toBe(fileOptions()[1]?.id);

  await user.keyboard("{ArrowUp}{ArrowUp}");
  expect(fileOptions()[1]?.getAttribute("aria-selected")).toBe("true");

  // Enter commits the selected file without submitting the form.
  await user.keyboard("{Enter}");
  expect(editor.textContent).toBe("check @agent/execenv/project_files.go ");
  expect(queryFileMenu()).toBeNull();
  expect(fake.calls.some((call) => call.method === "turn/start")).toBe(false);

  // Escape dismisses without modifying text.
  await user.type(editor, "@main");
  await waitFor(() => expect(fileOptions()).toHaveLength(2));
  await user.keyboard("{Escape}");
  expect(queryFileMenu()).toBeNull();
  expect(editor.textContent).toBe("check @agent/execenv/project_files.go @main");
});

test("Tab commits before punctuation without extra space and closes the menu cleanly", async () => {
  const user = userEvent.setup();
  const ref = "ref_file_punct";
  await mountComposerWithHandle(
    ref,
    {},
    {
      prepare: (client) => {
        client.on("evener/files/search", () => ({
          items: [{ path: "cmd/main.go", kind: "file" }],
        }));
      },
    },
  );

  const editor = textarea();
  await user.type(editor, "see @ma, please");
  selectEditorText(editor, "see @ma".length);
  await waitFor(() => expect(fileOptions()).toHaveLength(1));

  await user.keyboard("{Tab}");
  expect(editor.textContent).toBe("see @cmd/main.go, please");
  expect(queryFileMenu()).toBeNull();
});

test("selecting a directory inserts @dir/ and keeps menu open with immediate child search", async () => {
  const user = userEvent.setup();
  const ref = "ref_file_dir";
  const queries: string[] = [];
  await mountComposerWithHandle(
    ref,
    {},
    {
      prepare: (client) => {
        client.on("evener/files/search", (raw) => {
          const params = raw as FilesSearchParams;
          queries.push(params.query);
          if (params.query.startsWith("agent/execenv/")) {
            return {
              items: [{ path: "agent/execenv/project_files.go", kind: "file" }],
            };
          }
          return {
            items: [{ path: "agent/execenv", kind: "dir" }],
          };
        });
      },
    },
  );

  const editor = textarea();
  await user.type(editor, "in @exec");
  await waitFor(() => expect(fileOptions()).toHaveLength(1));

  await user.keyboard("{Enter}");
  expect(editor.textContent).toBe("in @agent/execenv/");
  await waitFor(() => expect(queries).toContain("agent/execenv/"));
  await waitFor(() => expect(fileOptions()[0]?.textContent).toContain("project_files.go"));
});

test("pasting @scope/pkg suppresses opening, caret move away closes menu, and click commits without blur", async () => {
  const user = userEvent.setup();
  const ref = "ref_file_paste_caret_click";
  const { fake } = await mountComposerWithHandle(
    ref,
    {},
    {
      prepare: (client) => {
        client.on("evener/files/search", () => ({
          items: [{ path: "src/app.tsx", kind: "file" }],
        }));
      },
    },
  );

  const editor = textarea();
  editor.focus();
  fireEvent.paste(editor, {
    clipboardData: {
      getData: (type: string) => (type === "text/plain" ? "@evener/appwire-client" : ""),
      types: ["text/plain"],
      files: [],
      items: [],
    },
  });
  expect(queryFileMenu()).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/files/search")).toBe(false);

  // Typing after prose opens menu; moving caret away closes it immediately.
  await user.type(editor, " read @app");
  await waitFor(() => expect(fileOptions()).toHaveLength(1));
  selectEditorText(editor, 2);
  await waitFor(() => expect(queryFileMenu()).toBeNull());

  // Move caret back to @app, reopen, and click option with mouse without blurring editor.
  await user.type(editor, " @app");
  await waitFor(() => expect(fileOptions()).toHaveLength(1));
  const opt = fileOptions()[0]!;
  fireEvent.mouseDown(opt);
  fireEvent.click(opt);
  expect(document.activeElement).toBe(editor);
  expect(editor.textContent).toContain("@src/app.tsx ");
  expect(queryFileMenu()).toBeNull();
});

test("slash menu and file mention menu are mutually exclusive with ARIA wiring switching", async () => {
  const user = userEvent.setup();
  const ref = "ref_file_slash_mutex";
  await mountComposerWithHandle(
    ref,
    {
      evener: {
        ...testThread(ref).evener,
        diagnostics: { commands: [{ name: "review", description: "review diff" }] },
      },
    },
    {
      prepare: (client) => {
        client.on("evener/files/search", () => ({
          items: [{ path: "src/index.ts", kind: "file" }],
        }));
      },
    },
  );

  const editor = textarea();
  await user.type(editor, "/rev");
  const slashMenu = screen.getByTestId("composer-slash-menu");
  expect(queryFileMenu()).toBeNull();
  expect(editor.getAttribute("aria-controls")).toBe(slashMenu.id);

  await user.type(editor, " @idx");
  await waitFor(() => expect(fileOptions()).toHaveLength(1));
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(editor.getAttribute("aria-controls")).toBe(fileMenu().id);
});

test("delays Loader by 150ms when zero rows, renders non-selectable truncation footer, and keeps zero-match menu closed", async () => {
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const ref = "ref_file_loader_trunc";

  let resolveSearch!: (resp: FilesSearchResponse) => void;
  const searchPromise = new Promise<FilesSearchResponse>((resolve) => {
    resolveSearch = resolve;
  });

  await mountComposerWithHandle(
    ref,
    {},
    {
      prepare: (client) => {
        client.on("evener/files/search", (raw) => {
          const params = raw as FilesSearchParams;
          if (params.query === "none") return { items: [] };
          return searchPromise;
        });
      },
    },
  );

  const editor = textarea();
  await user.type(editor, "@");
  await act(async () => {
    vi.advanceTimersByTime(0);
  });

  // Before 150ms, no spinner menu flashes open.
  await act(async () => {
    vi.advanceTimersByTime(100);
  });
  expect(queryFileMenu()).toBeNull();

  // Past 150ms, delayed Loader mounts with role="status".
  await act(async () => {
    vi.advanceTimersByTime(60);
  });
  expect(within(fileMenu()).getByRole("status")).toBeTruthy();
  expect(editor.getAttribute("aria-controls")).toBe(fileMenu().id);
  expect(editor.getAttribute("aria-activedescendant")).toBeNull();

  // Settling with truncated: true renders non-selectable role="note" footer.
  await act(async () => {
    resolveSearch({ items: [{ path: "src/a.go", kind: "file" }], truncated: true });
    await Promise.resolve();
  });
  expect(within(fileMenu()).queryByRole("status")).toBeNull();
  expect(fileOptions()).toHaveLength(1);
  expect(within(fileMenu()).getByRole("note")).toBeTruthy();

  // Zero settled matches keep the menu closed.
  await user.type(editor, "none");
  await act(async () => {
    vi.advanceTimersByTime(100);
    await Promise.resolve();
  });
  expect(queryFileMenu()).toBeNull();
});

test("inserted @path round-trips through draft persistence, queued-message Edit, and quiet MethodNotFound degradation", async () => {
  const user = userEvent.setup();
  const ref = "ref_file_roundtrip";
  const fake = await (async () => {
    const res = await mountComposerWithHandle(
      ref,
      {
        status: { type: "active" },
        evener: {
          ...testThread(ref).evener,
          activeTurnId: "turn_1",
          queue: {
            revision: 1,
            depth: 1,
            ids: ["q1"],
            texts: ["review @cmd/main.go"],
            preview: ["review @cmd/main.go"],
          },
        },
      },
      {
        prepare: (client) => {
          client.on("evener/files/search", () => {
            throw new WireError("method not found", -32601, { evenerErrorInfo: "methodNotFound" });
          });
          client.on("turn/cancelQueued", (params) => ({
            removedText: "review @cmd/main.go",
            receipt: appliedReceipt(params.clientMutationId, "thread_a"),
          }));
        },
      },
    );
    return res.fake;
  })();

  // Typing @ against an older hub returning -32601 stays ordinary prose with zero toasts.
  const editor = textarea();
  await user.type(editor, "open @main");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/files/search")).toBe(true));
  expect(queryFileMenu()).toBeNull();
  expect(getToasts()).toEqual([]);
  expect(readComposerDraft(ref).text).toBe("open @main");

  // Edit queued message carrying @cmd/main.go preserves the plain-text mention.
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await flushPendingTurnsProjectionForTests();
  expect(editor.textContent).toContain("review @cmd/main.go");
  expect(readComposerDraft(ref).text).toContain("review @cmd/main.go");

  // Also confirm plain @path draft survives remount intact alongside skill atoms.
  installCanvasStubs();
  writeComposerDraft("ref_file_remount", { text: "Run /review on @cmd/main.go", skillNames: ["review"] });
  cleanup();
  await mountComposerWithHandle("ref_file_remount", {
    evener: { ...testThread("ref_file_remount").evener, capabilities: { ...FULL_CAPABILITIES, skillInput: true } },
  });
  expect(textarea().textContent).toBe("Run /review on @cmd/main.go");
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
});
