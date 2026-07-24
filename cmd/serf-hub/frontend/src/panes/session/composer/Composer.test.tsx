import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { paletteStore } from "../../../shell/palette/paletteController";
import { connectionStore } from "../../../stores/connection";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { Composer } from "./Composer";

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
  resetPrefsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  paletteStore.setState({ open: false, query: "" });
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

// The composer's OWN Steer control. Its accessible name carries the
// Shift+Enter KeyHint ("Steer Shift+Enter"), so it is not exactly "Steer";
// QueueStrip's own drain button is exactly "Steer queue now" (T3's separate
// drain-as-steer affordance, wired into this component's tree at the wave
// integration merge - w5-integration-wiring-report.md). Both accessible
// names start with "Steer" - a couple of tests below hydrate with a
// non-empty queue, rendering QueueStrip's button alongside this one - so
// disambiguate by the one that does NOT carry the "queue" token.
function steerButton(): HTMLButtonElement {
  return screen.getByRole("button", {
    name: (accessibleName) => accessibleName.startsWith("Steer") && !accessibleName.includes("queue"),
  }) as HTMLButtonElement;
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

test("text typed while a send is still in flight survives (not cleared) - the asymmetric unchanged-since-submit condition", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  let resolveSend: (() => void) | undefined;
  fake.on(
    "turn/start",
    () =>
      new Promise<{ turn: { id: string; status: string; itemsView: string } }>((resolve) => {
        resolveSend = () => resolve({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } });
      }),
  );

  await user.type(textarea(), "original");
  await user.click(submitButton()); // fires the request; submitAction awaits the still-pending promise

  // The user keeps typing while the request is in flight - a real,
  // synchronous DOM change event (not user.type, whose per-keystroke
  // delays aren't the point here) landing between submit and settlement.
  fireEvent.change(textarea(), { target: { value: "original plus more" } });
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("original plus more");

  resolveSend?.();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));

  // Give clearIfUnchanged (submitAction's own .then continuation) a chance
  // to run and settle - if it were going to wrongly clear, it would have
  // by the time this passes.
  await new Promise((resolve) => setTimeout(resolve, 10));

  expect(textarea().value).toBe("original plus more"); // NOT cleared - text changed since submit
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("original plus more"); // draft untouched
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
  prefsStore.getState().setEnterToSend(true);
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
  prefsStore.getState().setEnterToSend(true);
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
  prefsStore.getState().setEnterToSend(true);
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

test("a successful classic steer also clears the textarea and its draft (contracts §Drafts)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "steer this");
  await user.click(steerButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBeNull();
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

test("the remove button names the specific attachment (filename + dimensions for image tile, or chip text for non-image)", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "shot.png");
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  // For image attachments, the remove button label is "Remove {filename}"
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
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

test("pasted image renders as a thumbnail tile with dimensions, remove button, and lightbox on click", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "screenshot.png");
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  // Assert the thumbnail <img> renders with a data-URL
  const thumbnail = screen.getByRole("img", { name: /image 1 of 1/i }) as HTMLImageElement;
  expect(thumbnail).toBeTruthy();
  expect(thumbnail.src).toMatch(/^data:image\/png;base64,/);

  // Assert the dimensions are displayed
  expect(screen.getByText(/4×4/)).toBeTruthy();

  // Assert clicking the thumbnail opens the lightbox (before removing)
  await user.click(thumbnail);
  const lightboxImg = screen.getByTestId("image-gallery-lightbox-img") as HTMLImageElement;
  expect(lightboxImg).toBeTruthy();
  expect(lightboxImg.src).toMatch(/^data:image\/png;base64,/);

  // Close the lightbox by clicking outside (Esc or backdrop)
  const dialogBackdrop = lightboxImg.parentElement?.parentElement;
  if (dialogBackdrop) fireEvent.click(dialogBackdrop);

  // Assert clicking the ✕ removes the attachment
  const removeButton = screen.getByRole("button", { name: /remove screenshot\.png/i });
  await user.click(removeButton);
  await waitFor(() => expect(textarea().value).toBe(""));
});

