import type { InputItem } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { useLayoutEffect } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
import { resetActivitySidebarStoreForTests } from "../../../shell/activitybar/activitySidebarStore";
import { paletteStore } from "../../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
import { createTestComposerSource } from "../testing/composerSource";
import { editorCursor, pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { resetComposerFocusStoreForTests } from "./composerFocus";
import {
  Composer,
  installCanvasStubs,
  mountComposer,
  mountComposerWithHandle,
  REVIEW_RELEASE_CATALOG,
  readResponse,
  seedRejectedRecovery,
  slashMenu,
  slashOptions,
  steerButton,
  submitButton,
  testThread,
  textarea,
} from "./composerTestUtils";
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
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  resetPendingTurnsStoreForTests();
  // askDockStore reconciles reactively off threadsStore (registered once at
  // module load - askDockStore.ts's own header comment), so its byRef map
  // outlives resetThreadsStoreForTests() the same way the toast store
  // outlives RTL's cleanup below: without this, a pending-ask batch minted
  // for "ref_a" in one test would still be sitting there for the next
  // test's own "ref_a" mount.
  resetAskDockStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  // useCommandCatalog is module state the same way, and is entirely unused
  // by every OTHER test in this file - only the slash-completion tests
  // below ever populate it - so resetting it here is purely additive
  // isolation, never a behavior change for the rest of the suite.
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  // The toast store is module state that outlives RTL's own cleanup, so a
  // toast pushed by one test would otherwise still be in the next test's
  // tree and make a getByText for the same message ambiguous.
  resetToastStoreForTests();
  paletteStore.setState({ open: false, query: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  // A narrow-layout test leaves its stub installed; jsdom has no real
  // ResizeObserver, so the honest baseline for the next test is none at all.
  delete (globalThis as { ResizeObserver?: typeof ResizeObserver }).ResizeObserver;
});

// The former "reserves marked slots for T3/T4" test lived here, pinning the
// two slots via source-text scraping (comments aren't queryable via the DOM,
// and there was nothing else there yet to query). Now that the wave
// integration has actually mounted <AskDock>/<QueueStrip> in those slots
// (Composer.integration.test.tsx), a real behavioral DOM-order assertion
// supersedes it - see that file's "the ask dock renders above the queue
// strip when both are visible at once" test.

// --- leading-"/" on an empty composer (product decision, SHOULD-FIX) -------
//
// "/" at the start of an empty composer used to preventDefault() and open
// the MODAL command palette instead of typing (floor §2.1) - which made the
// inline slash menu below unreachable in its single most common case. It is
// now always a literal keystroke, same as any other character: it lands in
// the draft and the inline menu opens off it, exactly like "/" typed
// anywhere else in a non-empty composer. Mod+K (AppShell.tsx) is the one
// remaining way to open the modal palette.

test('"/" at the start of an empty composer types a literal slash and opens the INLINE menu, not the modal palette', async () => {
  useCommandCatalog.setState({ commands: [{ name: "review", description: "review the diff" }] });
  const user = userEvent.setup();
  await mountComposer("ref_slash", {
    evener: {
      ...testThread("ref_slash").evener,
      diagnostics: { commands: [{ name: "review", description: "review the diff" }] },
    },
  });

  const editor = textarea();
  await user.type(editor, "/");

  expect(editor.textContent).toBe("/");
  expect(paletteStore.getState().open).toBe(false);
  expect(screen.getByTestId("composer-slash-menu")).toBeTruthy();
});

test('"/" in a NON-empty composer is a literal slash, not a palette trigger', async () => {
  const user = userEvent.setup();
  await mountComposer("ref_slash2");
  const editor = textarea();
  await user.type(editor, "hello");

  fireEvent.keyDown(editor, { key: "/" });

  expect(paletteStore.getState().open).toBe(false);
});

test("removing an attachment that joined a token to a chip keeps the editor and the draft agreeing", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const ref = "ref_attachment_joined_chip";
  writeComposerDraft(ref, { text: "Use /cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  // Stage an image directly against the chip, then type a token character
  // after it: the marker keeps the chip's label whole, so nothing separates yet.
  selectEditorText(editor, "Use /cleanup".length);
  pastePngInto(editor, "shot.png");
  await screen.findByRole("button", { name: "View shot.png" });
  await user.keyboard("d");
  expect(editor.textContent).toBe("Use /cleanup[image 1]d");

  // Removing the tile strips the marker, which joins the token to the chip.
  // The editor separates them again; the composer's value and draft must
  // follow the document rather than keep describing the joined text.
  await user.click(screen.getByRole("button", { name: "Remove shot.png" }));
  await waitFor(() => expect(editor.textContent).toBe("Use /cleanup d"));
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup d", skillNames: ["cleanup"] });
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
});

test("deleting the separator between two chips restores exactly one space", async () => {
  const user = userEvent.setup();
  const ref = "ref_shared_chip_boundary";
  const text = "Run /cleanup /cleanup.v2";
  writeComposerDraft(ref, { text, skillNames: ["cleanup", "cleanup.v2"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(2);

  // Removing the shared separator breaks both references at one boundary, so
  // the repair owes one space - not one per atom.
  selectEditorText(editor, "Run /cleanup ".length);
  await user.keyboard("{Backspace}");

  expect(editor.textContent).toBe(text);
  expect(readComposerDraft(ref)).toEqual({ text, skillNames: ["cleanup", "cleanup.v2"] });
});

test("a chip at the end of the draft does not reopen the slash menu when its separator is removed", async () => {
  const user = userEvent.setup();
  const ref = "ref_chip_without_separator";
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  const editor = textarea();

  await user.type(editor, "Run /skill-1");
  await user.click(slashOptions()[0]!);
  expect(readComposerDraft(ref)).toEqual({ text: "Run /skill-1 ", skillNames: ["skill-1"] });

  // A chip is a selection, not typed prose: dropping its separator leaves the
  // label as the last thing in the draft, and that must not read as a slash
  // command the user just started.
  await user.keyboard("{Backspace}");
  expect(readComposerDraft(ref)).toEqual({ text: "Run /skill-1", skillNames: ["skill-1"] });
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("committing a skill during an IME composition leaves the menu open rather than losing it", async () => {
  const user = userEvent.setup();
  const ref = "ref_skill_commit_composing";
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  const editor = textarea();

  await user.type(editor, "Run /skill-1");
  expect(slashOptions()).toHaveLength(1);

  fireEvent.compositionStart(editor);
  await user.click(slashOptions()[0]!);

  // The editor refuses an insertion mid-composition; dismissing the menu over
  // a token left as prose would claim a skill is staged that is not.
  expect(slashOptions()).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Run /skill-1", skillNames: [] });
  fireEvent.compositionEnd(editor);
});

test("a trailing slash token opens a completion menu merging session-scoped built-ins with the plugin command catalog", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash3", {
    evener: { ...testThread("ref_slash3").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });

  await user.type(textarea(), "hi /re");

  // "re" matches the built-in /reasoning-effort too (mergeSlashCommands puts
  // built-ins first), not just the two catalog entries. Fuzzy matching also
  // finds the command labels whose "r" and "e" are separated. /drain-as-steer
  // would match too, but the session is idle with nothing queued, so the
  // menu hides it the way it hides any unavailable built-in (scopeCommand's
  // canDrainQueue rule).
  expect(slashOptions().map((el) => el.textContent)).toEqual([
    expect.stringContaining("/reasoning-effort"),
    expect.stringContaining("/review"),
    expect.stringContaining("/release"),
    expect.stringContaining("/project"),
  ]);
});

// A fresh browser reads the owning session's user command through thread/read,
// without needing to open the controller palette.
test("a user command from the owning session autocompletes without a palette open", async () => {
  const user = userEvent.setup();
  await mountComposerWithHandle(
    "ref_slash_par",
    {
      evener: {
        ...testThread("ref_slash_par").evener,
        diagnostics: { commands: [{ name: "par", description: "adversarial review", source: "user" }] },
      },
    },
    {
      prepare: (fake) =>
        fake.on("evener/command/list", () => ({
          commands: [{ name: "par", description: "adversarial review", source: "user" }],
        })),
    },
  );

  await user.type(textarea(), "/pa");

  // /compact fuzzy-matches "pa" too; the claim is /par's presence, not the
  // whole list.
  await vi.waitFor(() => {
    expect(slashOptions().map((el) => el.textContent)).toContainEqual(expect.stringContaining("/par"));
  });
});

test("slash completion hides excluded plugin commands but keeps loaded plugin commands", async () => {
  useCommandCatalog.setState({
    commands: [
      { name: "review", description: "review the diff", source: "plugin", pluginName: "loaded" },
      { name: "revoke", description: "revoke access", source: "plugin", pluginName: "excluded" },
    ],
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_plugins", {
    evener: {
      ...testThread("ref_slash_plugins").evener,
      diagnostics: {
        commands: [{ name: "review", description: "review the diff", source: "plugin", pluginName: "loaded" }],
        plugins: [{ name: "loaded", skillCount: 0, agentCount: 0, hookCount: 0, mcpCount: 0 }],
      },
    },
  });

  await user.type(textarea(), "hi /rev");

  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("slash completion keeps built-ins while hiding plugin commands for an explicit empty inventory", async () => {
  useCommandCatalog.setState({
    commands: [
      { name: "review", description: "review the diff", source: "plugin", pluginName: "excluded" },
      { name: "release", description: "cut a release", source: "plugin", pluginName: "excluded" },
    ],
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_empty", {
    evener: {
      ...testThread("ref_slash_empty").evener,
      diagnostics: { commands: [], plugins: [] },
    },
  });

  await user.type(textarea(), "hi /re");

  expect(slashOptions().map((el) => el.textContent)).toEqual([
    expect.stringContaining("/reasoning-effort"),
    expect.stringContaining("/project"),
  ]);
});

test.each([undefined, []])(
  "live missing or empty owner inventory never falls back to controller user commands %s",
  async (commands) => {
    useCommandCatalog.setState({ commands: [{ name: "controller-only", source: "user" }] });
    const ref = "ref_owner_absent";
    await mountComposer(ref, { evener: { ...testThread(ref).evener, diagnostics: { commands } } });
    const user = userEvent.setup();
    await user.type(textarea(), "/controller-only");
    expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  },
);

test("skill completions keep indivisible chips in the sentence and submit both references", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_slash_skill", {
    evener: {
      ref: "ref_slash_skill",
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
          {
            name: "skill-2",
            description: "second skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  await user.type(editor, "Run /skill-1");
  expect(slashOptions()).toHaveLength(1);
  expect(slashOptions()[0]?.textContent).toContain("/skill-1");

  await user.click(slashOptions()[0]!);
  expect(readComposerDraft("ref_slash_skill")).toEqual({ text: "Run /skill-1 ", skillNames: ["skill-1"] });
  await user.type(editor, "and then /skill-2");
  await user.keyboard("{Tab}");
  expect(readComposerDraft("ref_slash_skill")).toEqual({
    text: "Run /skill-1 and then /skill-2 ",
    skillNames: ["skill-1", "skill-2"],
  });
  const chips = within(editor).getAllByTestId("composer-skill-chip");
  expect(chips.map((chip) => chip.textContent)).toEqual(["/skill-1", "/skill-2"]);
  expect(chips.every((chip) => chip.getAttribute("contenteditable") === "false")).toBe(true);
  // Remove the completion's trailing separator; the wire preserves draft text.
  await user.keyboard("{Backspace}");
  expect(readComposerDraft("ref_slash_skill")).toEqual({
    text: "Run /skill-1 and then /skill-2",
    skillNames: ["skill-1", "skill-2"],
  });
  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((candidate) => candidate.method === "turn/start");
  expect(call?.params).toMatchObject({
    ref: "ref_slash_skill",
    input: [
      { type: "text", text: "Run /skill-1 and then /skill-2" },
      { type: "skill", name: "skill-1" },
      { type: "skill", name: "skill-2" },
    ],
  });
});

test.each([
  ["project", "review", "plugin review", "project review", "project review"],
  ["plugin", "pkg:review", "plugin review", "project review", "plugin review"],
  ["descriptionless plugin collision", "review", undefined, "project review", "project review"],
  [
    "descriptionless project",
    "review",
    "plugin review",
    undefined,
    "review — no longer in this session's command catalog",
  ],
])(
  "command chip details use the canonical identity for %s",
  async (_case, name, pluginDescription, projectDescription, details) => {
    if (!name) throw new Error("command tooltip fixture requires a name");
    const ref = "ref_command_tooltip";
    writeComposerDraft(ref, { text: `Run /${name}`, skillNames: [], commandNames: [name] });
    await mountComposer(ref, {
      evener: {
        ...testThread(ref).evener,
        capabilities: { ...FULL_CAPABILITIES, commandInput: true },
        diagnostics: {
          commands: [
            { name: "review", source: "plugin", pluginName: "pkg", description: pluginDescription },
            { name: "review", source: "project", description: projectDescription },
          ],
        },
      },
    });

    const chip = textarea().querySelector("[data-command-name]");
    expect(chip?.getAttribute("data-command-name")).toBe(name);
    expect(chip?.getAttribute("title")).toBe(details);
    expect(chip?.getAttribute("aria-label")).toBe(`/${name}: ${details}`);
  },
);

test.each([undefined, []])("command chip details retain the missing owner catalog fallback %s", async (commands) => {
  const ref = "ref_missing_command_tooltip";
  writeComposerDraft(ref, { text: "Run /review", skillNames: [], commandNames: ["review"] });
  await mountComposer(ref, {
    evener: {
      ...testThread(ref).evener,
      capabilities: { ...FULL_CAPABILITIES, commandInput: true },
      diagnostics: { commands },
    },
  });

  const chip = textarea().querySelector("[data-command-name]");
  expect(chip?.getAttribute("data-command-name")).toBe("review");
  expect(chip?.getAttribute("title")).toBe("review — no longer in this session's command catalog");
  expect(chip?.getAttribute("aria-label")).toBe("/review: review — no longer in this session's command catalog");
});

test.each([true, false])("explicit commands persist through remount and respect commandInput %s", async (supported) => {
  const user = userEvent.setup();
  const ref = "ref_atomic_command";
  useCommandCatalog.setState({ commands: [{ name: "audit", source: "user", description: "command intent" }] });
  const options = {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, commandInput: supported },
      queue: { revision: 0 },
      diagnostics: { commands: [{ name: "audit", source: "user", description: "command intent" }] },
    },
  };
  await mountComposer(ref, options);
  await user.type(textarea(), "Before /aud");
  await user.click(slashOptions().find((option) => option.textContent?.includes("command intent"))!);
  await user.paste("after /audit");
  expect(textarea().textContent).toBe("Before /audit after /audit");
  expect(textarea().querySelectorAll("[data-command-name]")).toHaveLength(1);
  cleanup();
  const fake = await mountComposer(ref, options);
  expect(textarea().querySelectorAll("[data-command-name]")).toHaveLength(1);
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  if (supported) {
    expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({
      input: [
        { type: "text", text: "Before /audit after /audit" },
        { type: "command", name: "audit" },
      ],
    });
  } else {
    expect(fake.calls.some((call) => call.method === "turn/start")).toBe(false);
    expect(readComposerDraft(ref).commandNames).toEqual(["audit"]);
    expect(textarea().textContent).toBe("Before /audit after /audit");
  }
});

test.each(["same", "audit"])(
  "authoritative queue edit preserves selected %s atoms in a fresh client through delete and resend",
  async (name) => {
    const user = userEvent.setup();
    const ref = `ref_authoritative_${name}`;
    const text = name === "same" ? "/same /same /same" : "/audit /audit";
    const mentions = [
      { kind: "command" as const, name, offset: 0 },
      ...(name === "same" ? [{ kind: "skill" as const, name, offset: 6 }] : []),
    ];
    const { buildComposerInput } = await import("@evener/appwire-client");
    const input = buildComposerInput(text, [], name === "same" ? [name] : [], [name], mentions);
    // JSON is the accepted queue representation, with no local draft/outbox.
    const accepted = JSON.parse(JSON.stringify(input));
    const fake = await mountComposer(ref, {
      evener: {
        ref,
        capabilities: { ...FULL_CAPABILITIES, commandInput: true, skillInput: true },
        queue: {
          revision: 1,
          depth: 1,
          ids: ["q1"],
          texts: [accepted[0].text],
          preview: [text],
          commandNames: [[name]],
          skillNames: [name === "same" ? [name] : []],
          mentions: [accepted[0].mentions ?? []],
        },
      },
    });
    fake.on("turn/cancelQueued", (params) => ({
      removedText: text,
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    }));
    fake.on("turn/start", (params) => ({
      receipt: appliedReceipt(params.clientMutationId, "thread_a"),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    }));
    await user.click(screen.getByRole("button", { name: "Edit message" }));
    await flushPendingTurnsProjectionForTests();
    expect(readComposerDraft(ref).mentions).toEqual(mentions);
    expect(textarea().querySelectorAll("[data-command-name]")).toHaveLength(1);
    expect(textarea().querySelectorAll("[data-skill-name]")).toHaveLength(name === "same" ? 1 : 0);
    await selectEditorText(textarea(), name.length + 1);
    await user.keyboard("{Backspace}");
    expect(textarea().querySelectorAll("[data-command-name]")).toHaveLength(0);
    await user.click(submitButton());
    await flushPendingTurnsProjectionForTests();
    const sent = (fake.calls.find((call) => call.method === "turn/start")?.params as { input: InputItem[] } | undefined)
      ?.input;
    expect(sent?.filter((item) => item.type === "command")).toEqual([]);
    expect(sent?.filter((item) => item.type === "skill").map((item) => item.name)).toEqual(
      name === "same" ? [name] : [],
    );
    expect(sent?.[0]?.text).toBe(name === "same" ? " /same /same" : " /audit");
  },
);

test("queue edit retains current command and skill atom locations without activating duplicate prose", async () => {
  const user = userEvent.setup();
  const ref = "ref_queue_atom_intent";
  writeComposerDraft(ref, {
    text: "/same and /same and /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 0 },
      { kind: "skill", name: "same", offset: 10 },
    ],
  });
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, commandInput: true, skillInput: true },
      activeTurnId: "turn_1",
      queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["queued prose"], preview: ["queued prose"] },
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    removedText: "queued prose",
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
  }));
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await flushPendingTurnsProjectionForTests();
  expect(textarea().textContent).toBe("/same and /same and /same\n\nqueued prose");
  expect(textarea().querySelectorAll("[data-command-name]")).toHaveLength(1);
  expect(textarea().querySelectorAll("[data-skill-name]")).toHaveLength(1);
  expect(readComposerDraft(ref).mentions).toEqual([
    { kind: "command", name: "same", offset: 0 },
    { kind: "skill", name: "same", offset: 10 },
  ]);
});

