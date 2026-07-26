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
import buttonStyles from "../../../widgets/button/button.module.css";
import iconButtonStyles from "../../../widgets/iconbutton/iconbutton.module.css";
import promptCardStyles from "../../../widgets/promptcard/promptcard.module.css";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
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
  // The toast store is module state that outlives RTL's own cleanup, so a
  // toast pushed by one test would otherwise still be in the next test's
  // tree and make a getByText for the same message ambiguous.
  resetToastStoreForTests();
  paletteStore.setState({ open: false, query: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function textarea(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: /message/i }) as HTMLTextAreaElement;
}

// The composer's controls are addressed by their stable data-testid, not by
// accessible name: two different buttons in this tree start with "Steer"
// (this component's own and QueueStrip's "Steer queue now"), and the
// submit button's own name tracks the send/queue routing and the keyboard
// hint. The accessible names are still a real contract - see the dedicated
// "spoken name" tests below - they just aren't how tests navigate.
function submitButton(): HTMLButtonElement {
  return screen.getByTestId("composer-submit") as HTMLButtonElement;
}

function steerButton(): HTMLButtonElement {
  return screen.getByTestId("composer-steer") as HTMLButtonElement;
}

function stopButton(): HTMLButtonElement {
  return screen.getByTestId("composer-stop") as HTMLButtonElement;
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

// The card is widgets/promptcard now, so the focus-ring RULE is that widget's
// own contract (promptcard.test.tsx pins the declaration). What stays this
// component's business is that it renders the shared card at all and that the
// seamless field inside really drives the card's focus state. Only the
// post-focus state is queried: jsdom's selector engine caches a :focus-within
// result per element, so an earlier "not yet focused" call on the same node
// would keep answering false afterwards.
test("focusing the message field lights the shared prompt card's own focus affordance", async () => {
  await mountComposer("ref_a");
  textarea().focus();
  expect(screen.getByTestId("composer-input-card").matches(":focus-within")).toBe(true);
});

// The composer and the spawn form are the SAME object: both render
// widgets/promptcard, not two components that merely resemble each other. The
// class on the rendered card is the proof that reaches across both files.
test("the composer's card IS the shared PromptCard widget, not a lookalike", async () => {
  await mountComposer("ref_a");
  expect(screen.getByTestId("composer-input-card").className.split(" ")).toContain(promptCardStyles.card);
});

// The chords moved out of the buttons into their tooltips, so each control's
// spoken name is now just its verb. These are the ONE place that asserts
// accessible names deliberately; everywhere else addresses controls by testid.
test("each control's spoken name is its bare verb - no chord glyphs in the name or the label", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  expect(screen.getByRole("button", { name: "Stop" })).toBe(stopButton());
  expect(screen.getByRole("button", { name: "Send" })).toBe(submitButton());
  expect(screen.getByRole("button", { name: "Steer" })).toBe(steerButton());
  expect(screen.getByRole("button", { name: "Attach image" })).toBe(screen.getByTestId("composer-attach"));
});

// The boxed <kbd> runs are gone from inside the buttons - three nested boxes
// dominated the button they annotated. The chord still has to be DISCOVERABLE,
// which is what each button's Tooltip is for.
test("no button renders a chord hint inside itself; the chord lives in the button's tooltip", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

  for (const control of [stopButton(), submitButton(), steerButton()]) {
    expect(control.querySelector("kbd")).toBeNull();
    expect(control.textContent).not.toMatch(/↵/);
  }

  // Tooltip shows after its own 300ms delay; user-event's fake-free setup
  // advances real time, so this waits for the bubble rather than assuming it.
  await user.hover(submitButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toContain(`${modWord}+Enter`);
});

test("the Steer tooltip names the chord that fires it", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  await user.hover(steerButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toContain("Shift+Enter");
});

// Density: the control row is the 24px (xs) size. Three nested gaps plus the
// card's padding plus a taller row stacked up to a block far taller than the
// input it framed.
test("every control in the composer's button row is the xs (24px) size", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });

  // IconButton overrides Button's own xs/sm/md with its square sizing (see
  // iconbutton.module.css), so the one icon control carries that module's xs.
  expect(screen.getByTestId("composer-attach").className.split(" ")).toContain(iconButtonStyles.xs);
  for (const control of [stopButton(), steerButton(), submitButton()]) {
    expect(control.className.split(" ")).toContain(buttonStyles.xs);
  }
});

