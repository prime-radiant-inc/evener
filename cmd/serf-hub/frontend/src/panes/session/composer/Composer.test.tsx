import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { Composer } from "./Composer";
import { ENTER_TO_SEND_STORAGE_KEY } from "./enterToSendPref";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: FULL_CAPABILITIES, queue: {} },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

async function mountComposer(ref: string, overrides: Partial<Thread> = {}): Promise<FakeClient> {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  render(
    <>
      <Toast />
      <Composer ref={ref} />
    </>,
  );
  return fake;
}

beforeEach(() => {
  localStorage.clear();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function textarea(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: /message/i }) as HTMLTextAreaElement;
}

function submitButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: /^(send|queue)\b/i }) as HTMLButtonElement;
}

function steerButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: /^steer\b/i }) as HTMLButtonElement;
}

// --- basic surface ---------------------------------------------------------

test("renders a textarea with an accessible name", async () => {
  await mountComposer("ref_a");
  expect(textarea()).toBeTruthy();
});

test("restores a stored draft into the textarea on mount", async () => {
  localStorage.setItem("serf.composer.draft.v1.ref_a", "unsent thought");
  await mountComposer("ref_a");
  expect(textarea().value).toBe("unsent thought");
});

test("typing persists the draft under this ref's storage key", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  await user.type(textarea(), "hi");
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("hi");
});

// --- send / queue routing ---------------------------------------------------

test("idle session: submit button reads Send and posts turn/start with the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  await user.type(textarea(), "hello agent");
  expect(submitButton().textContent).toMatch(/send/i);
  await user.click(submitButton());

  await waitFor(() => {
    expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
  });
  const call = fake.calls.find((c) => c.method === "turn/start");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "hello agent" }] });
});

test("a successful send clears the textarea and its draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBeNull();
});

test("a failed send leaves the textarea text untouched and surfaces a toast", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => {
    throw new Error("daemon unreachable");
  });

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/send failed/i)).toBeTruthy());
  expect(textarea().value).toBe("hello");
});

test("active session with queue capability: submit button reads Queue and posts turn/queue", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  fake.on("turn/queue", () => ({}));

  await user.type(textarea(), "queued message");
  expect(submitButton().textContent).toMatch(/queue/i);
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true));
});

test("an ended session disables the submit button entirely (send unavailable)", async () => {
  await mountComposer("ref_a", { status: { type: "ended" } });
  expect(submitButton().disabled).toBe(true);
});

test("submitting an empty composer fires no request", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));
  expect(submitButton().disabled).toBe(true);
  await user.click(submitButton());
  // fake.calls already carries mountComposer's own thread/read hydration -
  // only the absence of an actual submit call is under test here.
  expect(fake.calls.filter((c) => c.method === "turn/start" || c.method === "turn/queue")).toHaveLength(0);
});

// --- keyboard shortcuts ------------------------------------------------------

test("Cmd+Enter always submits, regardless of the enterToSend preference", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  await user.type(textarea(), "quick send");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

test("bare Enter does not submit when enterToSend is off (default)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  await user.type(textarea(), "line one{Enter}");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
  expect(textarea().value).toBe("line one\n");
});

test("bare Enter submits when enterToSend is on", async () => {
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "1");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  await user.type(textarea(), "go{Enter}");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

test("Shift+Enter with an empty queue and text steers instead of submitting", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "steer this");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/steer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/steer");
  expect(call?.params).toMatchObject({ ref: "ref_a", expectedTurnId: "turn_1" });
});

test("with enterToSend on, Shift+Enter is a literal newline and does not steer", async () => {
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "1");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "abc");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
  expect(textarea().value).toBe("abc\n");
});

test("the submit kbd hint switches from Mod+Enter to a bare Enter when enterToSend is on", async () => {
  await mountComposer("ref_a");
  expect(submitButton().textContent).toMatch(/enter/i);

  cleanup();
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "1");
  await mountComposer("ref_a");
  const hint = submitButton().textContent ?? "";
  expect(hint.toLowerCase()).not.toContain("shift");
});

// --- steer / drain-as-steer routing -----------------------------------------

test("clicking steer with an empty textarea and empty queue is a focus-only no-op", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", () => ({}));
  fake.on("turn/drainAsSteer", () => ({}));

  await user.click(steerButton());

  expect(fake.calls.filter((c) => c.method === "turn/steer" || c.method === "turn/drainAsSteer")).toHaveLength(0);
  expect(document.activeElement).toBe(textarea());
});

test("clicking steer with a non-empty queue routes to drain-as-steer, carrying the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { depth: 2, preview: ["a", "b"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", () => ({}));

  await user.type(textarea(), "drain me");
  await user.click(steerButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "drain me" }] });
});