test("repeated inline skills survive remount and undo while deletion reconciles activation", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_repeat";
  const original = "Run /skill-1 and /skill-1";
  writeComposerDraft(ref, { text: original, skillNames: ["skill-1"] });
  await mountComposer(ref);
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(2);

  await selectEditorText(textarea(), "Run /skill-1".length);
  await user.keyboard("{Backspace}");
  expect(readComposerDraft(ref)).toEqual({ text: "Run  and /skill-1", skillNames: ["skill-1"] });
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  await user.keyboard("{Control>}z{/Control}");
  expect(readComposerDraft(ref)).toEqual({ text: original, skillNames: ["skill-1"] });
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(2);

  replaceEditorText(textarea(), "");
  expect(readComposerDraft(ref)).toEqual({ text: "", skillNames: [] });
  await user.keyboard("{Control>}z{/Control}");
  expect(readComposerDraft(ref)).toEqual({ text: original, skillNames: ["skill-1"] });

  cleanup();
  render(<Composer ref={ref} source={createTestComposerSource(ref)} focused={false} />);
  await settleActivityDiscovery(ref);
  expect(textarea().textContent).toBe(original);
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(2);
});

test("a token typed directly against a chip is separated so the reference stays whole", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_adjacent_token";
  writeComposerDraft(ref, { text: "Use /cleanup ", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);

  // Step over the completion's own separator so the caret sits directly
  // against the chip, then type a token character into that position.
  await selectEditorText(editor, "Use /cleanup ".length);
  await user.keyboard("{Backspace}");
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup", skillNames: ["cleanup"] });

  await selectEditorText(editor, "Use /cleanup".length);
  await user.keyboard("d");

  // `/cleanupd` is not a reference to `cleanup`, so the two are separated: the
  // label stays whole, the activation stays with it, and nothing typed is lost.
  expect(editor.textContent).toBe("Use /cleanup d");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup d", skillNames: ["cleanup"] });

  // The same holds after a re-derivation, which re-reads the persisted value.
  cleanup();
  render(<Composer ref={ref} source={createTestComposerSource(ref)} focused={false} />);
  await settleActivityDiscovery(ref);
  expect(textarea().textContent).toBe("Use /cleanup d");
  expect(within(textarea()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup d", skillNames: ["cleanup"] });
});

test("one undo removes a typed character and the separator the chip needed with it", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_adjacent_undo";
  writeComposerDraft(ref, { text: "Use /cleanup", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();

  await selectEditorText(editor, "Use /cleanup".length);
  await user.keyboard("d");
  expect(editor.textContent).toBe("Use /cleanup d");

  // The separator exists only because of the typed character, so one undo has
  // to take both: leaving `/cleanupd` behind would be a state the parser reads
  // as prose, and re-separating it would make the undo look like a no-op.
  await user.keyboard("{Control>}z{/Control}");
  expect(editor.textContent).toBe("Use /cleanup");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup", skillNames: ["cleanup"] });
});

test("a character that already bounds the reference is left exactly as typed", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_bounding_character";
  writeComposerDraft(ref, { text: "Use /cleanup ", skillNames: ["cleanup"] });
  await mountComposer(ref);
  const editor = textarea();

  await selectEditorText(editor, "Use /cleanup ".length);
  await user.keyboard("{Backspace}");
  await selectEditorText(editor, "Use /cleanup".length);
  await user.keyboard(",");

  expect(editor.textContent).toBe("Use /cleanup,");
  expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  expect(readComposerDraft(ref)).toEqual({ text: "Use /cleanup,", skillNames: ["cleanup"] });
});

test.each([
  { text: "Use ", skillNames: ["simplify"], chips: [], input: [{ type: "text", text: "Use " }] },
  {
    text: "Use /plugin:visible, then /plugin:visible; /plugin:hidden/extra and /unselected",
    skillNames: ["plugin:hidden", "plugin:visible", "simplify"],
    chips: ["/plugin:visible", "/plugin:visible"],
    input: [
      { type: "text", text: "Use /plugin:visible, then /plugin:visible; /plugin:hidden/extra and /unselected" },
      { type: "skill", name: "plugin:visible" },
    ],
  },
])("restored selections: persisted $text submits only complete visible selected references", async (fixture) => {
  const ref = "ref_restored_selections";
  writeComposerDraft(ref, { text: fixture.text, skillNames: fixture.skillNames });
  const user = userEvent.setup();
  const fake = await mountComposer(ref, {
    evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: true }, queue: { revision: 0 } },
  });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
  }));
  const editor = textarea();
  expect(editor.textContent).toBe(fixture.text);
  expect(
    within(editor)
      .queryAllByTestId("composer-skill-chip")
      .map((chip) => chip.textContent),
  ).toEqual(fixture.chips);
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({ input: fixture.input });
});