// Stop is the WORD, in danger ink, not a filled square glyph: it is chrome, and
// chrome speaks. dangerQuiet keeps the hue on the label rather than as a fill
// competing with the primary control beside it.
test("Stop renders as the word in the dangerQuiet variant, not as an icon", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  expect(stopButton().textContent).toBe("Stop");
  expect(stopButton().querySelector("svg")).toBeNull();
  expect(stopButton().className.split(" ")).toContain(buttonStyles.dangerQuiet);
});

// The attach control stays an icon (a paperclip needs no word), and stays a
// real SVG rather than a "📎"/"+" character whose weight and baseline shift
// from font to font.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  await mountComposer("ref_a");
  const attach = screen.getByTestId("composer-attach");
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

// --- the verb cluster's order and emphasis ---------------------------------
//
// Stop is pinned LEFTMOST so it never trades places with the verbs that come
// and go: it is the one control here whose misfire cannot be undone. Send holds
// the middle, Steer the right.
test("the cluster order is Stop, Send, Steer left to right", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  const card = screen.getByTestId("composer-input-card");
  const order = [...card.querySelectorAll("button")]
    .map((b) => b.getAttribute("data-testid"))
    .filter((id) => id !== null && id !== "composer-attach");
  expect(order).toEqual(["composer-stop", "composer-submit", "composer-steer"]);
});

// Jesse's own correction, honored exactly: both verbs stay, with distinct jobs.
// While a turn runs Steer is the primary (interrupt and redirect NOW) and Send
// sits beside it quiet, queueing until the agent stops. Idle, Send is the
// primary and sends immediately. A label never changes meaning under the user.
test("while a turn runs, Steer is primary and Send is quiet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  expect(steerButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(submitButton().className.split(" ")).toContain(buttonStyles.quiet);
});

test("with nothing running, Send is the primary and there is no Steer to outrank it", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(submitButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(screen.queryByTestId("composer-steer")).toBeNull();
});

// The label is stable across states even though the ROUTE isn't: a mid-turn
// Send queues (turn/queue, proven by the routing tests below) but still reads
// "Send", because the change is one of timing, not of verb. The tooltip is
// where the timing is spelled out.
test("Send keeps its label while a turn runs, and its tooltip explains the queueing", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  expect(submitButton().textContent).toBe("Send");
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toMatch(/queue until the agent stops/i);
});

test("Send's tooltip says it sends now when nothing is running", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toMatch(/send now/i);
});

// cezn: Send and Steer sit side by side while a turn runs, and Send's own
// label never changes whether it fires now or queues - only the tooltip
// above disambiguates, and it needs a 300ms hover nobody mid-flow gives it.
// This caption says the same thing the tooltip does, WITHOUT a hover -
// always on screen the instant the control row itself is, non-color
// (plain caption text), and it never touches the Send/Steer labels
// themselves (additive only, per Composer.tsx's own top-of-file reasoning
// for keeping one label).
test("an always-visible caption states Send's timing while a turn runs - no hover required", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  expect(screen.getByText(/send queues until the agent stops/i)).toBeTruthy();
});

// Idle Send is unambiguous (there's no Steer beside it, and no timing
// question to answer), so the caption would be noise there - it only earns
// its place while the ambiguity it resolves actually exists.
test("the timing caption is absent when nothing is running", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// A turn can be busy (Stop/Steer showing) while the source has explicitly
// advertised no queue capability - deriveSendQueueAvailability's own
// both-false branch. Send is disabled there (canCompose is false), so a
// caption claiming it queues would be a lie; this pins the caption to
// availability.canQueue, not to `busy` alone.
// status can read "active" before turn/started has populated activeTurnId
// (isTurnActive's own doc comment on that race window) - Stop/Steer stay
// hidden there since neither is meaningful without a real turn to act on,
// and the caption follows the same rule: nothing to explain the timing of
// until Send/Steer's own ambiguity actually exists.
test("the timing caption is absent while status reads active but no turn has actually started yet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

test("the timing caption is absent when busy but the source advertises no queue capability", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, queue: false },
      queue: {},
      activeTurnId: "turn_1",
    },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
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

test("active session with queue capability: Send routes to turn/queue", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} },
  });
  fake.on("turn/queue", () => ({}));

  await user.type(textarea(), "queued message");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true));
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