// installFailingDecodeStub mirrors installCanvasStubs but rejects (via
// Image.onerror) on a microtask, instead of resolving - so a test gets a
// window between the synchronous marker-insertion and the decode's
// eventual rejection in which to make further synchronous changes (typing)
// before that rejection settles.
function installFailingDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  class FailingImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onerror?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FailingImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// CRITICAL regression test (reviewer-reproduced): useAttachments' decode-
// failure path calls the composer's TextEditor.read()/write() long after
// the render that registered it. If read() ever mixes a live cursor with a
// stale per-render `text` closure, write()'s call to the (always-stable)
// setText silently reverts the ENTIRE composer to that stale value -
// discarding both the marker insertion and anything typed since, and
// desyncing the draft (a revert that bypasses writeDraft). Reproduction:
// paste an image whose decode later fails, then type SYNCHRONOUSLY (no
// yield to the microtask queue) before that rejection settles.
test("typing synchronously after a paste whose decode later fails survives - the failed marker alone is stripped (critical)", async () => {
  installFailingDecodeStub();
  await mountComposer("ref_a");

  // act() forces React to flush the paste's resulting state update
  // synchronously (pastePngInto's plain el.dispatchEvent isn't
  // auto-wrapped the way fireEvent/user-event are) - still entirely
  // synchronous JS, so this runs before the decode's microtask-deferred
  // rejection has any chance to fire.
  act(() => {
    pastePngInto(textarea());
  });
  expect(textarea().value).toBe("[image 1]");

  // Also synchronous (fireEvent, not user.type - no per-keystroke delay
  // that could yield to the microtask queue): types "hello" at the
  // (cursor-restored) end of the marker, landing entirely before the
  // decode's rejection settles.
  fireEvent.change(textarea(), { target: { value: "[image 1]hello" } });
  expect(textarea().value).toBe("[image 1]hello");
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("[image 1]hello");

  // Now let the decode's rejection actually settle.
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeNull());

  expect(textarea().value).toBe("hello"); // typed text survives; only the failed marker is gone
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("hello"); // draft matches, not stale
});

// Minor (reviewer-requested): two attachment gestures fired back-to-back
// with NO intervening render (both dispatched inside one `act()` block, so
// React has no chance to commit the first gesture's setText before the
// second gesture's own TextEditor.read() runs) must still chain correctly
// rather than the second clobbering the first.
test("two attachment gestures fired back-to-back with no intervening render still chain their markers, not clobber", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "a.png");
    pastePngInto(textarea(), "b.png");
  });

  expect(textarea().value).toBe("[image 1][image 2]");
  await waitFor(() => expect(screen.getAllByRole("button", { name: /remove/i })).toHaveLength(2));
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

test("picking a file via the hidden input attaches it, same as paste/drop", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  // The picker input is `hidden` (only ever reached via the visible Attach
  // button proxying a click to it - see the next test) - user-event's own
  // upload() expects a pointer-interactable target, so this uses RTL's
  // fireEvent.change with a target.files override instead, the documented
  // idiom for a file input's change event.
  const file = new File([new Uint8Array([1, 2, 3])], "picked.png", { type: "image/png" });
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [file] } });

  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("clicking the attach button triggers the hidden file input", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");

  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const clickSpy = vi.spyOn(input, "click");

  await user.click(screen.getByRole("button", { name: /attach image/i }));
  expect(clickSpy).toHaveBeenCalledTimes(1);
});

// The former "reserves marked slots for T3/T4" test lived here, pinning the
// two slots via source-text scraping (comments aren't queryable via the DOM,
// and there was nothing else there yet to query). Now that the wave
// integration has actually mounted <AskDock>/<QueueStrip> in those slots
// (Composer.integration.test.tsx), a real behavioral DOM-order assertion
// supersedes it - see that file's "the ask dock renders above the queue
// strip when both are visible at once" test.

// --- leading-"/" command-palette hook (wave 6) ------------------------

test('"/" at the start of an empty composer opens the command palette (floor §2.1)', async () => {
  await mountComposer("ref_slash");

  const notPrevented = fireEvent.keyDown(textarea(), { key: "/" });

  expect(paletteStore.getState().open).toBe(true);
  expect(paletteStore.getState().query).toBe("/");
  expect(notPrevented).toBe(false); // preventDefault()'d - the "/" never types
});

test('"/" in a NON-empty composer is a literal slash, not a palette trigger', async () => {
  const user = userEvent.setup();
  await mountComposer("ref_slash2");
  await user.type(textarea(), "hello");

  fireEvent.keyDown(textarea(), { key: "/" });

  expect(paletteStore.getState().open).toBe(false);
});