test.each(["before render", "before subscription"] as const)(
  "restored selections: a selection-only draft arriving %s is never sendable",
  async (arrival) => {
    const ref = "ref_selection_only";
    const draft = { text: "", skillNames: ["simplify"] };
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () =>
      readResponse(ref, {
        evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: true }, queue: { revision: 0 } },
      }),
    );
    await threadsStore.getState().ensureThread(ref);
    if (arrival === "before render") writeComposerDraft(ref, draft);
    let initialSendDisabled: boolean | undefined;
    function SeedBeforeSubscription() {
      useLayoutEffect(() => {
        if (arrival === "before subscription") writeComposerDraft(ref, draft);
      }, []);
      return null;
    }
    function ObserveFirstCommit() {
      useLayoutEffect(() => {
        initialSendDisabled = submitButton().disabled;
      }, []);
      return null;
    }
    render(
      <>
        <SeedBeforeSubscription />
        <Composer ref={ref} source={createTestComposerSource(ref)} focused={false} />
        <ObserveFirstCommit />
      </>,
    );
    await flushPendingTurnsProjectionForTests();
    expect(textarea().textContent).toBe("");
    expect(within(textarea()).queryAllByTestId("composer-skill-chip")).toHaveLength(0);
    // First-commit state covers the lazy initializer independently of the
    // subscription-time reread, which can synchronously schedule another render.
    expect(initialSendDisabled).toBe(true);
    expect(submitButton().disabled).toBe(true);
    await userEvent.setup().click(submitButton());
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  },
);