// The chord a hint advertises has to track the preference that actually fires
// it: enterToSend on means bare Enter submits, so the tooltip must say Enter,
// not Mod+Enter. Now asserted on the tooltip, since that's where the chord
// moved.
test("the submit tooltip's chord switches from Mod+Enter to a bare Enter when enterToSend is on", async () => {
  const user = userEvent.setup();
  const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

  await mountComposer("ref_a");
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toContain(`${modWord}+Enter`);

  cleanup();
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a");
  await user.hover(submitButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toMatch(/·\s*Enter$/);
  expect(tip.textContent).not.toContain(modWord);
});

// enterToSend on makes Shift+Enter a literal newline rather than a steer (see
// handleKeyDown), so Steer's tooltip must stop advertising a chord that no
// longer reaches it.
test("Steer's tooltip drops the chord when enterToSend has taken Shift+Enter away from it", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  await user.hover(steerButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toMatch(/interrupt and redirect now/i);
  expect(tip.textContent).not.toMatch(/Shift/);
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

// Steer and Stop only act on an in-flight turn, so neither is rendered
// during the window after status flips "active" but before activeTurnId
// arrives - the same isTurnActive gate their handlers need.
test("neither steer nor stop renders during the window after status flips active but before activeTurnId arrives", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {} }, // no activeTurnId yet
  });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

// Shift+Enter reaches the steer handler directly off the keydown event, so
// it works whether or not the Steer BUTTON is on screen at all - exactly
// mirroring legacy's own "keyboard equivalent of clicking the steer button"
// (the SAME function, not a separately-gated path). The handler's own
// internal activeTurnId check is therefore the only thing standing between
// the keyboard and a doomed steer, and these two cases are where it earns
// its keep: no turn is in flight, so no Steer button is rendered to gate on.
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

test("Shift+Enter on an idle session, where no Steer button renders at all, still reaches the handler and toasts", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/steer", () => ({}));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull(); // nothing to click; the keybinding is the only route
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

// --- which controls the row shows ------------------------------------------
//
// Steer and Stop both act on an IN-FLIGHT turn: with nothing running there is
// no turn to steer into and none to interrupt, so an idle composer shows only
// attach + the submit button rather than two permanently-dead controls.
// Capability still gates them independently for a session whose harness
// can't steer or can't interrupt.

test("an idle session renders neither steer nor stop - only attach and submit", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(submitButton()).toBeTruthy();
});

test("a busy session renders both steer and stop, enabled", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  expect(steerButton().disabled).toBe(false);
  expect(stopButton().disabled).toBe(false);
});

test("a busy session on a harness that can't interrupt renders steer but not stop", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, interrupt: false },
      queue: {},
      activeTurnId: "turn_1",
    },
  });
  expect(steerButton()).toBeTruthy();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

test("a busy session on a harness that can't steer renders stop but not steer", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, steer: false },
      queue: {},
      activeTurnId: "turn_1",
    },
  });
  expect(stopButton()).toBeTruthy();
  expect(screen.queryByTestId("composer-steer")).toBeNull();
});

test("the stop button is absent once the session has ended", async () => {
  await mountComposer("ref_a", { status: { type: "ended" } });
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// --- the ended state: an epitaph, not a cockpit ---------------------------
//
// A cold exited serf session arrives as "notLoaded" and STILL advertises Send
// (cmd/serf-hub/app_threadread.go's pastEntryThread: the hub auto-resumes it on
// the first message), so it keeps a card - collapsed to a one-line invitation
// AT REST, since chrome around an empty invitation is noise. Engaging it
// (focus, or any content) grows the real control row: a field you can type into
// with no visible way to send is a dead end, and a keyboard chord is not an
// affordance anyone can see.

const ENDED_STATUSES = ["ended", "closed", "notLoaded"] as const;

test.each(ENDED_STATUSES)("a %s session's card rests as a bare invitation with no control row", async (type) => {
  await mountComposer("ref_a", { status: { type } });
  const card = screen.getByTestId("composer-input-card");
  expect(textarea().getAttribute("placeholder")).toBe("Send a follow-up…");
  expect(card.querySelectorAll("button")).toHaveLength(0);
  expect(screen.queryByTestId("composer-attach")).toBeNull();
  expect(screen.queryByTestId("composer-submit")).toBeNull();
});

test.each(ENDED_STATUSES)("a %s session's card grows a usable Send once focused", async (type) => {
  await mountComposer("ref_a", { status: { type } });
  await userEvent.setup().click(textarea());

  const submit = screen.getByTestId("composer-submit");
  expect(submit.textContent).toContain("Send");
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  // Steer and Stop stay absent: there is no turn in flight to act on.
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// The button has to be ENABLED, not merely present. deriveSendQueueAvailability
// reports canSend===canQueue===false for ended/closed (no turn to send to or
// queue behind), so gating the control on it renders a permanently dead Send at
// exactly the sessions the hub resumes on demand.
test.each(ENDED_STATUSES)("a %s session's Send enables as soon as there is something to send", async (type) => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type } });
  await user.click(textarea());
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(true); // nothing typed yet

  await user.type(textarea(), "follow up on this");
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(false);
});