// The Steer BUTTON's disabled attribute blocks a mouse click during the
// window after status flips "active" but before activeTurnId arrives (the
// same isTurnActive gate as its own disabled condition), so a click never
// reaches the handler in that window.
test("steer button stays disabled during the window after status flips active but before activeTurnId arrives", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} }, // no activeTurnId yet
  });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "hi");
  expect(steerButton().disabled).toBe(true);
  await user.click(steerButton());

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

// Shift+Enter, unlike a button click, bypasses the disabled attribute
// entirely - it calls the same steer handler directly off the keydown
// event, exactly mirroring legacy's own "keyboard equivalent of clicking
// the steer button" (the SAME function, not a separately-gated path). The
// handler's own internal activeTurnId check is what still catches this
// window from the keyboard, where the button's disabled attribute cannot.
test("Shift+Enter with no active turn id shows a 'no active turn' toast rather than attempting a doomed steer", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} }, // no activeTurnId
  });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(screen.getByText(/no active turn/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("a queuedDrainPartial failure still clears the composer and shows a distinct toast", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { depth: 1 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/drainAsSteer", () => {
    throw new WireError("already queued, drain failed", -32013, { serfErrorInfo: "queuedDrainPartial" });
  });

  await user.type(textarea(), "partial");
  await user.click(steerButton());

  await waitFor(() => expect(screen.getByText(/drain failed after queueing/i)).toBeTruthy());
  expect(textarea().value).toBe("");
});

test("steer/interrupt are disabled when the turn is not active, even with capability true", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(steerButton().disabled).toBe(true);
});

test("the stop button is absent once the session has ended", async () => {
  await mountComposer("ref_a", { status: { type: "ended" } });
  expect(screen.queryByRole("button", { name: /^stop\b/i })).toBeNull();
});

// --- interrupt ---------------------------------------------------------------

test("clicking Stop calls turn/interrupt", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", () => ({}));

  await user.click(screen.getByRole("button", { name: /^stop\b/i }));

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true));
});

test("a failed interrupt surfaces a toast naming the action", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", () => {
    throw new Error("not interruptible right now");
  });

  await user.click(screen.getByRole("button", { name: /^stop\b/i }));

  await waitFor(() => expect(screen.getByText(/interrupt failed/i)).toBeTruthy());
});

// --- attachments (paste -> chip -> submit) ----------------------------------

function pastePngInto(el: HTMLElement, name = "shot.png"): void {
  const file = new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
  });
  el.dispatchEvent(event);
}

function installCanvasStubs(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    callback(new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" }));
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

test("pasting an image renders a removable attachment chip and inserts its marker", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("the chip's remove button names the specific attachment (filename + dimensions), not a bare 'Remove'", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "shot.png");
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  expect(screen.getByRole("button", { name: "Remove shot.png (4×4)" })).toBeTruthy();
});

test("a successful submit includes the pasted image as a base64 InputAttachment", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/start");
  const params = call?.params as { input: Array<{ type: string; mediaType?: string; data?: string }> };
  const imageEntry = params.input.find((i) => i.type === "image");
  expect(imageEntry?.mediaType).toBe("image/png");
  expect(typeof imageEntry?.data).toBe("string");
});

test("submitting while an attachment is still mid-encode is blocked with a toast, no request fires", async () => {
  // No canvas stubs installed: reencodeToPng's promise never resolves within
  // this test, so the item stays pending === true throughout.
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = () => {}; // never invokes its callback - decode never settles
  class NeverLoadsImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    src = "";
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = NeverLoadsImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};

  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/still processing/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("removing an attachment chip strips its marker from the textarea", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  await user.click(screen.getByRole("button", { name: /remove/i }));
  expect(textarea().value).toBe("");
});

// --- T3/T4 integration slots -------------------------------------------------
// Comments aren't queryable via the DOM - reading the component's own source
// is the only way to pin that these reserved regions exist, in the right
// relative order, for T3/T4 (or T6's integration pass) to fill in without
// ever needing to restructure this file's JSX.
test("reserves marked slots for T3's queue strip and T4's ask dock, dock above strip above the input row", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const source = readFileSync(join(here, "Composer.tsx"), "utf8");
  const askSlotIndex = source.indexOf("T4: ask dock");
  const queueSlotIndex = source.indexOf("T3: queue strip");
  expect(askSlotIndex).toBeGreaterThan(-1);
  expect(queueSlotIndex).toBeGreaterThan(-1);
  expect(askSlotIndex).toBeLessThan(queueSlotIndex);
});