test("a leading skill that shares a builtin name remains message input", async () => {
  const user = userEvent.setup();
  const ref = "ref_inline_builtin";
  writeComposerDraft(ref, { text: "/clear keep this reference", skillNames: ["clear"] });
  const fake = await mountComposer(ref, {
    evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: true }, queue: { revision: 0 } },
  });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await user.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({
    input: [
      { type: "text", text: "/clear keep this reference" },
      { type: "skill", name: "clear" },
    ],
  });
  expect(fake.calls.some((call) => call.method === "thread/clear")).toBe(false);
});

// model.skills mirrors thread.evener.diagnostics.skills, and the daemon only
// publishes entries that are available AND user-invocable (agent/status.go) -
// so a catalog entry reaching this tooltip is always usable. An "unavailable"
// or "not user-invocable" diagnostic would describe a state the wire cannot
// carry; the description is the whole tooltip.
test("a selected skill's tooltip never invents an unavailable or non-user-invocable diagnostic", async () => {
  const ref = "ref_skill_tip_usable";
  writeComposerDraft(ref, { text: "/simplify", skillNames: ["simplify"] });
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "simplify",
            description: "rewrite",
            disableModelInvocation: false,
            userInvocable: false,
            available: false,
          },
        ],
      },
    },
  });

  expect(within(textarea()).getByTestId("composer-skill-chip").getAttribute("title")).toBe("rewrite");
});