test.each(ENDED_STATUSES)(
  "clicking Send on a %s session really sends, rather than toasting a refusal",
  async (type) => {
    const user = userEvent.setup();
    const fake = await mountComposer("ref_a", { status: { type } });
    fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

    await user.click(textarea());
    await user.type(textarea(), "wake up and finish the job");
    await user.click(screen.getByTestId("composer-submit"));

    await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
    expect(screen.queryByText(/Send is not available/)).toBeNull();
  },
);

// Blur must not strand a typed message: the control row is gated on engagement
// (focus OR content), so text left in the field keeps its Send.
test("an ended session that still holds text keeps its control row after blur", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "ended" } });
  await user.click(textarea());
  await user.type(textarea(), "draft I walked away from");
  await user.tab();

  expect(screen.getByTestId("composer-submit")).toBeTruthy();
});

// One line at rest, opening to a real writing surface once focused. Driven from
// React state rather than a :focus-within CSS rule because the floor has to
// reach the field's own `rows` attribute to take effect at all (widgets/textarea
// documents why), and only the prop can do that. Verified in Chrome too: before
// the rows half of that fix, the collapsed field measured 39px - two lines - no
// matter what the floor said.
test("an ended session's field rests at one line and opens to three on focus", async () => {
  await mountComposer("ref_a", { status: { type: "notLoaded" } });
  expect(textarea().getAttribute("rows")).toBe("1");
  expect(textarea().style.getPropertyValue("--textarea-min-lines")).toBe("1");

  act(() => textarea().focus());
  expect(textarea().getAttribute("rows")).toBe("3");
  expect(textarea().style.getPropertyValue("--textarea-min-lines")).toBe("3");

  act(() => textarea().blur());
  expect(textarea().getAttribute("rows")).toBe("1");
});

// A live session's field must NOT pick up the collapsed floor - it keeps the
// widget's own MIN_ROWS default, so a running composer is a comfortable target.
test("a live session's field keeps the widget's own default line floor", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(textarea().getAttribute("rows")).toBe("2");
  expect(textarea().getAttribute("style") ?? "").not.toContain("--textarea-min-lines");
});

test("an ended session can still be typed into and submitted with the Mod+Enter chord", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "notLoaded" } });
  fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

  await user.type(textarea(), "one more thing");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

// The other half of the rule: when the source really cannot take input, there
// is nothing a card could accomplish, so none is rendered. An unusable field is
// worse than no field - which is exactly what a disabled one was.
test("a session whose harness advertises no send at all renders NO card, not a dead one", async () => {
  await mountComposer("ref_a", {
    status: { type: "closed" },
    serf: { ref: "ref_a", capabilities: { ...FULL_CAPABILITIES, send: false }, queue: {} },
  });
  expect(screen.queryByTestId("composer-input-card")).toBeNull();
  expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
});

// --- interrupt ---------------------------------------------------------------

test("clicking Stop calls turn/interrupt", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", () => ({}));

  await user.click(stopButton());

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

  await user.click(stopButton());

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

  await user.click(screen.getByTestId("composer-attach"));
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

// A cold session is resumed behind turn/start (cmd/serf-hub/app_session_resume.go).
// When that resume is what died, "Send failed" would send the user looking at
// their message rather than at the daemon that would not start.
test("a send that fails because the session would not start names the start, not the send", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => {
    throw new WireError("serf launch-check timed out", -32014, { serfErrorInfo: "hubLaunch" });
  });

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() =>
    expect(screen.getByText("Couldn't start this session: serf launch-check timed out")).toBeTruthy(),
  );
  expect(screen.queryByText(/send failed/i)).toBeNull();
});
