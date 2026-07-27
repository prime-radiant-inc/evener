import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy, useState } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { type PaneProps, registerPane } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { StackHost, setLastPopstateWasTrustedForTests } from "./StackHost";

// Fixture pane type for the bulk of this file's tests - non-singleton, so
// "doc"/{ref} with different refs are distinct panes (same dedup rule
// workspace.ts itself uses), and "doc" has no deep link (routing.ts's
// paneToURL returns null for it) which the URL-sync tests further down
// need a real example of. A local click counter proves whether a mount
// survived a focus change (StackHost's own remount-safety contract, same
// one DockHost's PaneHost documents - see this component's own comment).
function DocFixture({ params, paneId, focused }: PaneProps<{ ref: string }>) {
  const [clicks, setClicks] = useState(0);
  return (
    <div>
      <p>
        doc pane: {params.ref} (focused={String(focused)}) (paneId={paneId})
      </p>
      <button type="button" onClick={() => setClicks((c) => c + 1)}>
        clicks: {clicks}
      </button>
    </div>
  );
}

beforeAll(async () => {
  registerPane<{ ref: string }>({
    id: "doc",
    title: (params) => `Doc ${params.ref}`,
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  // Real production panes: "welcome" is StackHost's own hardcoded fallback
  // target, and "session" is this file's one real, deep-linked pane type
  // (routing.ts's paneToURL("session", ...) is the URL-sync tests' one
  // real, non-null example) - both registered for real rather than
  // fixtured, same dual approach DockHost.test.tsx uses for the same
  // reason.
  await import("../../panes/welcome/Welcome");
  await import("../../panes/welcome");
  await import("../../panes/session/Session");
  await import("../../panes/session");

  // Then RENDER each of the three, because importing a module is only half a
  // React.lazy's cost: lazy keeps a payload of its own that stays
  // uninitialized until React first renders the component, so the first
  // render still suspends, still commits its Suspense fallback, and then
  // waits out react-dom's FALLBACK_THROTTLE_MS (300ms, react-dom 19.2)
  // before it will commit the revealed content - a flicker guard that is
  // pure wall clock and does not shrink on a fast machine. An
  // already-resolved promise does not dodge it: the doc fixture above is
  // lazy(() => Promise.resolve(...)) and suspends once all the same.
  // Measured here: welcome 314ms, doc 305ms and session 307ms on their first
  // render, each inside a findBy budget that defaults to 1000ms. Paying it
  // in a hook whose ceiling is a tripwire, rather than inside an assertion
  // window. Same fix as App.test.tsx (commit c1a8616ea).
  await warmPane(
    () => {}, // nothing focused: StackHost's own fallback opens welcome
    () => screen.findByText("No session open"),
  );
  await warmPane(
    () => workspaceStore.getState().openPane("doc", { ref: "ref_warm" }),
    () => screen.findByText(/doc pane: ref_warm/),
  );
  await warmPane(
    () => workspaceStore.getState().openPane("session", { ref: "local:ref_warm" }),
    () => screen.findByText("local:ref_warm"),
  );
});

// Renders StackHost once with `open`'s pane focused and awaits its landmark,
// so both halves of that pane's lazy-loading cost are already paid by the
// time a test measures it. See the beforeAll above for why the module cache
// alone is not enough.
async function warmPane(open: () => void, findLandmark: () => Promise<unknown>): Promise<void> {
  open();
  render(<StackHost />);
  await findLandmark();
  cleanup();
  resetWorkspaceStoreForTests();
  setLastPopstateWasTrustedForTests(false);
  window.history.pushState({}, "", "/");
}

beforeEach(() => {
  resetWorkspaceStoreForTests();
  setLastPopstateWasTrustedForTests(false);
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  setLastPopstateWasTrustedForTests(false); // defensive: no test should leak this into the next
});

test("falls back to opening welcome when nothing is focused at mount", async () => {
  render(<StackHost />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("renders the focused pane's component full-screen, with focused=true", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("renders no 'Pop out' affordance - popout is a desktop dockview capability, absent on the mobile stack", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  // The popout affordance lives only in DockHost's dockview group headers
  // (see PopoutHeaderAction); the StackHost has no group chrome to host it,
  // and popOutPane is a no-op with no dockview api registered anyway.
  expect(screen.queryByRole("button", { name: "Pop out" })).toBeNull();
});

test("renders only the focused pane - a second open pane is not mounted at all", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // focused, most recently opened
  render(<StackHost />);

  await screen.findByText(/doc pane: ref_b/);
  expect(screen.queryByText(/doc pane: ref_a/)).toBeNull();
});

test("switching the focused pane (workspace.focusPane) swaps which one is rendered", async () => {
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().focusPane(first);

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(screen.queryByText(/doc pane: ref_b/)).toBeNull();
});

// StackHost and DockHost must consume the same store-level primary replacement
// contract. A mobile host that only changes its focused pane leaves the stale
// secondary doc in shared state and can render the wrong pane after a route.
test("renders the replacement session and drops stale secondary panes from the shared workspace", async () => {
  const workspace = workspaceStore.getState();
  workspace.openPane("session", { ref: "local:session-a" });
  render(<StackHost />);
  await screen.findByText("local:session-a");

  workspace.openPane("doc", { ref: "secondary" });
  await screen.findByText(/doc pane: secondary/);

  const replacementId = workspace.replacePrimary("session", { ref: "local:session-b" }, "local:session-b");

  expect(workspaceStore.getState().panes).toEqual([
    { id: replacementId, type: "session", params: { ref: "local:session-b" }, slot: "main" },
  ]);
  expect(await screen.findByText("local:session-b")).toBeTruthy();
  expect(screen.queryByText(/doc pane: secondary/)).toBeNull();
  expect(screen.queryByText("local:session-a")).toBeNull();
});

// --- remount safety: matches DockHost's own "unmount, not hide" contract -

test("switching away from a pane and back remounts it fresh - local state does not survive", async () => {
  // Real dockview unmounts a panel's whole tree whenever it isn't the
  // active tab (see DockHost.tsx's own PaneHost comment, live-probe
  // verified in that task's report) - every pane component is designed
  // around that contract already. StackHost only ever mounts ONE pane at a
  // time, so it must reproduce the same guarantee: a pane that regains
  // focus gets a FRESH instance, never one quietly kept alive off-screen.
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  const user = userEvent.setup();
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().focusPane(first);
  await screen.findByText(/doc pane: ref_a/);
  await user.click(screen.getByRole("button", { name: /clicks: 0/ }));
  expect(screen.getByRole("button", { name: /clicks: 1/ })).toBeTruthy();

  workspaceStore.getState().focusPane(second);
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().focusPane(first);

  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();
  expect(screen.getByRole("button", { name: /clicks: 0/ })).toBeTruthy(); // reset, not preserved
});

// --- tree drawer trigger ------------------------------------------------

test("hosts the tree drawer trigger in the top bar, regardless of which pane is focused", async () => {
  render(<StackHost />);
  expect(await screen.findByRole("button", { name: "Sessions" })).toBeTruthy();
});

// --- back navigation ---------------------------------------------------

test("the back affordance is hidden while welcome (the stack's root) is focused", async () => {
  render(<StackHost />);
  await screen.findByText("No session open");
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
});

test("the back affordance is shown once a non-welcome pane is focused", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  expect(await screen.findByRole("button", { name: "Back" })).toBeTruthy();
});

test("back returns to the pane that was focused before the current one", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);

  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // a forward navigation, observed live
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("back falls all the way to welcome when this component never observed an earlier pane", async () => {
  // StackHost mounts with ref_a already focused (simulating AppShell
  // having pre-seeded it via routing, same as the real integration) - this
  // component never itself witnessed a transition INTO ref_a, so there is
  // nothing on ITS OWN back-stack to return to; welcome is the documented
  // fallback (see requirement 2 - "pops to the previously-focused pane or
  // welcome").
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("back walks multiple levels deep, one pane per tap", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  await screen.findByText(/doc pane: ref_c/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText(/doc pane: ref_b/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText("No session open")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull(); // welcome: nothing further behind it
});

test("a back tap does not push the pane it left onto the stack (no ping-pong)", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" })); // -> ref_a
  await screen.findByText(/doc pane: ref_a/);

  // ref_a is StackHost's own root here (it never observed anything before
  // ref_a - see the "falls all the way to welcome" test above) - a second
  // tap must go straight to welcome, not bounce forward to ref_b.
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText("No session open")).toBeTruthy();
});

// --- real browser back/forward composing with the in-app Back button -----
//
// A REAL back/forward gesture reaches this component the same way as any
// other focus change AppShell's routing glue drives (via openPane/focusPane
// - not exercised by this standalone render, so these tests do that part by
// hand, same as every other test in this file stands in for "the tree rail
// did this" via a direct openPane() call). What's DIFFERENT about a real
// gesture is the popstate event that precedes it: a genuinely trusted one
// (isTrusted=true) cannot be constructed from script at all - the DOM spec
// makes isTrusted a non-configurable own accessor on every Event instance,
// in jsdom and every real browser alike, specifically so no script can
// forge one (confirmed directly: Object.defineProperty(event, "isTrusted",
// ...) throws "Cannot redefine property"). setLastPopstateWasTrustedForTests
// is StackHost.tsx's own escape hatch for this - it drives the exact
// downstream decision a real trusted popstate would, without needing an
// unforgeable event; the one line that actually reads a real event's
// isTrusted (StackHost.tsx's own popstate listener) was instead verified
// against a REAL trusted event via a live CDP-driven browser back (see this
// task's report), not a jsdom probe.
test("a REAL back/forward gesture does not stack the pane it left, unlike an ordinary forward navigation", async () => {
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // ordinary forward nav: stacks ref_a
  await screen.findByText(/doc pane: ref_b/);

  // Simulates a real browser back landing on ref_a.
  act(() => {
    setLastPopstateWasTrustedForTests(true);
    workspaceStore.getState().focusPane(first);
  });
  await screen.findByText(/doc pane: ref_a \(focused=true\)/);

  // Without the fix, ref_b would now be sitting on top of the stack (an
  // ordinary forward-navigation push) - tapping Back from here would move
  // the user FORWARD to ref_b instead of continuing backward to welcome.
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("an ORDINARY (untrusted) synthetic popstate - routing.ts's own navigate() - still stacks normally", async () => {
  // Exercises the real listener wiring end to end (a genuinely dispatched
  // event, not the test-only setter): routing.ts's navigate() dispatches
  // exactly this shape (new PopStateEvent() + window.dispatchEvent()),
  // always isTrusted=false, so it must compose as an ORDINARY forward step,
  // not be mistaken for a real back/forward.
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);

  act(() => {
    window.dispatchEvent(new PopStateEvent("popstate"));
    workspaceStore.getState().focusPane(first);
  });
  await screen.findByText(/doc pane: ref_a \(focused=true\)/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  // ref_b is still on the stack (pushed by the ordinary forward nav above,
  // NOT skipped - the untrusted popstate must not suppress it).
  expect(await screen.findByText(/doc pane: ref_b \(focused=true\)/)).toBeTruthy();
});

test("a REAL back/forward gesture landing back on the stack's own top does not require an extra, invisible tap to clear it", async () => {
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);

  // Real back to ref_a, then real FORWARD back to ref_b - both real, both
  // skip the push (see the test above), so the stack still names ref_a as
  // its one entry, matching ref_a's OWN position relative to ref_b in the
  // original forward walk (not a stale duplicate of the CURRENT pane).
  act(() => {
    setLastPopstateWasTrustedForTests(true);
    workspaceStore.getState().focusPane(first);
  });
  await screen.findByText(/doc pane: ref_a \(focused=true\)/);
  act(() => {
    setLastPopstateWasTrustedForTests(true);
    workspaceStore.getState().focusPane(second);
  });
  await screen.findByText(/doc pane: ref_b \(focused=true\)/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("KNOWN, DISCLOSED LIMITATION: two consecutive real back gestures still confuse the local stack (see StackHost.tsx's own comment)", async () => {
  // The fix only knows "the immediately preceding popstate was real", not
  // how many steps of real history navigation actually happened - a SECOND
  // consecutive real back is indistinguishable, from this component's own
  // vantage point, from any other focus change that isn't itself a
  // popstate-driven one, so the stack's stale top entry from the ORIGINAL
  // forward walk resurfaces. Pinned here as the honest boundary of what
  // this task's fix actually covers, not silently left unproven.
  const a = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  const b = workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().openPane("doc", { ref: "ref_c" }); // stacks ref_b
  await screen.findByText(/doc pane: ref_c/);

  act(() => {
    setLastPopstateWasTrustedForTests(true);
    workspaceStore.getState().focusPane(b);
  });
  await screen.findByText(/doc pane: ref_b \(focused=true\)/);
  act(() => {
    setLastPopstateWasTrustedForTests(true);
    workspaceStore.getState().focusPane(a);
  });
  await screen.findByText(/doc pane: ref_a \(focused=true\)/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText(/doc pane: ref_b \(focused=true\)/)).toBeTruthy();
});

test("back skips a stacked pane that has since been closed, falling to welcome when nothing else is left", async () => {
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks ref_a as the back target
  await screen.findByText(/doc pane: ref_b/);

  // Closing the NON-focused, stacked pane (ref_a) does not change
  // focusedPaneId (workspace.ts's closePane only clears focus when the
  // CLOSED pane was the focused one) - so this component's own bookkeeping
  // effect never re-runs and backStackRef still names ref_a's now-defunct
  // id. popValidBackTarget must discover that at pop time, not before.
  act(() => {
    workspaceStore.getState().closePane(first);
  });

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("back skips a stale MIDDLE stack entry and lands on the next valid one behind it", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().openPane("doc", { ref: "ref_c" }); // stacks ref_b
  await screen.findByText(/doc pane: ref_c/);

  // ref_b sits BETWEEN ref_a and ref_c on the stack ([ref_a, ref_b]) - closing
  // it (still not the focused pane) leaves a stale entry in the MIDDLE of
  // what popValidBackTarget will walk, not just at the top.
  act(() => {
    workspaceStore.getState().closePane(second);
  });

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  // ref_b is skipped silently; ref_a (still open, next behind it) is the
  // landing target - not welcome, and not a dead end on the stale entry.
  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

// --- URL sync: the address bar always reflects the focused pane -----------
//
// Unlike DockHost, StackHost has no per-pane "tab" carrying its own text -
// Session.tsx's ref (its own PaneScaffold title) appears exactly once per
// test here, so these use findByText, not AppShell.test.tsx's own
// findAllByText-and-count-2 idiom for the tab-plus-body case.

test("the URL updates to a deep-linked pane's URL once it becomes focused", async () => {
  render(<StackHost />);
  await screen.findByText("No session open");

  workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  await screen.findByText("local:ref_x");

  expect(window.location.pathname).toBe("/s/local%3Aref_x");
});

test("switching between two deep-linked panes updates the URL each time", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  render(<StackHost />);
  await screen.findByText("local:ref_x");
  expect(window.location.pathname).toBe("/s/local%3Aref_x");

  workspaceStore.getState().openPane("session", { ref: "local:ref_y" });
  await screen.findByText("local:ref_y");

  expect(window.location.pathname).toBe("/s/local%3Aref_y");
});

test("a pane type with no deep link (paneToURL returns null) leaves the URL untouched", async () => {
  window.history.pushState({}, "", "/before");
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);

  await screen.findByText(/doc pane: ref_a/);

  expect(window.location.pathname).toBe("/before");
});

test("back navigation updates the URL to match the pane it returns to", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  render(<StackHost />);
  await screen.findByText("local:ref_x");
  workspaceStore.getState().openPane("session", { ref: "local:ref_y" });
  await screen.findByText("local:ref_y");
  expect(window.location.pathname).toBe("/s/local%3Aref_y");

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  await screen.findByText("local:ref_x");
  expect(window.location.pathname).toBe("/s/local%3Aref_x");
});

test("mounting already at the focused pane's own URL does not dispatch a redundant popstate", async () => {
  // The pane's OWN url, i.e. exactly what paneToURL emits - percent-encoded,
  // since a ref carries a ":" that does not survive a path segment raw.
  window.history.pushState({}, "", "/s/local%3Aref_x");
  workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  const handler = vi.fn();
  window.addEventListener("popstate", handler);

  render(<StackHost />);
  await screen.findByText("local:ref_x");

  window.removeEventListener("popstate", handler);
  expect(handler).not.toHaveBeenCalled();
});

// --- bottom safe-area padding (requirement 4) ------------------------
//
// jsdom does not evaluate real CSS (env(), viewport units, etc.), so - same
// idiom as sheet.test.tsx's own prefers-reduced-motion check - this reads
// the CSS module's own source rather than asserting on computed style.

test("the stack container reserves the device's bottom safe-area inset", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "StackHost.module.css"), "utf8");
  expect(css).toContain("env(safe-area-inset-bottom)");
});