// The one diagnostic that CAN happen: the selection outlives the catalog
// report that backed it, so the tooltip names the skill and says why it is
// absent.
test("a selected skill the catalog no longer reports says so in its tooltip", async () => {
  const ref = "ref_skill_tip_missing";
  writeComposerDraft(ref, { text: "/vanished", skillNames: ["vanished"] });
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: { skills: [] },
    },
  });

  expect(within(textarea()).getByTestId("composer-skill-chip").getAttribute("title")).toBe(
    "vanished — no longer in this session's skill catalog",
  );
});

test("a token with no catalog match shows no menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash6", {
    evener: { ...testThread("ref_slash6").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });

  await user.type(textarea(), "hi /zzz");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("ArrowDown/ArrowUp move the highlighted option and wrap at both ends", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash7", {
    evener: { ...testThread("ref_slash7").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  await user.type(textarea(), "hi /re");
  // Four matches: three contiguous beginnings, then one fuzzy match
  // (/drain-as-steer would be a second, but it is unavailable on an idle
  // session with nothing queued and the menu hides it).
  expect(slashOptions()).toHaveLength(4);

  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[2]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[3]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}"); // wraps past the last option back to the first
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowUp}"); // wraps the other way, back to the last
  expect(slashOptions()[3]?.getAttribute("aria-selected")).toBe("true");
});

test("Tab commits the highlighted option: splices /name<space> at the token start, caret after the space", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash8", {
    evener: { ...testThread("ref_slash8").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  const editor = textarea();
  await user.type(editor, "hi /re");
  // index 0 is the built-in /reasoning-effort, 1 /review, 2 /release.
  await user.keyboard("{ArrowDown}{ArrowDown}"); // highlight "release"

  await user.keyboard("{Tab}");

  expect(editor.textContent).toBe("hi /release ");
  expect(editorCursor(editor)).toBe("hi /release ".length);
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(document.activeElement).toBe(editor);
});

test("committing a plugin-sourced catalog entry inserts the QUALIFIED /plugin:name invocation, not the bare name", async () => {
  // The unqualified "/name" form only resolves to the FIRST plugin
  // registering that name on the hub side (app_rpc.go's dispatch) - see
  // shell/palette/commands.ts's slashCommandInvocation, the single source
  // of truth this insert and the modal palette's own activateCommand both
  // go through. Queries "/rev" rather than "/re" so the built-in
  // /reasoning-effort (which also starts with "re") never enters this
  // single-match scenario.
  useCommandCatalog.setState({
    commands: [{ name: "review", description: "review the diff", source: "plugin", pluginName: "p" }],
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_qualified", {
    evener: {
      ...testThread("ref_slash_qualified").evener,
      diagnostics: {
        commands: [{ name: "review", description: "review the diff", source: "plugin", pluginName: "p" }],
        plugins: [{ name: "p", skillCount: 0, agentCount: 0, hookCount: 0, mcpCount: 0 }],
      },
    },
  });
  const editor = textarea();
  await user.type(editor, "hi /rev");

  await user.keyboard("{Tab}");

  expect(editor.textContent).toBe("hi /p:review ");
  expect(editorCursor(editor)).toBe("hi /p:review ".length);
});

test("Enter commits the highlighted option and does NOT fall through to the composer's send routing", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  const fake = await mountComposer("ref_slash9", {
    status: { type: "idle" },
    evener: { ...testThread("ref_slash9").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  const editor = textarea();
  await user.type(editor, "hi /rev"); // single match (review) - avoids the /reasoning-effort built-in collision on "re"

  await user.keyboard("{Enter}");

  expect(editor.textContent).toBe("hi /review ");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("Escape closes the menu without clearing the draft, and typing further reopens it", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash10", {
    evener: { ...testThread("ref_slash10").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  const editor = textarea();
  await user.type(editor, "hi /re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  await user.keyboard("{Escape}");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(editor.textContent).toBe("hi /re"); // draft untouched

  await user.type(editor, "v");

  expect(editor.textContent).toBe("hi /rev");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("blur closes the menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash11", {
    evener: { ...testThread("ref_slash11").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  const editor = textarea();
  await user.type(editor, "hi /re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  fireEvent.blur(editor);

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("clicking an option commits it without ever blurring the textarea", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash12", {
    evener: { ...testThread("ref_slash12").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  const editor = textarea();
  await user.type(editor, "hi /re");
  // index 0 is the built-in /reasoning-effort, 1 /review, 2 /release.

  await user.click(slashOptions()[2]!); // "release"

  expect(editor.textContent).toBe("hi /release ");
  expect(document.activeElement).toBe(editor);
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("the open menu wires listbox/option roles and aria-activedescendant on the textarea", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  await mountComposer("ref_slash13", {
    evener: { ...testThread("ref_slash13").evener, diagnostics: { commands: REVIEW_RELEASE_CATALOG } },
  });
  const editor = textarea();
  await user.type(editor, "hi /re");

  expect(slashMenu().getAttribute("role")).toBe("listbox");
  const activeId = editor.getAttribute("aria-activedescendant");
  expect(activeId).toBeTruthy();
  expect(document.getElementById(activeId ?? "")).toBe(slashOptions()[0]);

  await user.keyboard("{Escape}");
  expect(editor.getAttribute("aria-activedescendant")).toBeNull();
});

test("closing the slash menu removes both of the editor's optional ARIA references", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG });
  const user = userEvent.setup();
  const ref = "ref_slash_aria_cleanup";
  await mountComposer(ref, {
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  const editor = textarea();

  await user.type(editor, "hi /re");
  const listboxId = editor.getAttribute("aria-controls");
  expect(listboxId).toBeTruthy();
  expect(document.getElementById(listboxId ?? "")).toBe(slashMenu());
  expect(editor.getAttribute("aria-activedescendant")).toBeTruthy();

  // Closing must REMOVE the attributes rather than leave empty ones: an empty
  // reference still points assistive technology at a menu that is gone.
  await user.keyboard("{Escape}");
  expect(editor.hasAttribute("aria-controls")).toBe(false);
  expect(editor.hasAttribute("aria-activedescendant")).toBe(false);

  // Completion closes the menu by the other route, and must clean up the same.
  await user.type(editor, " /skill-1");
  expect(slashOptions()).toHaveLength(1);
  await user.click(slashOptions()[0]!);
  expect(editor.hasAttribute("aria-controls")).toBe(false);
  expect(editor.hasAttribute("aria-activedescendant")).toBe(false);
});

// --- Enter/submit interception: the composer as the session command line
// (2026-08-14 decision, "the composer is where you act on this session") ---
//
// A draft that PARSES as a known BUILT-IN session command runs that
// command's RPC instead of being sent as a chat message - matching Slack/
// Discord muscle memory (decisions.md). Plugin catalog commands and any
// unrecognized "/name" keep sending as plain text: the escape hatch.

test("a built-in invocation (/goal) runs the RPC instead of sending, and clears the draft on success", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_goal");
  let goalCall: unknown;
  fake.on("goal/set", (params) => {
    goalCall = params;
    return { started: false };
  });

  const editor = textarea();
  await user.type(editor, "/goal fix the login bug");
  await user.click(submitButton());

  await waitFor(() => expect(editor.textContent).toBe(""));
  expect(goalCall).toEqual({ ref: "ref_builtin_goal", objective: "fix the login bug" });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(localStorage.getItem("evener.composer.draft.v1.ref_builtin_goal")).toBeNull();
});

test("a successful /goal response fallback shows the goal row without rehydration", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_goal_chip");
  fake.on("goal/set", () => ({ started: true }));

  await user.type(textarea(), "/goal ship the demo");
  await user.click(submitButton());

  // This fake emits no notification and never rehydrates the thread, so this
  // exercises the goal/set response fallback stored in ThreadModel. Separate
  // store tests prove an accepted push or hydration invalidates that
  // fallback. The composer's goal readout is the CurrentWork goal row now:
  // the goal chip left the composer row with Jesse's 2026-09-28 "drop goal
  // inline in the composer" ruling.
  await waitFor(() => expect(screen.getByTestId("current-work-goal")).toBeTruthy());
  expect(screen.getByTestId("current-work-goal-value").textContent).toBe("ship the demo");
});

test("a failed built-in invocation preserves the draft and toasts a friendly message", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_fail");
  fake.on("goal/set", () => {
    throw new Error("boom");
  });

  const editor = textarea();
  await user.type(editor, "/goal fix the login bug");
  await user.click(submitButton());

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  // The friendly message replaces the raw handler error.
  expect(getToasts()[0]?.text).not.toContain("boom");
  expect(editor.textContent).toBe("/goal fix the login bug");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("an argless built-in invocation (/compact) runs and clears the draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_compact");
  let compactCalled = false;
  fake.on("thread/compact/start", () => {
    compactCalled = true;
    return {};
  });

  const editor = textarea();
  await user.type(editor, "/compact");
  await user.click(submitButton());

  await waitFor(() => expect(editor.textContent).toBe(""));
  expect(compactCalled).toBe(true);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// /steer on an idle session is unavailable (scopeCommand applies the
// running-turn rule the handler uses, since the hub's steer capability is
// harness support alone), and a typed invocation for an unavailable built-in
// gets the honest "not available right now" rather than the handler's floor
// message or a plain-message send.
test("an unavailable built-in (/steer while idle) is refused with the unavailable message, draft preserved", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_steer", { status: { type: "idle" } });

  const editor = textarea();
  await user.type(editor, "/steer go left");
  await user.click(submitButton());

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(editor.textContent).toBe("/steer go left");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("an unavailable /steer over an active recovery is refused without consuming it", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  await mountComposer("ref_a", { status: { type: "idle" } });
  await flushPendingTurnsProjectionForTests();
  replaceEditorText(textarea(), "/steer go left");
  fireEvent.click(submitButton());
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  expect(toastKinds()).toContain("error");
  expect(textarea().textContent).toBe("/steer go left");
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeDefined();
});

test("an unknown /foo sends as a plain message - the escape hatch", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_unknown", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  await user.type(editor, "/foo bar");
  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
  expect(editor.textContent).toBe("");
});

test("a plugin catalog command still sends as text - only BUILT-INS are intercepted", async () => {
  useCommandCatalog.setState({
    commands: [{ name: "review", description: "review the diff", source: "plugin" }],
  });
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_plugin", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  await user.type(editor, "/review please");
  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
  expect(editor.textContent).toBe("");
  expect(fake.calls.some((call) => call.method === "goal/set")).toBe(false);
});

test("a message carrying an attachment is never read as a command invocation, even if the text looks like one", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_attachment", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread_a"),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  const editor = textarea();
  await user.type(editor, "/goal fix it ");
  pastePngInto(editor);
  await waitFor(() => expect(editor.textContent).toBe("/goal fix it [image 1]"));

  await user.click(submitButton());

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
  expect(fake.calls.some((call) => call.method === "goal/set")).toBe(false);
});

// The skillInput gate reads this snapshot's capabilities unconditionally, so
// Queue, Steer and Drain refuse a staged selection on a target that never
// advertised the capability rather than deferring to the store-side throw
// ("skill selections are not supported on this target"), which surfaced as a
// generic "<verb> failed". The mount is an unfenced ACTIVE session: since the
// recovery fence began covering active snapshots too (it refuses the press
// before any verb-specific gate - see the active-fenced tests above), a fenced
// mount can no longer reach this gate at all, so the gate's ordering is pinned
// here on the path that still routes.
test.each([
  { label: "Queue", queue: { revision: 0 }, control: "submit" as const, method: "turn/queue" },
  { label: "Steer", queue: { revision: 0 }, control: "steer" as const, method: "turn/steer" },
  { label: "Drain", queue: { revision: 0, depth: 1 }, control: "steer" as const, method: "turn/drainAsSteer" },
])("a staged skill on $label hears the capability refusal", async ({ label, queue, control, method }) => {
  const user = userEvent.setup();
  const ref = `local:skill-gate-${label.toLowerCase()}`;
  // A selection is staged only as a complete chip in the document - main's
  // parser never reconstructs a hidden name that the text does not spell -
  // so the reference has to be present for the gate below to see a skill.
  writeComposerDraft(ref, { text: "skillful action /pkg:probe", skillNames: ["pkg:probe"] });
  const fake = await mountComposer(ref, {
    status: { type: "active" },
    evener: {
      ref,
      capabilities: { ...FULL_CAPABILITIES, skillInput: false },
      queue,
      activeTurnId: "turn_1",
      mutationStateAuthoritative: false,
    },
  });
  const editor = textarea();
  expect(within(editor).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");

  await user.click(control === "submit" ? submitButton() : steerButton());

  expect(toastKinds()).toEqual(["error"]);
  expect(fake.calls.filter((call) => call.method === method)).toHaveLength(0);
  expect(within(editor).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");
});

// The whole refusal contract on Send, against real durable storage: the draft's
// text and chip stay, the user hears why, nothing reaches the wire, and nothing
// durable is written for the refused press - no outbox, optimistic or recovery
// row a later reconnect could replay.
test("a staged skill on Send to a target without skillInput keeps the draft and writes nothing durable", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const ref = "local:skill-gate-send";
  writeComposerDraft(ref, { text: "aimed at a target without skills /pkg:probe", skillNames: ["pkg:probe"] });
  const fake = await mountComposer(ref, {
    status: { type: "idle" },
    evener: { ref, capabilities: { ...FULL_CAPABILITIES, skillInput: false }, queue: { revision: 0 } },
  });
  const editor = textarea();
  expect(within(editor).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");

  await user.click(submitButton());

  expect(toastKinds()).toEqual(["error"]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  expect(editor.textContent).toContain("aimed at a target without skills");
  expect(within(editor).getByTestId("composer-skill-chip").textContent).toContain("pkg:probe");
  expect(await storage.listOutbox(ref)).toEqual([]);
  expect(await storage.listOptimistic(ref)).toEqual([]);
  expect(await storage.listRecovery(ref)).toEqual([]);
});
