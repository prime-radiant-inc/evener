import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { bindFilePath } from "@evener/appwire-client/docContent";
import { manifest as navigationManifest } from "@evener/appwire-client/testing/navigation";
import { testEscalation } from "@evener/appwire-client/testing/reducerTestKit";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DockviewApi } from "dockview-core";
import { lazy, useEffect, useRef, useState } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { openDocBeside } from "../../panes/doc/openDoc";
import { installMobileViewport } from "../../panes/session/testing/mobileViewport";
import { useTranscriptScroll } from "../../panes/session/transcript/flow/useTranscriptScroll";
import { TranscriptBody } from "../../panes/session/transcript/TranscriptBody";
import {
  holdReaderFrames,
  installTranscriptGeometry,
} from "../../panes/session/transcript/transcriptReadingGeometryTestUtils";
import { makeTranscriptPreviewModel } from "../../transcriptDisplay/previewFixture";
import type { VirtualListHandle } from "../../widgets";
import { connectIPhoneHost } from "../iPhoneHost";
import { MOBILE_QUERY, TABLET_QUERY } from "../useIsMobile";
import "../../panes/transcript";
import { cascadeClient, cascadeContext } from "../../panes/zoom/cascadeTestUtils";
import { recordCascadeOrigin } from "../../panes/zoom/inspectionOrigin";
import type { SessionZoomParams } from "../../panes/zoom/intent";
import { StubResizeObserver } from "../../resizeObserverTestUtils";
import "../../panes/zoom";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityDetailsThread,
  activitySummary,
} from "../../stores/sessionActivityTestUtils";
import { topNotesStore } from "../../stores/topNotes";
import { initTranscriptDisplay, resetTranscriptDisplayStoreForTests } from "../../stores/transcriptDisplay";
import { readModuleCss } from "../../styles/cssBlock";
import { Badge, Dialog, Input } from "../../widgets";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { openAttentionSession } from "../attentionHistory";
import { captureAttentionPlacement, presentAttentionTarget, restoreAttentionPlacement } from "../attentionPlacement";
import { chromeStore, resetChromeStoreForTests } from "../chromeStore";
import { ClientProvider } from "../clientContext";
import { type PaneProps, registerPaneForTests } from "../paneRegistry";
import { usePaneVisible } from "../paneVisibility";
import { useRailHeaderAction } from "../rail/railHeaderAction";
import { openSessionByRef, openTopLevelSession } from "../sessionPlacement";
import {
  documentPaneState,
  recordConversationOpenParent,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../workspace";
import mobilePanelStyles from "./MobilePanel.module.css";
import { StackHost, scrollOffsetsForTests, setLastPopstateWasTrustedForTests } from "./StackHost";
import stackStyles from "./StackHost.module.css";

test("ordinary mobile Back skips attention parked records", async () => {
  const x = workspaceStore.getState().openPane("doc", { ref: "independent" });
  const host = await act(async () => render(<StackHost routeDeferred />));
  await act(async () => workspaceStore.getState().openPane("session", { ref: "local:a" }));
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing phone origin");
  await act(async () => presentAttentionTarget("local:b"));
  expect(origin.pane.slot).toBe("parked");
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(x);
  expect(workspaceStore.getState().panes).toContain(origin.pane);
  await act(async () => {
    expect(restoreAttentionPlacement(origin)).toBe(true);
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(origin.pane.id);
  host.unmount();
});

// A cascade opened over the attention target is a local step. Its Back belongs
// to the phone's own stack, not to the attention entry the target owns: the
// entry has no URL for a cascade, so attention Back would jump past the target
// to the entry's source.
test("toolbar Back from a cascade over the attention target returns to the target", async () => {
  window.history.replaceState({}, "", "/s/local%3Aa");
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const host = await act(async () => render(<StackHost />));
  await act(async () => workspaceStore.getState().focusPane(a));
  await act(async () => {
    openAttentionSession("local:b");
  });
  const b = workspaceStore.getState().mainPane();
  if (!b) throw new Error("attention hop did not place its destination");
  act(() => {
    presentAttentionTarget("local:b");
  });
  await act(async () => {
    openAttentionSession("local:c");
  });
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await act(async () => workspaceStore.getState().focusPane(b.id));

  const inspector = await act(async () =>
    workspaceStore
      .getState()
      .openPane(
        "sessionZoom",
        { ref: "local:leaf", source: { type: "session", params: { ref: "local:b" } }, edges: [] },
        { slot: "secondary" },
      ),
  );
  await act(async () => workspaceStore.getState().focusPane(inspector));

  // Attention Back traverses the browser history to the entry's source, past
  // the target. A cascade opened over the target is a local step, so Back must
  // not take that step at all.
  const traverse = vi.spyOn(window.history, "back");
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(traverse).not.toHaveBeenCalled();
  traverse.mockRestore();
  host.unmount();
});

test("mobile Back from a saved in-place cascade clears wentBackRef so the next child still returns to the restored source", async () => {
  const childA = workspaceStore.getState().openPane("session", { ref: "local:child-a" }, { slot: "secondary" });
  const cascadeParams: SessionZoomParams = {
    ref: "local:leaf",
    source: { type: "session", params: { ref: "local:root" } },
    edges: [{ ownerRef: "local:root", childRef: "local:leaf", delegateId: "d1" }],
  };
  const cascadeId = workspaceStore.getState().openPane("sessionZoom", cascadeParams, { slot: "main" });
  workspaceStore.getState().focusPane(childA);
  const host = await act(async () => render(<StackHost />));
  // Switch from childA to the saved in-place cascade so childA sits on backStack.
  await act(async () => workspaceStore.getState().focusPane(cascadeId));
  // Back retypes cascadeId in place to local:root without changing focusedPaneId.
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().panes.find((pane) => pane.id === cascadeId)).toMatchObject({
    type: "session",
    params: { ref: "local:root" },
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  // Opening another child from the restored source must record cascadeId on
  // backStack, so the following Back returns to the restored source instead of
  // skipping past it to childA.
  const childB = await act(async () =>
    workspaceStore.getState().openPane("session", { ref: "local:child-b" }, { slot: "secondary" }),
  );
  expect(workspaceStore.getState().focusedPaneId).toBe(childB);
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  host.unmount();
});

test("mobile toolbar Back dismisses an inspector even when Needs You bound it to attention history", async () => {
  window.history.replaceState({}, "", "/s/local%3Aother");
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:source" }, { slot: "main" });
  const otherId = workspaceStore.getState().openPane("session", { ref: "local:other" }, { slot: "secondary" });
  const inspectorId = workspaceStore.getState().openPane(
    "sessionZoom",
    {
      ref: "local:leaf",
      source: { type: "session", params: { ref: "local:source" } },
      edges: [{ ownerRef: "local:source", childRef: "local:leaf", delegateId: "d1" }],
      inspection: { origin: { paneId: sourceId, type: "session", ref: "local:source" } },
    },
    { slot: "secondary" },
  );
  const sourcePane = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const inspectorPane = workspaceStore.getState().panes.find((pane) => pane.id === inspectorId);
  if (!sourcePane || !inspectorPane) throw new Error("missing inspector fixture panes");
  recordCascadeOrigin(inspectorPane, sourcePane);
  workspaceStore.getState().focusPane(otherId);
  const host = await act(async () => render(<StackHost />));
  // Hop via Needs You from otherId to the already-presented inspector leaf so
  // the current attention history entry binds the inspector itself.
  await act(async () => {
    openAttentionSession("local:leaf");
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
  const traverse = vi.spyOn(window.history, "back");
  await userEvent.click(screen.getByRole("button", { name: "Back" }));
  expect(traverse).not.toHaveBeenCalled();
  expect(workspaceStore.getState().panes.some((pane) => pane.id === inspectorId)).toBe(false);
  expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
  traverse.mockRestore();
  host.unmount();
});

test("ordinary focus after an attention hop publishes its own session URL", async () => {
  window.history.replaceState({}, "", "/s/local%3Aa");
  const origin = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const independent = workspaceStore.getState().openPane("session", { ref: "local:x" }, { slot: "secondary" });
  const host = await act(async () => render(<StackHost />));
  await act(async () => workspaceStore.getState().focusPane(origin));
  await act(async () => {
    openAttentionSession("local:b");
  });
  const destination = workspaceStore.getState().mainPane()?.id;
  if (!destination) throw new Error("attention hop did not place its destination");
  expect(workspaceStore.getState().focusedPaneId).toBe(destination);
  expect(window.location.pathname).toBe("/s/local%3Ab");

  await act(async () => workspaceStore.getState().focusPane(independent));

  expect(window.location.pathname).toBe("/s/local%3Ax");
  host.unmount();
});

// Fixture pane type for the bulk of this file's tests - non-singleton, so
// "doc"/{ref} with different refs are distinct panes (same dedup rule
// workspace.ts itself uses), and "doc" has no deep link (routing.ts's
// paneToURL returns null for it) which the URL-sync tests further down
// need a real example of. A local click counter proves whether a mount
// survived a focus change (StackHost's own remount-safety contract, same
// one DockHost's PaneHost documents - see this component's own comment).
// The doc panes mounted right now, by label: what a pane's subscriptions
// would see.
const liveDocPanes = new Set<string>();

function DocFixture({ params, paneId, focused }: PaneProps<{ ref?: string; path?: string }>) {
  const [clicks, setClicks] = useState(0);
  const visible = usePaneVisible();
  const label = params.ref ?? params.path ?? "unknown";
  useEffect(() => {
    liveDocPanes.add(label);
    return () => {
      liveDocPanes.delete(label);
    };
  }, [label]);
  return (
    <div>
      <p data-testid={`doc-visibility-${label}`} data-pane-visible={String(visible)}>
        doc pane: {label} (focused={String(focused)}) (paneId={paneId})
      </p>
      <div data-testid={`doc-scroller-${label}`} />
      <button type="button" onClick={() => setClicks((c) => c + 1)}>
        clicks: {clicks}
      </button>
    </div>
  );
}

function SettingsFixture({ params }: PaneProps<{ section?: string }>) {
  return <div>settings pane: {params.section ?? "none"}</div>;
}

// The product opens a conversation as a session pane and keeps its parent
// context beside the pane (workspace.ts's recordConversationOpenParent),
// never in the pane params, so a retained-parent fixture must build that
// shape. An absent, empty, self, or non-string parent records no edge - the
// same rejection the product's legacy fallback applies before it stores one.
function openConversation(ref: string, parentRef?: unknown): string {
  const id = workspaceStore.getState().openPane("session", { ref }, { slot: "secondary" });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === id);
  if (!pane) throw new Error(`fixture did not open the ${ref} conversation`);
  recordConversationOpenParent(pane, parentRef as string | undefined);
  return id;
}

async function openSessionsPanel(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  expect(screen.getByRole("dialog", { name: "Sessions" })).toBeTruthy();
}

class RestoreDockviewApi {
  panels: Array<{ id: string; params: unknown }> = [];
  activePanel: { id: string } | undefined = undefined;
  received: unknown;

  constructor(
    readonly serialized: unknown,
    private readonly restoredPanels: Array<{ id: string; params: unknown }>,
    private readonly restoredActivePanel: { id: string },
  ) {}

  toJSON(): unknown {
    return this.serialized;
  }

  fromJSON(data: unknown): void {
    this.received = data;
    this.panels = this.restoredPanels;
    this.activePanel = this.restoredActivePanel;
  }

  clear(): void {
    this.panels = [];
    this.activePanel = undefined;
  }

  removePanel(panel: { id: string }): void {
    this.panels = this.panels.filter((candidate) => candidate.id !== panel.id);
    if (this.activePanel?.id === panel.id) this.activePanel = undefined;
  }
}

function asDockviewApi(fake: RestoreDockviewApi): DockviewApi {
  return fake as unknown as DockviewApi;
}

// motion-dom initializes its prefers-reduced-motion listener once per module
// load against window.matchMedia("(prefers-reduced-motion)"). Installing a
// MediaQueryList with real change notifications up front lets the reduced-
// motion test toggle the preference through the library's own listener.
let reducedMotionMatches = false;
const reducedMotionListeners = new Set<() => void>();

function setReducedMotionPreference(matches: boolean): void {
  reducedMotionMatches = matches;
  for (const listener of reducedMotionListeners) listener();
}

const defaultMatchMedia: typeof window.matchMedia = (media) => {
  const isReducedMotion = media === "(prefers-reduced-motion)" || media === "(prefers-reduced-motion: reduce)";
  return {
    media,
    get matches() {
      return isReducedMotion ? reducedMotionMatches : false;
    },
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject | null) => {
      if (isReducedMotion && typeof listener === "function") reducedMotionListeners.add(listener as () => void);
    },
    removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject | null) => {
      if (isReducedMotion && typeof listener === "function") reducedMotionListeners.delete(listener as () => void);
    },
    dispatchEvent: () => false,
  };
};

function installTabletMatchMedia(): () => void {
  const originalMedia = window.matchMedia;
  window.matchMedia = ((media: string) =>
    media === "(prefers-reduced-motion)" || media === "(prefers-reduced-motion: reduce)"
      ? defaultMatchMedia(media)
      : ({
          media,
          matches: media === MOBILE_QUERY || media === TABLET_QUERY,
          addEventListener() {},
          removeEventListener() {},
        } as unknown as MediaQueryList)) as typeof window.matchMedia;
  return () => {
    window.matchMedia = originalMedia;
  };
}

beforeAll(async () => {
  window.matchMedia = defaultMatchMedia;
  registerPaneForTests<{ ref?: string; path?: string }>({
    id: "doc",
    title: (params) => `Doc ${params.ref ?? params.path ?? "unknown"}`,
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  registerPaneForTests<{ section?: string }>({
    id: "settings",
    singleton: true,
    title: (params) => `Settings${params.section ? `: ${params.section}` : ""}`,
    component: lazy(() => Promise.resolve({ default: SettingsFixture })),
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
  // render still suspends. An already-resolved promise does not dodge it: the
  // doc fixture above is lazy(() => Promise.resolve(...)) and suspends once
  // all the same. A reveal that commits outside act waits out react-dom's
  // FALLBACK_THROTTLE_MS (300ms, react-dom 19.2) on a real timer, so warmPane
  // renders inside an awaited act, where the reveal commits as soon as the
  // chunk resolves. Each landmark wait still gets PANE_WARMUP_TRIPWIRE_MS, the
  // same tripwire as App.test.tsx's warmRoute.
  await warmPane(
    () => {}, // nothing focused: StackHost's own fallback opens welcome
    (timeout) => findStackedEmptyState("welcome", timeout),
  );
  await warmPane(
    () => workspaceStore.getState().openPane("doc", { ref: "ref_warm" }),
    (timeout) => screen.findByText(/doc pane: ref_warm/, undefined, { timeout }),
  );
  await warmPane(
    () => workspaceStore.getState().openPane("session", { ref: "local:ref_warm" }),
    (timeout) => screen.findByRole("heading", { name: "local:ref_warm" }, { timeout }),
  );
});

// A warm-up render has no responsiveness bar to hold, so this is a tripwire
// for a hung render, not a responsiveness budget. Same value and reasoning as App.test.tsx's own
// WARM_ROUTE_TRIPWIRE_MS.
const PANE_WARMUP_TRIPWIRE_MS = 10_000;

// Waits until the stack shows a pane of `paneType` whose body has rendered
// its empty state: welcome's "nothing open" landing. The pane type comes from
// the stack body's own marker, so
// this proves which pane is on screen without reading its copy.
async function findStackedEmptyState(paneType: string, timeout?: number): Promise<HTMLElement> {
  return waitFor(
    () => {
      const body = document.querySelector(`[data-stacked-pane-type="${paneType}"]`);
      if (!(body instanceof HTMLElement)) throw new Error(`No stacked ${paneType} pane`);
      return within(body).getByTestId("empty-state");
    },
    { timeout },
  );
}

// Renders StackHost once with `open`'s pane focused and awaits its landmark,
// so both halves of that pane's lazy-loading cost are already paid by the
// time a test measures it. See the beforeAll above for why the module cache
// alone is not enough.
async function warmPane(open: () => void, findLandmark: (timeout: number) => Promise<unknown>): Promise<void> {
  open();
  await act(async () => {
    render(<StackHost />);
  });
  await findLandmark(PANE_WARMUP_TRIPWIRE_MS);
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetNavigationStoreForTests();
  setLastPopstateWasTrustedForTests(false);
  window.history.pushState({}, "", "/");
}

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetNavigationStoreForTests();
  resetConnectionStoreForTests();
  resetChromeStoreForTests();
  topNotesStore.getState().resetForTests();
  setLastPopstateWasTrustedForTests(false);
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  setReducedMotionPreference(false);
  setLastPopstateWasTrustedForTests(false); // defensive: no test should leak this into the next
});

test("falls back to opening welcome when nothing is focused at mount", async () => {
  render(<StackHost />);
  await findStackedEmptyState("welcome");
});

test("opens the mobile panel when nothing is focused", async () => {
  render(<StackHost />);
  // Welcome pane mounts behind the panel; the panel opens over it
  await findStackedEmptyState("welcome");
  // The panel's Sheet title "Sessions" is the dialog's accessible name
  expect(screen.getByRole("dialog", { name: "Sessions" })).toBeTruthy();
});

test("the Duo edge controls open and dismiss Sessions and yield to a nested dialog", async () => {
  const restore = installMobileViewport();
  const ua = vi
    .spyOn(navigator, "userAgent", "get")
    .mockReturnValue("EvenerNative/1 EvenerSessionHeader/1 EvenerSessionsToolbar/1 EvenerPaneNavigation/1");
  let toolbar: { id: string; mode: string | null; needsYou?: number } | undefined;
  const modes: (string | null)[] = [];
  vi.stubGlobal("evenerEdgeToolbar", true);
  vi.stubGlobal("ReactNativeWebView", {
    postMessage(json: string) {
      const message = JSON.parse(json);
      if (message.type === "sessionsToolbar") {
        toolbar = message;
        modes.push(message.mode);
      }
    },
  });
  const send = (id: string | undefined, action: string) =>
    act(() =>
      window.dispatchEvent(
        new MessageEvent("message", { data: JSON.stringify({ type: "sessionsAction", id, action }) }),
      ),
    );
  try {
    const filter = <Input role="searchbox" aria-label="Filter" value="" onChange={() => {}} />;
    const { rerender } = render(<StackHost railSlot={filter} />);
    await screen.findByRole("dialog");
    await waitFor(() => expect(toolbar?.mode).toBe("list"));
    expect(document.activeElement).toBe(screen.getByRole("dialog"));
    await userEvent.click(screen.getByRole("searchbox"));
    expect(document.activeElement).toBe(screen.getByRole("searchbox"));
    const listID = toolbar?.id;
    rerender(
      <StackHost
        railSlot={
          <Dialog open onClose={() => {}} title="Confirm">
            <Badge count={1} />
          </Dialog>
        }
      />,
    );
    await waitFor(() => expect(toolbar?.mode).toBeNull());
    send(listID, "close");
    expect(screen.getAllByRole("dialog")).toHaveLength(2);
    rerender(<StackHost />);
    await waitFor(() => expect(toolbar?.mode).toBe("list"));
    send(listID, "close");
    await waitFor(() => expect(toolbar?.mode).toBe("launcher"));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(within(screen.getByTestId("topbar")).queryByRole("button")).toBeNull();
    send(toolbar?.id, "open");
    await waitFor(() => expect(toolbar?.mode).toBe("list"));
    send(listID, "close");
    expect(screen.getByRole("dialog")).toBeTruthy();
    send(toolbar?.id, "settings");
    expect(window.location.pathname).toBe("/settings");
    expect(screen.queryByRole("dialog")).toBeNull();
    act(() => workspaceStore.getState().openPane("settings", { section: "appearance" }));
    await waitFor(() => expect(toolbar).toMatchObject({ mode: "navigation", back: true, destination: "open" }));
    expect(within(screen.getByTestId("topbar")).queryByRole("button")).toBeNull();
    modes.length = 0;
    act(() => setNeedsYouCount(2));
    expect(toolbar?.needsYou).toBe(2);
    expect.soft(modes).not.toContain(null);
    rerender(<StackHost routeDeferred />);
    expect.soft(toolbar?.mode).toBeNull();
    rerender(<StackHost />);
    const paneBack = vi.fn();
    act(() => chromeStore.getState().setPaneBack(paneBack));
    send(toolbar?.id, "back");
    expect(paneBack).toHaveBeenCalledOnce();
    send(toolbar?.id, "open");
    await screen.findByRole("dialog");
  } finally {
    cleanup();
    vi.unstubAllGlobals();
    ua.mockRestore();
    restore();
  }
});

test.each([
  { installed: false, pane: "session" },
  { installed: true, pane: "session" },
  { installed: false, pane: "welcome" },
  { installed: true, pane: "welcome" },
  { installed: false, pane: "settings" },
  { installed: true, pane: "settings" },
])(
  "Sessions uses the free edge and restores the opener on $pane when installed=$installed",
  async ({ installed, pane }) => {
    const user = userEvent.setup();
    const restoreViewport = installMobileViewport();
    Object.defineProperty(navigator, "standalone", { configurable: true, value: installed });
    document.documentElement.style.setProperty("--home-safe-right", "84px");
    try {
      if (pane === "session") workspaceStore.getState().openPane("session", { ref: "local:root-navigation" });
      if (pane === "settings") workspaceStore.getState().openPane("settings", {});
      render(<StackHost />);
      if (pane === "welcome") {
        await screen.findByRole("dialog");
        await user.keyboard("{Escape}");
      }
      await screen.findByTestId("topbar-title");
      if (pane === "settings") {
        const back = screen.getByRole("button", { name: "Back" });
        expect(Boolean(back.closest("[data-home-toolbar]"))).toBe(installed);
      } else {
        expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
      }
      const opener = screen.getByRole("button", { name: "Sessions" });
      expect(opener.closest("[role=toolbar]")?.getAttribute("data-home-toolbar") ?? null).toBe(
        installed ? "right" : null,
      );
      const bar = screen.getByTestId("topbar");
      expect(bar.contains(opener)).toBe(!installed);
      if (!installed) {
        // The leading cluster owns the trigger wherever Back is absent; a
        // Back-owning pane keeps it trailing so the trigger never disappears
        // behind Back.
        const cluster = screen.queryByRole("button", { name: "Back" }) ? "trailing" : "leading";
        expect(opener.closest(`.${stackStyles[cluster]}`)).toBeTruthy();
      }
      await openSessionsPanel(user);
      expect(screen.getByRole("dialog").classList.contains(mobilePanelStyles.right ?? "")).toBe(installed);
      await user.keyboard("{Escape}");
      expect(document.activeElement).toBe(opener);
    } finally {
      cleanup();
      document.documentElement.style.removeProperty("--home-safe-right");
      Reflect.deleteProperty(navigator, "standalone");
      restoreViewport();
    }
  },
);

test("a focused session's top bar orders Find before Overview", async () => {
  const restoreViewport = installMobileViewport();
  try {
    workspaceStore.getState().openPane("session", { ref: "local:header-order" });
    render(<StackHost />);
    const title = await screen.findByTestId("topbar-title");
    const topBar = title.parentElement;
    if (!topBar) throw new Error("session top bar is missing");

    const find = await within(topBar).findByRole("button", { name: "Find in session" });
    const overview = within(topBar).getByRole("button", { name: "Overview" });
    expect(find.compareDocumentPosition(overview) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  } finally {
    restoreViewport();
  }
});

test("the iPhone controls stay above inline Find and follow the focused reader", async () => {
  const restoreViewport = installMobileViewport();
  const ua = vi.spyOn(navigator, "userAgent", "get").mockReturnValue("EvenerNative/1 EvenerSessionHeader/1");
  const height = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(61);
  const postMessage = vi.fn<(message: string) => void>();
  vi.stubGlobal("ReactNativeWebView", { postMessage });
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  const header = () =>
    postMessage.mock.calls
      .map(([json]) => JSON.parse(json))
      .filter((message) => message.type === "sessionHeader")
      .at(-1)?.header;
  const press = (id: string, action: string) =>
    act(async () => {
      window.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "headerAction", id, action }) }));
    });
  const ref = "remote:header";
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const result = activityDetailsThread(ref, {
      turns: [{ id: "t", status: "completed", itemsView: "full", items: [] }],
    });
    result.thread.evener.capabilities.sharedNotes = true;
    return result;
  });
  connectionStore.getState().connect(client);
  const pane = workspaceStore.getState().openPane("session", { ref });
  setNeedsYouCount(3);
  try {
    render(
      <ClientProvider client={client}>
        <StackHost />
      </ClientProvider>,
    );
    await waitFor(() => expect(header()).toMatchObject({ id: pane, leading: "sessions", height: 61, needsYou: 3 }));
    const bar = screen.getByTestId("topbar");
    expect(bar.hasAttribute("inert")).toBe(true);
    const scroller = document.querySelector("[data-reading-scroll-host] > *");
    if (!scroller) throw new Error("missing transcript scroller");
    const notes = screen.getByTestId("top-notes-panel");
    const foldedHeight = bar.offsetHeight + notes.offsetHeight;
    const scroll = (top: number) =>
      act(() => {
        scroller.scrollTop = top;
        scroller.dispatchEvent(new Event("scroll"));
      });
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 2400 });
    scroll(2400);
    act(() => {
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -2400, bubbles: true }));
    });
    scroll(0);
    act(() => {
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: 240, bubbles: true }));
    });
    scroll(240);
    expect(header()?.collapsed).toBe(true);
    expect(scroller.scrollTop).toBe(240 - foldedHeight);
    await press(pane, "find");
    const field = await within(await screen.findByTestId("find-bar")).findByRole("searchbox");
    expect(document.activeElement).toBe(field);
    await waitFor(() => expect(header()).toMatchObject({ id: pane, collapsed: false }));
    scroll(340);
    expect(header()?.collapsed).toBe(false);
    expect(bar.hasAttribute("inert")).toBe(true);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(header()?.id).toBe(pane));
    const child = await act(async () => openConversation("remote:header-child", ref));
    await waitFor(() => expect(header()).toMatchObject({ id: child, leading: "back" }));
    await press(pane, "back");
    expect(workspaceStore.getState().focusedPaneId).toBe(child);
    await press(child, "back");
    expect(workspaceStore.getState().focusedPaneId).toBe(pane);
    await press(pane, "sessions");
    expect(await screen.findByRole("dialog")).toBeTruthy();
    expect(header()).toBeNull();
  } finally {
    cleanup();
    restoreViewport();
    vi.unstubAllGlobals();
    ua.mockRestore();
    height.mockRestore();
  }
});

test("the inner iPhone window keeps the Board and hands focused pane actions to the system edge bar", async () => {
  const restore = installMobileViewport([MOBILE_QUERY, "(min-width: 648px) and (min-height: 600px)"]);
  const ua = vi
    .spyOn(navigator, "userAgent", "get")
    .mockReturnValue(
      "EvenerNative/1 EvenerSessionHeader/1 EvenerWorkspaceControls/1 EvenerSessionsToolbar/1 EvenerPaneNavigation/1",
    );
  const height = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(58);
  const messages: { type: string; id?: string; header?: { leading: string | null } | null }[] = [];
  vi.stubGlobal("ReactNativeWebView", { postMessage: (json: string) => messages.push(JSON.parse(json)) });
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  const stop = connectIPhoneHost();
  const send = (command: unknown) =>
    act(() => window.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(command) })));
  const lastHeader = () => messages.filter(({ type }) => type === "sessionHeader").at(-1)?.header;
  try {
    const id = workspaceStore.getState().openPane("session", { ref: "local:duo" });
    render(
      <ClientProvider client={activityClient()}>
        <StackHost railSlot={<TabletRailActionSlot />} />
      </ClientProvider>,
    );
    const board = await screen.findByTestId("board-column");
    const bar = screen.getByTestId("topbar");
    expect(bar.hasAttribute("inert")).toBe(false);
    // Rotation can present the already-scoped toolbar before a response from the web process.
    expect(lastHeader()).toMatchObject({ id, leading: null, split: true });
    send({ type: "material", glass: true, edgeToolbar: true });
    await waitFor(() => expect(lastHeader()).toMatchObject({ id, leading: null }));
    expect(bar.hasAttribute("data-system-edge-actions")).toBe(true);
    expect(within(bar).getByRole("heading")).toBe(screen.getByTestId("topbar-title"));
    await userEvent.click(within(board).getByTestId("board-toggle"));
    await waitFor(() => expect(lastHeader()?.leading).toBe("sessions"));
    send({ type: "headerAction", id, action: "sessions" });
    await waitFor(() => expect(lastHeader()?.leading).toBeNull());
    send({ type: "headerAction", id, action: "overview" });
    expect(await screen.findByRole("dialog")).toBeTruthy();
    await waitFor(() => expect(lastHeader()).toBeNull());
    expect(within(bar).queryAllByRole("button")).toHaveLength(0);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(lastHeader()).toMatchObject({ id }));
    send({ type: "material", glass: true, edgeToolbar: false });
    await waitFor(() => expect(bar.hasAttribute("data-system-edge-actions")).toBe(false));
    expect(within(bar).getAllByRole("button").length).toBeGreaterThan(0);
    expect(screen.getByTestId("board-column")).toBeTruthy();
    expect(workspaceStore.getState().focusedPaneId).toBe(id);
    act(() => {
      workspaceStore.getState().openPane("settings", {});
      setNeedsYouCount(3);
    });
    const settingsId = workspaceStore.getState().focusedPaneId;
    send({ type: "material", glass: true, edgeToolbar: true });
    const paneToolbar = () => messages.filter(({ type }) => type === "sessionsToolbar").at(-1);
    await waitFor(() => expect(paneToolbar()).toMatchObject({ mode: "navigation", destination: null }));
    await userEvent.click(within(screen.getByTestId("board-column")).getByTestId("board-toggle"));
    await waitFor(() => expect(paneToolbar()).toMatchObject({ mode: "navigation", destination: "board", needsYou: 3 }));
    send({ type: "sessionsAction", id: paneToolbar()?.id, action: "board" });
    await waitFor(() => expect(paneToolbar()).toMatchObject({ mode: "navigation", destination: null }));
    expect(screen.getByTestId("board-column")).toBeTruthy();
    expect(workspaceStore.getState().focusedPaneId).toBe(settingsId);
  } finally {
    cleanup();
    stop?.();
    restore();
    ua.mockRestore();
    height.mockRestore();
    vi.unstubAllGlobals();
  }
});

test("a session with a retained parent keeps Back instead of Sessions", async () => {
  const user = userEvent.setup();
  const parent = openConversation("local:navigation-parent");
  openConversation("local:navigation-child", "local:navigation-parent");
  render(<StackHost />);
  await screen.findByTestId("topbar-title");

  expect(screen.queryByRole("button", { name: "Sessions" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(parent);
});

test("a session gaining a valid back-stack destination swaps Sessions for Back", async () => {
  const user = userEvent.setup();
  const documentPane = workspaceStore.getState().openPane("doc", { ref: "navigation-origin" });
  render(<StackHost />);
  await screen.findByText(/doc pane: navigation-origin/);

  await act(async () => workspaceStore.getState().openPane("session", { ref: "local:backstack-navigation" }));

  expect(screen.queryByRole("button", { name: "Sessions" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(documentPane);
});

test("Activity opened from the mobile drawer supersedes the drawer", async () => {
  const ref = "remote:drawer-activity";
  const client = activityClient();
  connectionStore.getState().connect(client);
  const user = userEvent.setup();
  workspaceStore.getState().openPane("doc", { ref });

  render(
    <MotionProvider>
      <StackHost
        railSlot={
          <button
            type="button"
            onClick={() => {
              workspaceStore.getState().openPane("doc", { ref });
              activitySidebarStore.getState().openFor(ref, "jobs");
            }}
          >
            Activity for drawer session
          </button>
        }
      />
    </MotionProvider>,
  );

  await screen.findByText(new RegExp(`doc pane: ${ref}`));
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  await user.click(screen.getByRole("button", { name: "Activity for drawer session" }));

  expect(screen.queryByRole("dialog", { name: "Sessions" })).toBeNull();
  const sidebar = await screen.findByTestId("activity-sidebar");
  expect(sidebar).toBeTruthy();
  // The drawer's close restores focus to its own opener; the sidebar's
  // selected category must take focus back after that restore.
  await waitFor(() => expect(document.activeElement).toBe(within(sidebar).getByRole("tab", { name: /Jobs/ })));
});

test("renders the new activity sidebar over the mobile stack with the selected tab", async () => {
  const ref = "remote:activity";
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref: requested, scope }) => ({
    context: activityContext(requested),
    scope: scope ?? "session",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  workspaceStore.getState().openPane("doc", { ref });
  activitySidebarStore.getState().openWith("jobs");

  render(
    <MotionProvider>
      <StackHost />
    </MotionProvider>,
  );

  expect(await screen.findByTestId("activity-sidebar")).toBeTruthy();
  expect(screen.getByRole("tab", { name: /Jobs/ })).toBeTruthy();
});

test.each(["Hide", "Escape"])(
  "%s returns phone Overview focus to the revealed conversation after child drilling",
  async (dismiss) => {
    const restoreViewport = installMobileViewport();
    const parent = "remote:focus-parent";
    const child = "remote:focus-child";
    const client = activityClient();
    const context = (ref: string) => ({
      ...activityContext(ref),
      rootRef: parent,
      ancestors: ref === parent ? [] : [{ ref: parent, sessionId: "parent", title: "Parent work" }],
    });
    client.on("thread/read", ({ ref }) =>
      activityDetailsThread(ref, { name: ref === parent ? "Parent work" : "Child work" }),
    );
    client.on("evener/thread/activity/read", ({ ref, scope }) => ({
      ...activitySummary(ref, scope),
      context: context(ref),
    }));
    client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
      context: context(ref),
      scope: scope ?? "session",
      delegates:
        ref === parent ? [activityDelegate({ ownerRef: parent, childRef: child, description: "Open child" })] : [],
      page: { complete: true, issues: [] },
    }));
    connectionStore.getState().connect(client);
    const parentPane = workspaceStore.getState().openPane("session", { ref: parent });
    const user = userEvent.setup();
    try {
      render(
        <ClientProvider client={client}>
          <MotionProvider>
            <StackHost />
          </MotionProvider>
        </ClientProvider>,
      );
      await user.click(await screen.findByRole("button", { name: "Overview" }));
      const sidebar = await screen.findByTestId("activity-sidebar");
      await user.click(within(sidebar).getByRole("tab", { name: /^Agents,/ }));
      await user.click(await screen.findByRole("button", { name: /Open child/ }));
      // Drilling changes the inspected scope. The new scope's selected
      // category must own focus inside the trap before the reader touches
      // another control.
      // Entry focus lands in one queued frame, so the short bound reports a
      // regression as the focus mismatch, not as the test's own deadline.
      await waitFor(() => expect(document.activeElement).toBe(within(sidebar).getByRole("tab", { name: /Agents/ })), {
        timeout: 2_000,
      });
      await user.click(within(sidebar).getByRole("tab", { name: "About" }));
      await within(sidebar).findByText("~$1.00");
      await screen.findByRole("heading", { name: "Child work" });
      const before = workspaceStore.getState();
      expect(before.focusedPaneId).not.toBe(parentPane);
      if (dismiss === "Hide") await user.click(within(sidebar).getByRole("button", { name: "Hide Overview" }));
      else await user.keyboard("{Escape}");
      // The read-only transcript's ancestor link is gone: the revealed
      // conversation owns the return focus through its own actions control.
      const childActions = await screen.findByRole("button", { name: "Overview" });
      await waitFor(() => expect(document.activeElement).toBe(childActions));
      await waitFor(() => expect(screen.queryByTestId("activity-sidebar")).toBeNull());
      expect(workspaceStore.getState().panes).toEqual(before.panes);
      expect(workspaceStore.getState().focusedPaneId).toBe(before.focusedPaneId);
      // The path back to the parent is the retained Back walk.
      await user.click(screen.getByRole("button", { name: "Back" }));
      await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parentPane));
    } finally {
      cleanup();
      restoreViewport();
    }
  },
);

test("at the mobile boundary an ordinary Agents row retains the existing conversation route", async () => {
  const restoreViewport = installMobileViewport();
  const client = activityClient();
  connectionStore.getState().connect(client);
  workspaceStore.getState().openPane("doc", { ref: "remote:owner" });
  activitySidebarStore.getState().openWith("agents");
  try {
    render(
      <ClientProvider client={client}>
        <MotionProvider>
          <StackHost />
        </MotionProvider>
      </ClientProvider>,
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /inspect/ }));
    const state = workspaceStore.getState();
    // The ordinary row keeps opening the thread it always opened - the
    // conversation pane for a session ref, never the cascade - at the
    // mobile boundary where the desktop row would enter a cascade instead.
    expect(state.panes.find((pane) => pane.id === state.focusedPaneId)).toMatchObject({
      type: "session",
      params: { ref: "remote:child" },
    });
    expect(state.panes.some((pane) => pane.type === "sessionZoom")).toBe(false);
  } finally {
    cleanup();
    restoreViewport();
    resetConnectionStoreForTests();
  }
});

test("a restored cascade renders only its selected reader on mobile and restores its desktop columns without changing intent", async () => {
  const original = window.matchMedia;
  const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
  const media = Object.assign(new EventTarget(), {
    matches: true,
    media: MOBILE_QUERY,
    onchange: null,
    addListener() {},
    removeListener() {},
  });
  window.matchMedia = ((query: string) =>
    query === media.media
      ? media
      : Object.assign(new EventTarget(), {
          matches: false,
          media: query,
          onchange: null,
          addListener() {},
          removeListener() {},
        })) as typeof window.matchMedia;
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  const context = (ref: string) =>
    cascadeContext(ref, ref === "grandchild" ? ["root", "child"] : ref === "child" ? ["root"] : []);
  const client = cascadeClient(context);
  const params: SessionZoomParams = {
    ref: "grandchild",
    source: { type: "session", params: { ref: "root" } },
    edges: [
      { ownerRef: "root", childRef: "child", delegateId: "edge-child" },
      { ownerRef: "child", childRef: "grandchild", delegateId: "edge-grandchild" },
    ],
  };
  workspaceStore.setState({
    panes: [{ id: "restored-cascade", type: "sessionZoom", slot: "main", params }],
    focusedPaneId: "restored-cascade",
  });
  connectionStore.getState().connect(client);
  window.history.replaceState({}, "", "/s/root");
  try {
    await act(async () =>
      render(
        <ClientProvider client={client}>
          <MotionProvider>
            <StackHost />
          </MotionProvider>
        </ClientProvider>,
      ),
    );
    expect(await screen.findByTestId("cascade-column")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Return to previous view" })).toBeNull();
    expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual([
      "grandchild",
    ]);
    expect(screen.queryByTestId("cascade-spine")).toBeNull();
    // The selected column embeds the live conversation, composer included -
    // the read-only reader surface with no composer is gone.
    const editor = screen.getByRole("textbox");
    expect(workspaceStore.getState().panes[0]?.params).toBe(params);
    act(() => editor.focus());
    act(() => activitySidebarStore.getState().openWith("about"));
    const sidebar = await screen.findByTestId("activity-sidebar");
    await within(sidebar).findByText("wire-grandchild");
    expect(within(sidebar).queryByText("wire-child")).toBeNull();
    await userEvent.setup().keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByTestId("activity-sidebar")).toBeNull());
    expect(document.activeElement).toBe(editor);
    expect(workspaceStore.getState().panes[0]?.params).toBe(params);
    expect(workspaceStore.getState().focusedPaneId).toBe("restored-cascade");
    await act(async () => {
      media.matches = false;
      media.dispatchEvent(Object.assign(new Event("change"), { matches: false }));
    });
    expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual([
      "child",
      "grandchild",
    ]);
    expect(screen.getByTestId("cascade-spine").getAttribute("data-scope-ref")).toBe("root");
    expect(workspaceStore.getState().panes[0]?.params).toBe(params);
    expect(window.location.pathname).toBe("/s/root");
    expect(
      screen.getByTestId("pane-actions").contains(screen.getByRole("button", { name: "Return to previous view" })),
    ).toBe(true);
    await act(async () => {
      media.matches = true;
      media.dispatchEvent(Object.assign(new Event("change"), { matches: true }));
    });
    expect(screen.queryByRole("button", { name: "Return to previous view" })).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
    expect(workspaceStore.getState().panes[0]).toMatchObject({
      id: "restored-cascade",
      type: "session",
      params: { ref: "root" },
    });
    expect(await screen.findByText("root real content")).toBeTruthy();
    expect(screen.queryByTestId("cascade-column")).toBeNull();
  } finally {
    cleanup();
    resetTranscriptDisplayStoreForTests();
    if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
    else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
    if (original) window.matchMedia = original;
    else Reflect.deleteProperty(window, "matchMedia");
    resetConnectionStoreForTests();
  }
});

test("does not flash the panel during routeDeferred", async () => {
  render(<StackHost routeDeferred />);
  // Wait for welcome to appear behind the (closed) panel
  await findStackedEmptyState("welcome");
  // Panel should NOT be open while routeDeferred
  expect(screen.queryByRole("dialog", { name: "Sessions" })).toBeNull();
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

test("publishes only the rendered mobile foreground pane as visible", async () => {
  workspaceStore.getState().openPane("doc", { ref: "background" });
  workspaceStore.getState().openPane("doc", { ref: "foreground" });
  render(<StackHost />);
  const foreground = await screen.findByTestId("doc-visibility-foreground");
  expect(foreground.getAttribute("data-pane-visible")).toBe("true");
  expect(screen.queryByTestId("doc-visibility-background")).toBeNull();
});

test("switching the focused pane (workspace.focusPane) swaps which one is rendered", async () => {
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_b/);

  act(() => {
    workspaceStore.getState().focusPane(first);
  });

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(screen.queryByText(/doc pane: ref_b/)).toBeNull();
});

// StackHost and DockHost must consume the same store-level primary replacement
// contract. A mobile host that only changes its focused pane leaves the stale
// secondary doc in shared state and can render the wrong pane after a route.
test("renders the replacement session and drops stale secondary panes from the shared workspace", async () => {
  const workspace = workspaceStore.getState();
  workspace.replacePrimary("settings", {});
  workspace.openPane("doc", { ref: "secondary" }, { slot: "secondary" });
  openTopLevelSession("local:session-a");
  const replacementId = workspaceStore.getState().mainPane()!.id;

  render(<StackHost />);

  expect(workspaceStore.getState().panes).toEqual([
    {
      id: replacementId,
      type: "session",
      params: { ref: "local:session-a" },
      slot: "main",
      composerSeatId: expect.any(String),
    },
  ]);
  expect(await screen.findByRole("heading", { name: "local:session-a" })).toBeTruthy();
  expect(screen.queryByText(/doc pane: secondary/)).toBeNull();
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

  act(() => {
    workspaceStore.getState().focusPane(first);
  });
  await screen.findByText(/doc pane: ref_a/);
  await user.click(screen.getByRole("button", { name: /clicks: 0/ }));
  expect(screen.getByRole("button", { name: /clicks: 1/ })).toBeTruthy();

  act(() => {
    workspaceStore.getState().focusPane(second);
  });
  await screen.findByText(/doc pane: ref_b/);
  act(() => {
    workspaceStore.getState().focusPane(first);
  });

  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();
  expect(screen.getByRole("button", { name: /clicks: 0/ })).toBeTruthy(); // reset, not preserved
});

// --- tree drawer trigger ------------------------------------------------

test("hosts the tree drawer trigger in the top bar, regardless of which pane is focused", async () => {
  render(<StackHost />);
  expect(await screen.findByRole("button", { name: "Sessions" })).toBeTruthy();
});

test("a single-pane route offers no tree drawer trigger", async () => {
  render(<StackHost singlePane />);
  expect(await screen.findByTestId("topbar-title")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Sessions" })).toBeNull();
});

// --- back navigation ---------------------------------------------------

test("the back affordance is hidden while welcome (the stack's root) is focused", async () => {
  render(<StackHost />);
  await findStackedEmptyState("welcome");
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
});

test("the back affordance is shown once a non-welcome pane is focused", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  expect(await screen.findByRole("button", { name: "Back" })).toBeTruthy();
});

// The scaffold header is display:none below 900px (panescaffold.module.css),
// so the top bar is the ONLY visible chrome a phone user has. A panel pane's
// Back there must return to the parent session - never pop the generic stack,
// which would walk to whatever happened to be focused before.
test("a session panel pane's top-bar Back returns to the parent session, not the back stack", async () => {
  const user = userEvent.setup();
  await import("../../panes/sessionPanels");
  workspaceStore.getState().openPane("doc", { ref: "ref_before" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_before/);

  act(() => {
    workspaceStore.getState().openPane("sessionDetails", { ref: "local:ref_panel" });
  });
  const sessionDetails = await waitFor(() => {
    const body = document.querySelector('[data-stacked-pane-type="sessionDetails"]');
    if (!(body instanceof HTMLElement)) throw new Error("No stacked sessionDetails pane");
    return body;
  });
  expect(await within(sessionDetails).findByRole("status")).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(workspaceStore.getState().panes).toContainEqual(
    expect.objectContaining({ type: "session", params: { ref: "local:ref_panel" } }),
  );
  const state = workspaceStore.getState();
  const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
  expect(focused?.type).toBe("session");
});

test("back returns to the pane that was focused before the current one", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);

  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // a forward navigation, observed live
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("document Back prefers its retained exact origin over transient local history", async () => {
  const session = "local:02wMz5TxvEMoJEDTDGOTil";
  const workspace = workspaceStore.getState();
  const sourceId = workspace.openPane("session", { ref: session });
  const unrelated = workspace.openPane("doc", { ref: "unrelated" }, { slot: "secondary" });
  render(<StackHost />);
  await screen.findByText(/doc pane: unrelated/);
  const reference = bindFilePath("docs/a.md", "/work/child");
  if (!reference) throw new Error("fixture did not bind document");
  act(() => openDocBeside({ session, reference, sourcePaneId: sourceId }));
  await screen.findByText(/doc pane: docs\/a.md/);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(unrelated);

  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
});

test("document Back uses the latest exact same-session origin after reopen and host remount", async () => {
  const session = "local:02wMz5TxvEMoJEDTDGOTil";
  const workspace = workspaceStore.getState();
  const firstSourceId = workspace.openPane("session", { ref: session });
  const secondSourceId = workspace.openPane("transcript", { ref: session }, { slot: "secondary" });
  const firstSource = workspaceStore.getState().panes.find((pane) => pane.id === firstSourceId);
  const secondSource = workspaceStore.getState().panes.find((pane) => pane.id === secondSourceId);
  const reference = bindFilePath("docs/a.md", "/work/child");
  if (!reference || !firstSource || !secondSource) throw new Error("fixture did not create bound sources");

  openDocBeside({ session, reference, sourcePaneId: firstSource.id });
  const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!document) throw new Error("document did not open");
  const firstHost = render(<StackHost />);
  await screen.findByText(/doc pane: docs\/a.md/);
  firstHost.unmount();

  openDocBeside({ session, reference, sourcePaneId: secondSource.id });
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toEqual([document]);
  expect(documentPaneState(document)).toEqual({ reference, origin: secondSource, reopen: 1 });
  workspace.focusPane(firstSource.id);

  render(<StackHost />);
  await screen.findByRole("heading", { name: session });
  act(() => workspace.focusPane(document.id));
  await screen.findByText(/doc pane: docs\/a.md/);
  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));

  const state = workspaceStore.getState();
  expect(state.focusedPaneId).toBe(secondSource.id);
  expect(state.focusedPaneId).not.toBe(firstSource.id);
  expect(state.panes.find((pane) => pane.id === state.focusedPaneId)).toBe(secondSource);
});

test("document Back retains its exact origin across a StackHost remount", async () => {
  const session = "local:02wMz5TxvEMoJEDTDGOTil";
  const workspace = workspaceStore.getState();
  const sourceId = workspace.openPane("session", { ref: session });
  const reference = bindFilePath("docs/a.md", "/work/child");
  if (!reference) throw new Error("fixture did not bind document");
  openDocBeside({ session, reference, sourcePaneId: sourceId });
  const first = render(<StackHost />);
  await screen.findByText(/doc pane: docs\/a.md/);
  first.unmount();

  render(<StackHost />);
  await screen.findByText(/doc pane: docs\/a.md/);
  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
});

test("document Back navigates to its bound session after the exact origin closes", async () => {
  const session = "local:02wMz5TxvEMoJEDTDGOTil";
  const workspace = workspaceStore.getState();
  const sourceId = workspace.openPane("session", { ref: session });
  const reference = bindFilePath("docs/a.md", "/work/child");
  if (!reference) throw new Error("fixture did not bind document");
  openDocBeside({ session, reference, sourcePaneId: sourceId });
  workspace.closePane(sourceId);
  render(<StackHost />);
  await screen.findByText(/doc pane: docs\/a.md/);

  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(session)}`);
});

test("restored document Back cannot follow a source id reused from the retired lifetime", async () => {
  const session = "local:02wMz5TxvEMoJEDTDGOTil";
  const workspace = workspaceStore.getState();
  const sourceId = workspace.openPane("session", { ref: session });
  const oldSource = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const reference = bindFilePath("docs/a.md", "/work/child");
  if (!reference || !oldSource) throw new Error("fixture did not create a bound source");
  openDocBeside({ session, reference, sourcePaneId: sourceId });
  const oldDocument = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!oldDocument) throw new Error("document did not open");
  expect(documentPaneState(oldDocument)).toEqual({ reference, origin: oldSource, reopen: 0 });

  const saved = {
    grid: { root: { type: "branch", data: [] } },
    panels: {
      [sourceId]: {
        id: sourceId,
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: session } },
      },
      [oldDocument.id]: {
        id: oldDocument.id,
        contentComponent: "default",
        params: { paneType: "doc", paneParams: oldDocument.params },
      },
    },
    activeGroup: "group-2",
  };
  const fake = new RestoreDockviewApi(
    saved,
    [
      { id: sourceId, params: { paneType: "session", paneParams: { ref: session } } },
      { id: oldDocument.id, params: { paneType: "doc", paneParams: oldDocument.params } },
    ],
    { id: oldDocument.id },
  );
  registerDockviewApi(asDockviewApi(fake));

  expect(workspace.restoreLayout(workspace.layoutJSON())).toBe(true);
  expect(fake.received).toBe(saved);
  const restoredSource = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const restoredDocument = workspaceStore.getState().panes.find((pane) => pane.id === oldDocument.id);
  if (!restoredSource || !restoredDocument) throw new Error("layout did not restore source and document");
  expect(restoredSource).not.toBe(oldSource);
  expect(restoredDocument).not.toBe(oldDocument);
  expect(documentPaneState(oldDocument)).toBeUndefined();
  expect(documentPaneState(restoredDocument)).toBeUndefined();

  render(<StackHost />);
  await screen.findByText(/doc pane: docs\/a.md/);
  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));

  const state = workspaceStore.getState();
  expect(state.focusedPaneId).not.toBe(restoredSource.id);
  expect(state.panes.find((pane) => pane.id === state.focusedPaneId)?.type).toBe("welcome");
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

  await findStackedEmptyState("welcome");
});

test("back walks multiple levels deep, one pane per tap", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await screen.findByText(/doc pane: ref_b/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  });
  await screen.findByText(/doc pane: ref_c/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText(/doc pane: ref_b/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));
  await findStackedEmptyState("welcome");
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull(); // welcome: nothing further behind it
});

test("a back tap does not push the pane it left onto the stack (no ping-pong)", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" })); // -> ref_a
  await screen.findByText(/doc pane: ref_a/);

  // ref_a is StackHost's own root here (it never observed anything before
  // ref_a - see the "falls all the way to welcome" test above) - a second
  // tap must go straight to welcome, not bounce forward to ref_b.
  await user.click(screen.getByRole("button", { name: "Back" }));
  await findStackedEmptyState("welcome");
});

test("retained-parent Back: valid observed history wins over a retained transcript parent", async () => {
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
  const previous = workspace.openPane("doc", { ref: "previous" }, { slot: "secondary" });
  render(<StackHost />);
  await screen.findByText(/doc pane: previous/);
  act(() => {
    workspace.openPane("transcript", { ref: "local:leaf", parentRef: "local:owner" }, { slot: "secondary" });
  });
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(previous);
  expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
  await user.click(screen.getByRole("button", { name: "Back" }));
  await findStackedEmptyState("welcome");
});

test.each([
  { reason: "missing reference", parentRef: undefined },
  { reason: "null reference", parentRef: null },
  { reason: "non-string reference", parentRef: 42 },
  { reason: "empty reference", parentRef: "" },
  { reason: "absent pane", parentRef: "local:absent" },
  { reason: "closed pane", parentRef: "local:closed" },
  { reason: "self reference", parentRef: "local:leaf" },
  { reason: "non-transcript/session pane", parentRef: "local:document" },
])("retained-parent navigation: $reason opens Sessions without creating a parent", async ({ parentRef }) => {
  const workspace = workspaceStore.getState();
  workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
  workspace.openPane("doc", { ref: "local:document" }, { slot: "secondary" });
  const closed = workspace.openPane("session", { ref: "local:closed" }, { slot: "secondary" });
  workspace.closePane(closed);
  const leaf = openConversation("local:leaf", parentRef);
  const retained = workspaceStore.getState().panes;
  render(<StackHost />);
  expect(workspaceStore.getState().focusedPaneId).toBe(leaf);
  const user = userEvent.setup();
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  await openSessionsPanel(user);
  const state = workspaceStore.getState();
  expect(state.focusedPaneId).toBe(leaf);
  expect(state.panes).toEqual(retained);
});

test("retained-parent Back: a non-transcript's parentRef does not override the Welcome fallback", async () => {
  const workspace = workspaceStore.getState();
  workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
  workspace.openPane("doc", { ref: "document", parentRef: "local:owner" }, { slot: "secondary" });
  render(<StackHost />);
  await screen.findByText(/doc pane: document/);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  await findStackedEmptyState("welcome");
});

test.each([false, true])(
  "retained-parent Back: nested=%s walks to the owner without revisiting descendants",
  async (nested) => {
    const workspace = workspaceStore.getState();
    const owner = workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
    const child = openConversation("local:child", "local:owner");
    if (nested) openConversation("local:grandchild", "local:child");
    const retained = workspaceStore.getState().panes;
    render(<StackHost />);
    const user = userEvent.setup();
    if (nested) {
      await user.click(screen.getByRole("button", { name: "Back" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(child);
      expect(workspaceStore.getState().panes).toEqual(retained);
      expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
    }
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(workspaceStore.getState().focusedPaneId).toBe(owner);
    expect(workspaceStore.getState().panes).toEqual(retained);
    expect(workspaceStore.getState().mainPane()).toEqual({
      id: owner,
      type: "session",
      params: { ref: "local:owner" },
      slot: "main",
      composerSeatId: expect.any(String),
    });
    await openSessionsPanel(user);
    expect(workspaceStore.getState().panes).toEqual(retained);
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  },
);

test("retained-parent Back: prefers the session pane when both session and transcript share a ref", async () => {
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
  // Open the transcript FIRST so it precedes the session in the pane array —
  // a bare panes.find would match the transcript, not the session.
  const sharedTranscript = workspace.openPane(
    "transcript",
    { ref: "local:shared", parentRef: "local:owner" },
    { slot: "secondary" },
  );
  const sharedSession = workspace.openPane("session", { ref: "local:shared" }, { slot: "secondary" });
  // The focused leaf's parentRef is "local:shared"; Back should focus the
  // SESSION pane for that ref, not the transcript.
  workspace.openPane("transcript", { ref: "local:leaf", parentRef: "local:shared" }, { slot: "secondary" });
  render(<StackHost />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(sharedSession);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(sharedTranscript);
  expect(workspaceStore.getState().panes).toHaveLength(4);
  expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
});

test.each([
  {
    shape: "reciprocal",
    context: [
      { ref: "local:a", parentRef: "local:b" },
      { ref: "local:b", parentRef: "local:a" },
    ],
  },
  {
    shape: "three-pane",
    context: [
      { ref: "local:a", parentRef: "local:c" },
      { ref: "local:b", parentRef: "local:a" },
      { ref: "local:c", parentRef: "local:b" },
    ],
  },
])(
  "retained-parent cycle safety: $shape cycle terminates at Welcome without revisiting a descendant",
  async ({ context }) => {
    const workspace = workspaceStore.getState();
    const owner = workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
    for (const params of context) openConversation(params.ref, params.parentRef);
    const retained = workspaceStore.getState().panes;
    // All context predates this mount: the real host starts with empty history.
    render(<StackHost />);
    const user = userEvent.setup();
    const visited = new Set([workspaceStore.getState().focusedPaneId]);
    for (let step = 0; step < context.length; step++) {
      const back = screen.queryByRole("button", { name: "Back" });
      if (!back) break;
      await user.click(back);
      const next = workspaceStore.getState().focusedPaneId;
      expect(visited.has(next), "Back must not revisit an abandoned descendant through cyclic parent context").toBe(
        false,
      );
      visited.add(next);
    }
    await findStackedEmptyState("welcome");
    const state = workspaceStore.getState();
    expect(state.panes.find((pane) => pane.id === state.focusedPaneId)?.type).toBe("welcome");
    expect(state.panes.filter((pane) => pane.type !== "welcome")).toEqual(retained);
    expect(state.mainPane()?.id).toBe(owner);
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  },
);

test.each([false, true])(
  "retained-parent cycle safety: longer=%s missing farther ancestor permits exact parents then opens Sessions",
  async (longer) => {
    const workspace = workspaceStore.getState();
    const owner = workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
    // Paired with the cyclic contexts above: only the final link differs.
    const a = openConversation("local:a", "local:missing");
    const b = openConversation("local:b", "local:a");
    if (longer) openConversation("local:c", "local:b");
    const retained = workspaceStore.getState().panes;
    render(<StackHost />);
    const user = userEvent.setup();
    for (const target of longer ? [b, a] : [a]) {
      await user.click(screen.getByRole("button", { name: "Back" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(target);
      expect(workspaceStore.getState().panes).toEqual(retained);
      expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
    }
    await openSessionsPanel(user);
    expect(workspaceStore.getState().panes).toEqual(retained);
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  },
);

test("retained-parent cycle safety: valid observed history wins even when retained context is cyclic", async () => {
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:owner" }, { slot: "main" });
  openConversation("local:a", "local:b");
  const b = openConversation("local:b", "local:a");
  const previous = workspace.openPane("doc", { ref: "previous" }, { slot: "secondary" });
  const retained = workspaceStore.getState().panes;
  render(<StackHost />);
  await screen.findByText(/doc pane: previous/);
  act(() => workspace.focusPane(b));
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(previous);
  expect(workspaceStore.getState().panes).toEqual(retained);
  expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
  await user.click(screen.getByRole("button", { name: "Back" }));
  await findStackedEmptyState("welcome");
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
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // ordinary forward nav: stacks ref_a
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

  await findStackedEmptyState("welcome");
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
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // stacks ref_a
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
  const second = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_b" })); // stacks ref_a
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
  const b = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_b" })); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  }); // stacks ref_b
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
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // stacks ref_a as the back target
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

  await findStackedEmptyState("welcome");
});

test("back skips a stale MIDDLE stack entry and lands on the next valid one behind it", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  const second = await act(async () => workspaceStore.getState().openPane("doc", { ref: "ref_b" })); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  }); // stacks ref_b
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

// --- paneBack channel: a pane's own "up" target takes over the top bar ----
//
// The chrome store's second channel (2026-08-16 settings mobile-nav design):
// a pane whose internal drill-down the workspace cannot see (settings is a
// SINGLETON pane - section switches update its params in place, so its pane
// id never changes and this component's focus-keyed back-stack never
// observes them) publishes its own "up" handler, and the top-bar Back button
// invokes it instead of walking the pane stack. Published host-agnostically,
// exactly like paneTitle (chromeStore.ts's own channel comment): DockHost
// never reads it, so the publishing pane never asks "am I mobile?".

test("a published paneBack handler takes over the top-bar Back button instead of walking the stack", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);

  const paneBack = vi.fn();
  act(() => {
    chromeStore.getState().setPaneBack(paneBack);
  });

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(paneBack).toHaveBeenCalledTimes(1);
  // The pane stack was NOT walked: ref_b is still the focused pane.
  expect(screen.getByText(/doc pane: ref_b \(focused=true\)/)).toBeTruthy();
});

test("clearing paneBack hands the Back button back to the ordinary stack walk", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  }); // stacks ref_a
  await screen.findByText(/doc pane: ref_b/);

  act(() => {
    chromeStore.getState().setPaneBack(() => {});
  });
  act(() => {
    chromeStore.getState().setPaneBack(null);
  });

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("resetChromeStoreForTests clears a published paneBack handler", () => {
  chromeStore.getState().setPaneBack(() => {});

  resetChromeStoreForTests();

  expect(chromeStore.getState().paneBack).toBeNull();
});

// --- URL sync: the address bar always reflects the focused pane -----------
//
// Unlike DockHost, StackHost has no per-pane "tab" carrying its own text -
// Session.tsx's ref (its own PaneScaffold title) appears exactly once per
// test here, so these use findByText, not AppShell.test.tsx's own
// findAllByText-and-count-2 idiom for the tab-plus-body case.

test("the URL updates to a deep-linked pane's URL once it becomes focused", async () => {
  render(<StackHost />);
  await findStackedEmptyState("welcome");

  act(() => {
    workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  });
  await screen.findByRole("heading", { name: "local:ref_x" });

  expect(window.location.pathname).toBe("/s/local%3Aref_x");
});

test("switching between two deep-linked panes updates the URL each time", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  render(<StackHost />);
  await screen.findByRole("heading", { name: "local:ref_x" });
  expect(window.location.pathname).toBe("/s/local%3Aref_x");

  act(() => {
    workspaceStore.getState().openPane("session", { ref: "local:ref_y" });
  });
  await screen.findByRole("heading", { name: "local:ref_y" });

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
  await screen.findByRole("heading", { name: "local:ref_x" });
  act(() => {
    workspaceStore.getState().openPane("session", { ref: "local:ref_y" });
  });
  await screen.findByRole("heading", { name: "local:ref_y" });
  expect(window.location.pathname).toBe("/s/local%3Aref_y");

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  await screen.findByRole("heading", { name: "local:ref_x" });
  expect(window.location.pathname).toBe("/s/local%3Aref_x");
});

// kata bbsv. AppShell needs the AppWire navigation read before it can place a
// /s/{ref} deep link, and no response resolves inside the first commit - so the shell spends a
// beat with the route parsed and unplaced, which the fallback below fills with
// welcome. Publishing welcome's "/" then would throw away the deep link the
// shell is still working on, so routeDeferred suspends the sync for the wait.
test("a deferred route keeps the address bar - the fallback pane does not publish over it", async () => {
  window.history.pushState({}, "", "/s/local%3Aref_deferred");

  render(<StackHost routeDeferred />);
  await findStackedEmptyState("welcome"); // the fallback pane is up and focused

  expect(window.location.pathname).toBe("/s/local%3Aref_deferred");
});

// The other half of the same contract: once the route is placed (routeDeferred
// back to false), the pane that landed publishes its own URL as usual.
test("the sync resumes on the pane that lands once the route is no longer deferred", async () => {
  window.history.pushState({}, "", "/s/local%3Aref_deferred");
  const { rerender } = render(<StackHost routeDeferred />);
  await findStackedEmptyState("welcome");

  act(() => {
    workspaceStore.getState().openPane("session", { ref: "local:ref_placed" });
  });
  rerender(<StackHost />);

  await screen.findByRole("heading", { name: "local:ref_placed" });
  expect(window.location.pathname).toBe("/s/local%3Aref_placed");
});

// kata 098n. /thread/{ref} and /s/{ref} both resolve to the SAME session pane
// (routing.ts), but only the former turns single-pane mode on
// (singlePane.ts's isSinglePaneRoute, read by AppShell off the pathname).
// paneToURL can only ever serialize a session pane back to /s/{ref}, so
// publishing it over a /thread route rewrites a share link into a URL that
// means something else - chrome-stripped becomes ordinary shell. Desktop's
// DockHost writes no URL at all and so leaves the mode alone for the whole
// visit; the sync matches that here rather than inventing a mobile-only
// rewrite.
test("kata 098n: a /thread share link is left alone when it already names the focused pane", async () => {
  window.history.pushState({}, "", "/thread/local%3Aref_shared");
  workspaceStore.getState().openPane("session", { ref: "local:ref_shared" });

  render(<StackHost />);
  await screen.findByRole("heading", { name: "local:ref_shared" });

  expect(window.location.pathname).toBe("/thread/local%3Aref_shared");
});

// The other half: the guard is about THIS pane's own URL, not about /thread
// routes in general. Focusing a DIFFERENT session from a /thread route still
// publishes - otherwise the address bar would be stuck naming a pane that is
// no longer on screen, which is the exact failure the sync exists to prevent.
test("kata 098n: focusing a different session from a /thread route still publishes its URL", async () => {
  window.history.pushState({}, "", "/thread/local%3Aref_shared");
  workspaceStore.getState().openPane("session", { ref: "local:ref_shared" });
  render(<StackHost />);
  await screen.findByRole("heading", { name: "local:ref_shared" });

  act(() => {
    workspaceStore.getState().openPane("session", { ref: "local:ref_other" });
  });
  await screen.findByRole("heading", { name: "local:ref_other" });

  expect(window.location.pathname).toBe("/s/local%3Aref_other");
});

test("mounting already at the focused pane's own URL does not dispatch a redundant popstate", async () => {
  // The pane's OWN url, i.e. exactly what paneToURL emits - percent-encoded,
  // since a ref carries a ":" that does not survive a path segment raw.
  window.history.pushState({}, "", "/s/local%3Aref_x");
  workspaceStore.getState().openPane("session", { ref: "local:ref_x" });
  const handler = vi.fn();
  window.addEventListener("popstate", handler);

  render(<StackHost />);
  await screen.findByRole("heading", { name: "local:ref_x" });

  window.removeEventListener("popstate", handler);
  expect(handler).not.toHaveBeenCalled();
});

// --- bottom safe-area accommodation (requirement 4) -------------------
//
// The blanket host padding that used to carry the bottom inset lifted the
// whole stack - docked composer footer included - off the screen's bottom
// edge, leaving a dead surface-0 band under the input. The accommodation
// moved to where the composer docks (PaneScaffold's footer and body rules);
// the host deliberately carries none of it so the pane chrome fills to the
// bottom edge. jsdom does not evaluate real CSS (env(), viewport units,
// etc.), so - same idiom as sheet.test.tsx's own prefers-reduced-motion
// check - this reads the CSS module's own source rather than asserting on
// computed style, comments stripped so the .host comment's own mention of
// the inset cannot false-positive the match.

test("the host lifts nothing off the bottom edge - docked chrome owns the bottom inset", () => {
  const css = readModuleCss(import.meta.url, "StackHost.module.css");
  expect(css).not.toContain("env(safe-area-inset-bottom)");
  const hostRule = css.match(/\.host \{([^}]*)\}/)?.[1] ?? "";
  expect(hostRule).not.toContain("padding-bottom");
});

test("the top bar reserves the device's top safe-area inset", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "StackHost.module.css"), "utf8");
  const topBarRule = css.match(/\.topBar \{([^}]*)\}/)?.[1] ?? "";
  expect(topBarRule).toContain("env(safe-area-inset-top)");
});

// The top-bar title channel (2026-07-30-mobile-session-layout-design.md,
// decision 2): StackHost renders whatever PaneScaffold published to the
// chrome store between the back button and the drawer trigger.
test("renders the chrome store's published title in the top bar", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  chromeStore.getState().setPaneTitle("Move Side Open Control");
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  expect(screen.getByTestId("topbar-title").textContent).toBe("Move Side Open Control");
});

test("the top bar title is empty when no pane has published one", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  expect(screen.getByTestId("topbar-title").textContent).toBe("");
});

test("a real session pane's scaffold-published title lands in the top bar", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:ref_titled" });
  await act(async () => {
    render(<StackHost />);
  });
  // The session pane's own PaneScaffold publishes its title (model.name ||
  // ref fallback, Session.tsx) through the channel on mount.
  await vi.waitFor(() => expect(screen.getByTestId("topbar-title").textContent).toBe("local:ref_titled"));
});

// Only "session origin" remains. Its three retired siblings - "exact cycle",
// "mixed cycle" and "missing farther ancestor" - each focused a transcript
// pane whose parentRef also named a session pane, so transcriptOpenOrigin
// could select that exact pane over the fallback's session preference. A
// transcript pane is now only ever a job log (PR 4373), whose ref can never
// also name a session pane, so the origin record cannot be made decisive for
// them; they were retired rather than re-modelled onto a shape the product
// no longer produces.
test.each(["session origin"])(
  "exact Open origin Back: $0 follows the selected edge rather than a same-ref sibling",
  async (shape) => {
    const { recordTranscriptOpenOrigin } = await import("../workspace");
    const workspace = workspaceStore.getState();
    const owner = workspace.openPane("session", { ref: "local:owner" });
    const siblingId = workspace.openPane("session", { ref: "local:b" });
    const aId = openConversation("local:a", "local:b");
    const bId = workspace.openPane("transcript", {
      ref: "local:b",
      parentRef: shape === "missing farther ancestor" ? "local:missing" : "local:a",
    });
    const retained = workspaceStore.getState().panes;
    const a = retained.find((pane) => pane.id === aId)!;
    const b = retained.find((pane) => pane.id === bId)!;
    const sibling = retained.find((pane) => pane.id === siblingId)!;
    recordTranscriptOpenOrigin(a, shape === "session origin" ? sibling : b);
    if (shape !== "mixed cycle") recordTranscriptOpenOrigin(b, a);
    workspace.focusPane(aId);
    render(<StackHost />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Back" }));
    if (shape === "session origin" || shape === "missing farther ancestor") {
      expect(workspaceStore.getState().focusedPaneId).toBe(shape === "session origin" ? siblingId : bId);
      expect(workspaceStore.getState().panes).toEqual(retained);
      await openSessionsPanel(user);
    }
    expect(workspaceStore.getState().panes).toEqual(retained);
    expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  },
);

test("exact Open origin Back: valid local history wins over a recorded origin", async () => {
  const { openTranscript } = await import("../../panes/session/transcript/openTranscript");
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:owner" });
  openTranscript("local:leaf", "local:owner");
  const leaf = workspaceStore.getState().focusedPaneId!;
  const previous = workspace.openPane("doc", { ref: "previous" });
  const retained = workspaceStore.getState().panes;
  render(<StackHost />);
  await screen.findByText(/doc pane: previous/);
  act(() => workspace.focusPane(leaf));
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(previous);
  expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
  expect(workspaceStore.getState().panes).toEqual(retained);
  await user.click(screen.getByRole("button", { name: "Back" }));
  await findStackedEmptyState("welcome");
});

// --- pane-to-pane motion -------------------------------------------------------
// jsdom runs no animation, so these tests stand in for the Slide recipe: an
// element carrying the push entrance, or a leaving layer marked closing,
// reports one running animation whose end the test chooses. Everything else
// is the real stack. Without the stand-in (every other test in this file, and
// a browser under reduced motion) no animation runs and each change is the
// instant swap.
function installSlideAnimations() {
  const finishers: Array<() => void> = [];
  const enterClass = (stackStyles.paneEnter ?? "").split(" ")[0] ?? "";
  const previous = Object.getOwnPropertyDescriptor(Element.prototype, "getAnimations");
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    value(this: Element, options?: { subtree?: boolean }) {
      if (options?.subtree) return [];
      const sliding =
        (enterClass !== "" && this.classList.contains(enterClass)) || this.getAttribute("data-motion") === "closing";
      if (!sliding) return [];
      const finished = new Promise<void>((resolve) => finishers.push(resolve));
      return [{ playState: "running", effect: { getComputedTiming: () => ({ endTime: 240 }) }, finished }];
    },
  });
  return {
    async finish() {
      for (const resolve of finishers.splice(0)) resolve();
      await act(async () => {});
    },
    restore() {
      if (previous) Object.defineProperty(Element.prototype, "getAnimations", previous);
      else Reflect.deleteProperty(Element.prototype, "getAnimations");
    },
  };
}

function stackBody(): HTMLElement {
  const body = document.querySelector("[data-stacked-pane-type]");
  if (!(body instanceof HTMLElement)) throw new Error("no stack body");
  return body;
}

// The frames still on screen that no longer take input: the leaving pane.
function leavingFrames(): HTMLElement[] {
  return [...stackBody().querySelectorAll<HTMLElement>("[inert]")];
}

// A doc fixture's scrollable element, by pane label.
function paneScroller(label: string): HTMLElement {
  const scroller = screen.getByTestId(`doc-scroller-${label}`);
  if (!(scroller instanceof HTMLElement)) throw new Error(`no scroller in doc pane ${label}`);
  return scroller;
}

async function showDoc(ref: string): Promise<void> {
  await act(async () => {
    workspaceStore.getState().openPane("doc", { ref });
  });
  await screen.findByText(new RegExp(`doc pane: ${ref} `));
}

test("a step deeper slides the new pane in over the old frame, which is inert and gone when the slide ends", async () => {
  const slides = installSlideAnimations();
  try {
    const user = userEvent.setup();
    workspaceStore.getState().openPane("doc", { ref: "push_from" });
    render(<StackHost />);
    await screen.findByText(/doc pane: push_from /);

    await showDoc("push_to");
    expect(stackBody().getAttribute("data-stack-transition")).toBe("push");
    const [frame] = leavingFrames();
    expect(leavingFrames()).toHaveLength(1);
    expect(frame?.getAttribute("aria-hidden")).toBe("true");
    expect(frame?.textContent).toContain("push_from");
    // Only the shown pane is alive: the old one unmounted in the same commit.
    expect([...liveDocPanes]).toEqual(["push_to"]);
    // The arriving pane takes input at once.
    const counter = screen.getByRole("button", { name: "clicks: 0" });
    expect(counter.closest("[inert]")).toBeNull();
    await user.click(counter);
    expect(screen.getByRole("button", { name: "clicks: 1" })).toBeTruthy();

    await slides.finish();
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);
    expect(stackBody().textContent).not.toContain("push_from");
  } finally {
    slides.restore();
  }
});

test("Back slides the top frame out to uncover the pane beneath, which is live at once", async () => {
  const slides = installSlideAnimations();
  try {
    workspaceStore.getState().openPane("doc", { ref: "pop_under" });
    render(<StackHost />);
    await screen.findByText(/doc pane: pop_under /);
    await showDoc("pop_top");
    await slides.finish();

    await userEvent.click(screen.getByRole("button", { name: "Back" }));
    await screen.findByText(/doc pane: pop_under /);
    expect(stackBody().getAttribute("data-stack-transition")).toBe("pop");
    const [frame] = leavingFrames();
    expect(leavingFrames()).toHaveLength(1);
    expect(frame?.getAttribute("data-motion")).toBe("closing");
    expect(frame?.textContent).toContain("pop_top");
    expect([...liveDocPanes]).toEqual(["pop_under"]);
    expect(screen.getByRole("button", { name: "clicks: 0" }).closest("[inert]")).toBeNull();

    await slides.finish();
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);
  } finally {
    slides.restore();
  }
});

test("changes that are not a step deeper or back replace the pane without a slide", async () => {
  const slides = installSlideAnimations();
  try {
    const user = userEvent.setup();
    // The first pane does not slide in.
    workspaceStore.getState().openPane("doc", { ref: "replace_a" });
    render(<StackHost />);
    await screen.findByText(/doc pane: replace_a /);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);

    // A pane chosen in the Sessions drawer.
    await user.click(screen.getByRole("button", { name: "Sessions" }));
    await showDoc("replace_b");
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    // Browser history.
    setLastPopstateWasTrustedForTests(true);
    await showDoc("replace_c");
    expect(leavingFrames()).toEqual([]);

    // The shown pane closing under the user.
    await act(async () => {
      const shown = workspaceStore.getState().focusedPaneId;
      const other = workspaceStore.getState().panes.find((pane) => pane.id !== shown && pane.type === "doc");
      if (!shown || !other) throw new Error("fixture lacks a second doc pane");
      workspaceStore.getState().focusPane(other.id);
    });
    await slides.finish();
    await act(async () => {
      const shown = workspaceStore.getState().focusedPaneId;
      if (shown) workspaceStore.getState().closePane(shown);
    });
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);
  } finally {
    slides.restore();
  }
});

test("rapid steps and Backs keep one leaving frame at most and always end on the focused pane", async () => {
  const slides = installSlideAnimations();
  try {
    workspaceStore.getState().openPane("doc", { ref: "rapid_0" });
    render(<StackHost />);
    await screen.findByText(/doc pane: rapid_0 /);
    for (let step = 1; step <= 5; step++) {
      await showDoc(`rapid_${step}`);
      expect(leavingFrames().length).toBeLessThanOrEqual(1);
      await userEvent.click(screen.getByRole("button", { name: "Back" }));
      expect(leavingFrames().length).toBeLessThanOrEqual(1);
    }
    await slides.finish();
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);
    const focused = workspaceStore.getState().panes.find((p) => p.id === workspaceStore.getState().focusedPaneId);
    const shownRef = (focused?.params as { ref?: string } | undefined)?.ref;
    expect(shownRef).toBeDefined();
    expect([...liveDocPanes]).toEqual([shownRef]);
    expect(stackBody().textContent).toContain(`doc pane: ${shownRef} `);
  } finally {
    slides.restore();
  }
});

test("with no slide running, as under reduced motion, a step deeper is the instant swap", async () => {
  workspaceStore.getState().openPane("doc", { ref: "instant_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: instant_a /);
  await showDoc("instant_b");
  expect(leavingFrames()).toEqual([]);
  expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);
  expect(stackBody().textContent).not.toContain("instant_a");
});

function TabletRailActionSlot() {
  return <>{useRailHeaderAction()}</>;
}

function setNeedsYouCount(needsYou: number): void {
  navigationStore.setState({
    mode: "v3",
    manifest: {
      key: { kind: "manifest" },
      data: navigationManifest({
        sections: { live: { count: 0 }, needs_you: { count: needsYou }, pin_sections: { count: 0 } },
      }),
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: '"manifest"',
      loading: false,
      stale: false,
      error: null,
      generationID: "test-generation",
    },
  });
}

test("collapsed tablet Board toggle carries the Needs You badge and count only while hidden with attention waiting", async () => {
  const originalMedia = window.matchMedia;
  window.matchMedia = (query) =>
    ({ matches: query === MOBILE_QUERY || query === TABLET_QUERY, media: query }) as MediaQueryList;
  try {
    const attentionSample = render(<Badge count={3} tone="attention" />);
    const attentionBadgeClass = attentionSample.container.firstElementChild?.className;
    attentionSample.unmount();

    const user = userEvent.setup();
    setNeedsYouCount(3);
    workspaceStore.getState().openPane("doc", { ref: "tablet_badge" });
    render(<StackHost railSlot={<TabletRailActionSlot />} />);
    await screen.findByText(/doc pane: tablet_badge /);

    const expandedToggle = within(screen.getByTestId("board-column")).getByTestId("board-toggle");
    expect(expandedToggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(expandedToggle).queryByText("3")).toBeNull();
    expect(expandedToggle.getAttribute("aria-label")).not.toMatch(/\d/);

    await user.click(expandedToggle);
    expect(screen.queryByTestId("board-column")).toBeNull();
    const collapsedToggle = within(screen.getByTestId("topbar")).getByTestId("board-toggle");
    expect(collapsedToggle.getAttribute("aria-expanded")).toBe("false");
    expect(within(collapsedToggle).getByText("3").className).toBe(attentionBadgeClass);
    expect(within(screen.getByTestId("topbar")).getByRole("button", { name: /\b3\b/ })).toBe(collapsedToggle);

    act(() => setNeedsYouCount(0));
    expect(within(collapsedToggle).queryByText("3")).toBeNull();
    expect(within(collapsedToggle).queryByText("0")).toBeNull();
    expect(collapsedToggle.getAttribute("aria-label")).not.toMatch(/\d/);

    act(() => setNeedsYouCount(2));
    expect(within(collapsedToggle).getByText("2").className).toBe(attentionBadgeClass);
    expect(within(screen.getByTestId("topbar")).getByRole("button", { name: /\b2\b/ })).toBe(collapsedToggle);

    await user.click(collapsedToggle);
    const reopenedToggle = within(screen.getByTestId("board-column")).getByTestId("board-toggle");
    expect(reopenedToggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(reopenedToggle).queryByText("2")).toBeNull();
    expect(reopenedToggle.getAttribute("aria-label")).not.toMatch(/\d/);
  } finally {
    window.matchMedia = originalMedia;
  }
});

test.each(["phone", "tablet to phone"])("a slide discards the leaving scroller after %s layout", async (layout) => {
  const slides = installSlideAnimations();
  const originalMedia = window.matchMedia;
  const tablet = new EventTarget();
  const tabletMedia = Object.assign(tablet, { matches: true, media: TABLET_QUERY });
  if (layout === "tablet to phone") {
    window.matchMedia = (query) =>
      (query === TABLET_QUERY ? tabletMedia : { matches: query === MOBILE_QUERY, media: query }) as MediaQueryList;
  }
  try {
    const user = userEvent.setup();
    workspaceStore.getState().openPane("doc", { ref: "scrolled_from" });
    render(<StackHost railSlot={<TabletRailActionSlot />} />);
    await screen.findByText(/doc pane: scrolled_from /);
    const offsets = scrollOffsetsForTests();
    if (!offsets) throw new Error("StackHost did not expose its scroll offsets");
    const scroller = paneScroller("scrolled_from");

    scroller.scrollTop = 48;
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(offsets.get(scroller)).toEqual([48, 0]);

    if (layout === "tablet to phone") {
      expect(screen.getByTestId("board-column")).toBeTruthy();
      await user.click(screen.getByTestId("board-toggle"));
      expect(screen.queryByTestId("board-column")).toBeNull();
      expect(paneScroller("scrolled_from")).toBe(scroller);
      expect(scroller.scrollTop).toBe(48);
      expect(offsets.get(scroller)).toEqual([48, 0]);

      await user.click(screen.getByTestId("board-toggle"));
      expect(screen.getByTestId("board-column")).toBeTruthy();
      expect(paneScroller("scrolled_from")).toBe(scroller);
      expect(scroller.scrollTop).toBe(48);
      expect(offsets.get(scroller)).toEqual([48, 0]);

      await act(async () => {
        tabletMedia.matches = false;
        tabletMedia.dispatchEvent(Object.assign(new Event("change"), { matches: false }));
      });
      expect(screen.queryByTestId("board-column")).toBeNull();
      expect(paneScroller("scrolled_from")).toBe(scroller);
      expect(scroller.scrollTop).toBe(48);
      expect(offsets.get(scroller)).toEqual([48, 0]);
    }

    await showDoc("scrolled_to");
    expect(stackBody().getAttribute("data-stack-transition")).toBe("push");
    // Restoring the leaving frame's offsets queues scroll events from inside
    // that frame while it is still connected - the events a browser fires
    // during the slide. They must not put the discarded pane back in the map.
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });

    await slides.finish();
    expect(scroller.isConnected).toBe(false);
    expect(offsets.has(scroller)).toBe(false);

    // The shown pane's own scroller still records.
    const arrived = paneScroller("scrolled_to");
    arrived.scrollTop = 24;
    await act(async () => {
      arrived.dispatchEvent(new Event("scroll"));
    });
    expect(offsets.get(arrived)).toEqual([24, 0]);
    expect(offsets.has(scroller)).toBe(false);
  } finally {
    slides.restore();
    window.matchMedia = originalMedia;
  }
});

test("picking from the open tablet board replaces without sliding, while non-board steps still push", async () => {
  const slides = installSlideAnimations();
  const restoreViewport = installMobileViewport([MOBILE_QUERY, TABLET_QUERY]);
  try {
    const user = userEvent.setup();
    workspaceStore.getState().openPane("session", { ref: "local:board_a" });
    render(
      <StackHost
        railSlot={
          <div>
            <TabletRailActionSlot />
            <button type="button" onClick={() => openSessionByRef("local:board_b")}>
              Board session B
            </button>
            <div
              role="treeitem"
              aria-selected={false}
              tabIndex={0}
              onKeyDown={(event) => {
                if (event.key === "Enter") openSessionByRef("local:board_c");
              }}
            >
              Board session C
            </div>
          </div>
        }
      />,
    );
    await screen.findByRole("heading", { name: "local:board_a" });
    const board = screen.getByTestId("board-column");

    // Pointer pick from the open tablet board replaces without a push slide.
    await user.click(within(board).getByRole("button", { name: "Board session B" }));
    await screen.findByRole("heading", { name: "local:board_b" });
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);

    // Keyboard Enter inside the board replaces without a push slide too.
    const itemC = within(board).getByRole("treeitem", { name: "Board session C" });
    itemC.focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("heading", { name: "local:board_c" });
    expect(leavingFrames()).toEqual([]);
    expect(stackBody().hasAttribute("data-stack-transition")).toBe(false);

    // A non-board forward navigation after a board pick still pushes.
    await showDoc("from_pane");
    expect(stackBody().getAttribute("data-stack-transition")).toBe("push");
    expect(leavingFrames()).toHaveLength(1);
    await slides.finish();

    // Board input followed by input inside the pane column still pushes.
    itemC.focus();
    await user.keyboard("{ArrowDown}");
    await user.click(screen.getByRole("button", { name: "clicks: 0" }));
    await showDoc("after_pane_input");
    expect(stackBody().getAttribute("data-stack-transition")).toBe("push");
    await slides.finish();

    // Collapsing the board from inside board-column clears board input so the next push runs.
    await user.click(within(board).getByTestId("board-toggle"));
    expect(screen.queryByTestId("board-column")).toBeNull();
    await showDoc("after_collapse");
    expect(stackBody().getAttribute("data-stack-transition")).toBe("push");
    await slides.finish();
  } finally {
    slides.restore();
    restoreViewport();
  }
});

// --- floating top bar ----------------------------------------------------

function registerScrollablePaneForTests(id: "session" | "sessionZoom" | "transcript" | "doc") {
  return registerPaneForTests({
    id,
    component: lazy(() =>
      Promise.resolve({
        default: () => (
          <div data-reading-scroll-host="" style={{ height: "100%" }}>
            <div data-testid="pane-scroll" style={{ height: "100%", overflowY: "auto" }}>
              <div style={{ height: 2400 }} />
            </div>
          </div>
        ),
      }),
    ),
    title: () => id,
  });
}

test.each([
  { paneType: "session" as const, params: { ref: "local:a" }, collapses: true },
  {
    paneType: "sessionZoom" as const,
    params: { ref: "local:leaf", source: { type: "session", params: { ref: "local:a" } }, edges: [] },
    collapses: true,
  },
  { paneType: "transcript" as const, params: { ref: "local:job" }, collapses: true },
  { paneType: "doc" as const, params: { ref: "ref_doc" }, collapses: false },
])(
  "scrolling a $paneType pane collapses=$collapses the top bar and scrolling up reveals it",
  async ({ paneType, params, collapses }) => {
    window.history.replaceState({}, "", "/");
    const restorePane = registerScrollablePaneForTests(paneType);
    try {
      const host = await act(async () => render(<StackHost routeDeferred />));
      await act(async () => {
        workspaceStore.getState().openPane(paneType, params);
      });
      const bar = screen.getByTestId("topbar");
      expect(bar.getAttribute("data-collapsed")).not.toBe("true");

      const scroller = await screen.findByTestId("pane-scroll");
      act(() => {
        scroller.scrollTop = 200;
        scroller.dispatchEvent(new Event("scroll"));
      });
      if (collapses) {
        await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
        act(() => {
          scroller.scrollTop = 10;
          scroller.dispatchEvent(new Event("scroll"));
        });
        await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
        if (paneType === "sessionZoom") {
          fireEvent.scroll(scroller, { target: { scrollTop: 200 } });
          expect(bar.getAttribute("data-collapsed")).toBe("true");
          act(() => topNotesStore.getState().openAndFocus(params.ref));
          expect(bar.getAttribute("data-collapsed")).not.toBe("true");
        }
      } else {
        expect(bar.getAttribute("data-collapsed")).not.toBe("true");
      }
      host.unmount();
    } finally {
      restorePane();
    }
  },
);

test("floating top bar compensates scroll, ignores content height shifts, and resets on focus change", async () => {
  window.history.replaceState({}, "", "/");
  const restoreSession = registerScrollablePaneForTests("session");
  try {
    const host = await act(async () => render(<StackHost routeDeferred />));
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:comp" });
    });
    const bar = screen.getByTestId("topbar");
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 56 });

    const scroller = await screen.findByTestId("pane-scroll");
    let currentHeight = 2400;
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, get: () => currentHeight });

    // Initial height observation (0 -> 2400) is position-neutral so initial
    // scroll-to-bottom on open does not collapse the bar.
    act(() => {
      scroller.scrollTop = 200;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");

    // Downward gesture past barHeight + 12 collapses and compensates by -56.
    act(() => {
      scroller.scrollTop = 260;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(scroller.scrollTop).toBe(204);

    // Content prepend/append shifts scrollHeight: position-neutral, stays collapsed.
    currentHeight = 2800;
    act(() => {
      scroller.scrollTop = 604;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).toBe("true");

    // Upward step >= 8px reveals and compensates by +56.
    act(() => {
      scroller.scrollTop = 594;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(scroller.scrollTop).toBe(650);

    // Collapse again, then switch focus to another pane: solid bar returns.
    act(() => {
      scroller.scrollTop = 720;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));

    await act(async () => {
      workspaceStore.getState().openPane("doc", { ref: "after_float" });
    });
    await screen.findByText(/doc pane: after_float /);
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    host.unmount();
  } finally {
    restoreSession();
  }
});

test("mobile notes tuck in the reading direction and return on reversal or explicit open", async () => {
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  window.history.replaceState({}, "", "/");
  const restoreViewport = installMobileViewport();
  const ref = "remote:mobile-notes";
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const result = activityDetailsThread(ref, {
      turns: [{ id: "t", status: "completed", itemsView: "full", items: [] }],
    });
    result.thread.evener.capabilities.sharedNotes = true;
    return result;
  });
  connectionStore.getState().connect(client);
  workspaceStore.getState().openPane("session", { ref });
  try {
    const host = render(
      <ClientProvider client={client}>
        <StackHost />
      </ClientProvider>,
    );
    const notes = await screen.findByTestId("top-notes-panel");
    Object.defineProperty(notes, "offsetHeight", { configurable: true, value: 44 });
    const bar = screen.getByTestId("topbar");
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 56 });
    const scroller = document.querySelector("[data-reading-scroll-host] > *");
    if (!scroller) throw new Error("missing transcript scroller");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 2400 });
    const scroll = (top: number) =>
      act(() => {
        scroller.scrollTop = top;
        scroller.dispatchEvent(new Event("scroll"));
      });

    // The initial end landing can move again with the same measured height.
    // Neither automatic offset establishes the reader's direction.
    scroll(1800);
    scroll(2400);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    fireEvent.touchStart(scroller, { touches: [{ clientY: 400 }] });
    fireEvent.touchMove(scroller, { touches: [{ clientY: 420 }] });
    scroll(2340);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    // A composited scroll can rebound after the viewport grows under the
    // finger. The finger is still moving in the original direction.
    scroll(2310);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    fireEvent.touchMove(scroller, { touches: [{ clientY: 400 }] });
    scroll(2320);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    fireEvent.touchEnd(scroller);
    fireEvent.wheel(scroller, { deltaY: -2320 });
    scroll(0);

    // Hiding the notes summary and floating the title preserves the reading line.
    scroll(260);
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    expect(scroller.scrollTop).toBe(160);

    // Another session's notes expanding leaves this session's floating bar alone.
    act(() => {
      topNotesStore.getState().setExpanded("local:other", true);
    });
    expect(bar.getAttribute("data-collapsed")).toBe("true");
    expect(scroller.scrollTop).toBe(160);

    // A small reversal is neutral. Continuing upward restores Notes and navigation.
    scroll(156);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    scroll(140);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    expect(scroller.scrollTop).toBe(140);
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    scroll(124);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();

    // Resuming the original direction tucks the summary. Explicit opening restores it.
    scroll(140);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    act(() => {
      topNotesStore.getState().setExpanded(ref, true);
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    expect(scroller.scrollTop).toBe(240);

    const editor = within(notes).getByRole<HTMLTextAreaElement>("textbox");
    await userEvent.type(editor, "draft");
    scroll(340);
    expect(document.activeElement).toBe(editor);
    expect(editor.value).toBe("draft");
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    expect(scroller.scrollTop).toBe(340);

    // Collapsing the notes leaves the bar solid until the next scroll decides.
    act(() => {
      topNotesStore.getState().setExpanded(ref, false);
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");

    scroll(400);
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(scroller.scrollTop).toBe(300);
    scroll(5);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    expect(scroller.scrollTop).toBe(5);

    // Opening at the latest turn can start the first reading gesture upward.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 3600 });
    scroll(2500);
    scroll(2460);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    expect(scroller.scrollTop).toBe(2360);
    // Two small reverse steps still count when streaming moves the offset
    // between them. The content shift itself contributes no travel.
    scroll(2364);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 3900 });
    scroll(2720);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    scroll(scroller.scrollTop + 4);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    expect(bar.getAttribute("data-collapsed")).toBe("true");
    scroll(0);
    expect(scroller.scrollTop).toBe(0);
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    act(() =>
      client.emitNotification({ method: "evener/sandbox/escalation/requested", params: testEscalation({ ref }) }),
    );
    const card = await screen.findByRole("group", { name: "Sandbox approval" });
    scroll(260);
    expect(notes.closest("[data-reading-overlay]")).toBeNull();
    client.on("evener/sandbox/escalation/resolve", () => ({}));
    await userEvent.click(within(card).getByRole("button", { name: "Deny" }));
    await waitFor(() => expect(card.isConnected).toBe(false));
    scroll(340);
    expect(notes.closest("[data-reading-overlay]")).not.toBeNull();
    scroll(0);

    // A short history can have room for the summary alone. Keep its title
    // above the reader throughout the reversal so the viewport stays fixed.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 600 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 500 });
    scroll(100);
    scroll(80);
    expect(notes.closest("[data-notes-hidden]")).not.toBeNull();
    expect(scroller.scrollTop).toBe(36);
    scroll(46);
    expect(notes.closest("[data-notes-hidden]")).toBeNull();
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    expect(scroller.scrollTop).toBe(46);
    host.unmount();
  } finally {
    topNotesStore.getState().resetForTests();
    restoreViewport();
    vi.unstubAllGlobals();
  }
});

// On 120Hz displays a slow upward drag moves fewer than 8px per frame. Upward
// travel accumulates across consecutive frames until reaching 8px, and resets
// whenever downward scrolling interrupts the run.
test("floating top bar accumulates small upward scroll steps and resets on downward movement", async () => {
  window.history.replaceState({}, "", "/");
  const restoreSession = registerScrollablePaneForTests("session");
  try {
    const host = await act(async () => render(<StackHost routeDeferred />));
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:slow-drag" });
    });
    const bar = screen.getByTestId("topbar");
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 56 });
    const scroller = await screen.findByTestId("pane-scroll");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 2400 });

    const scrollTo = (top: number) => {
      act(() => {
        scroller.scrollTop = top;
        scroller.dispatchEvent(new Event("scroll"));
      });
    };

    // Initial height observation, then collapse downward (260 -> 204).
    scrollTo(200);
    scrollTo(260);
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(scroller.scrollTop).toBe(204);

    // Two 3px upward steps (6px total) stay below the 8px reveal threshold.
    scrollTo(201);
    expect(bar.getAttribute("data-collapsed")).toBe("true");
    scrollTo(198);
    expect(bar.getAttribute("data-collapsed")).toBe("true");

    // Reversing downward resets the upward run start before crossing 8px.
    scrollTo(200);
    expect(bar.getAttribute("data-collapsed")).toBe("true");

    // Two more 3px upward steps from 200 (6px) still stay collapsed even though
    // the net distance from 204 is 10px.
    scrollTo(197);
    expect(bar.getAttribute("data-collapsed")).toBe("true");
    scrollTo(194);
    expect(bar.getAttribute("data-collapsed")).toBe("true");

    // A third 3px upward step crosses 8px (200 -> 191 = 9px), restoring the bar
    // and compensating scroll by +56 (191 -> 247).
    scrollTo(191);
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(scroller.scrollTop).toBe(247);
    host.unmount();
  } finally {
    restoreSession();
  }
});

// Tablet persistent board column animates from the left edge symmetrically with
// the desktop Overview sidebar: inert and non-interactive while exiting, one
// visible toggle at all times, and clipped horizontally so no scrollbar appears.
test("tablet board column slides out inert on collapse and slides in from the left on reopen", async () => {
  const css = readModuleCss(import.meta.url, "StackHost.module.css");
  const splitRule = css.match(/\.split\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(splitRule).toContain("position: relative");
  expect(splitRule).toContain("overflow-x: clip");
  const closingRule = css.match(/\.boardColumn\[data-motion="closing"\]\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(closingRule).toContain("position: absolute");
  expect(closingRule).toContain("pointer-events: none");
  expect(closingRule).toContain("z-index: var(--z-raised)");

  const restoreViewport = installTabletMatchMedia();
  try {
    const user = userEvent.setup();
    workspaceStore.getState().openPane("doc", { ref: "tablet_slide" });
    render(
      <MotionProvider>
        <StackHost railSlot={<TabletRailActionSlot />} />
      </MotionProvider>,
    );
    await screen.findByText(/doc pane: tablet_slide /);

    const initialBoard = screen.getByTestId("board-column");
    expect(initialBoard.hasAttribute("inert")).toBe(false);
    expect(initialBoard.getAttribute("data-motion")).not.toBe("closing");

    const expandedToggle = within(initialBoard).getByTestId("board-toggle");
    expandedToggle.focus();
    await user.keyboard("{Enter}");

    // Under MotionProvider the exiting column stays mounted for the Slide exit,
    // immediately marked inert and closing while the single live toggle lives in
    // the top bar with restored focus.
    const exitingBoard = screen.getByTestId("board-column");
    expect(exitingBoard.hasAttribute("inert")).toBe(true);
    expect(exitingBoard.getAttribute("aria-hidden")).toBe("true");
    expect(exitingBoard.getAttribute("data-motion")).toBe("closing");
    expect(screen.getAllByTestId("board-toggle")).toHaveLength(1);
    const collapsedToggle = within(screen.getByTestId("topbar")).getByTestId("board-toggle");
    expect(document.activeElement).toBe(collapsedToggle);

    // Once the exit animation finishes, the column unmounts cleanly.
    await waitFor(() => expect(screen.queryByTestId("board-column")).toBeNull());

    // Reopening mounts the board sliding in from the left edge, live and
    // interactive again, with the single toggle back inside the board.
    await user.keyboard("{Enter}");
    const reopenedBoard = screen.getByTestId("board-column");
    expect(reopenedBoard.hasAttribute("inert")).toBe(false);
    expect(reopenedBoard.getAttribute("aria-hidden")).toBeNull();
    expect(reopenedBoard.getAttribute("data-motion")).not.toBe("closing");
    expect(reopenedBoard.getAttribute("style") ?? "").toContain("-100%");
    expect(screen.getAllByTestId("board-toggle")).toHaveLength(1);
    const reopenedToggle = within(reopenedBoard).getByTestId("board-toggle");
    expect(document.activeElement).toBe(reopenedToggle);
    await waitFor(() => {
      expect(reopenedBoard.getAttribute("style") ?? "").not.toContain("-100%");
    });
  } finally {
    cleanup();
    restoreViewport();
  }
});

test("tablet board column unmounts and reopens immediately without sliding under prefers-reduced-motion", async () => {
  // motion/react emits a one-time dev warning when reduced motion is active.
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const restoreViewport = installTabletMatchMedia();
  setReducedMotionPreference(true);
  try {
    const user = userEvent.setup();
    workspaceStore.getState().openPane("doc", { ref: "tablet_reduced" });
    render(
      <MotionProvider>
        <StackHost railSlot={<TabletRailActionSlot />} />
      </MotionProvider>,
    );
    await screen.findByText(/doc pane: tablet_reduced /);

    const expandedToggle = within(screen.getByTestId("board-column")).getByTestId("board-toggle");
    expandedToggle.focus();
    await user.keyboard("{Enter}");

    // Under reduced motion the board skips exit animation and unmounts at once.
    expect(screen.queryByTestId("board-column")).toBeNull();
    expect(document.activeElement).toBe(within(screen.getByTestId("topbar")).getByTestId("board-toggle"));

    await user.keyboard("{Enter}");
    const reopenedBoard = screen.getByTestId("board-column");
    expect(reopenedBoard.getAttribute("style") ?? "").not.toContain("-100%");
    expect(warn.mock.calls.every(([msg]) => typeof msg === "string" && msg.includes("reduced-motion-disabled"))).toBe(
      true,
    );
  } finally {
    cleanup();
    warn.mockRestore();
    restoreViewport();
  }
});

test("browser top bar capsules use --radius-pane, omit empty clusters, and never transform the header", () => {
  const css = readModuleCss(import.meta.url, "StackHost.module.css");
  const collapsed = css.match(/\.topBarCollapsed\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(collapsed).toContain("position: absolute");
  expect(collapsed).toContain("pointer-events: none");
  expect(collapsed).not.toMatch(/\btransform\s*:/);
  const capsules =
    css.match(
      /\.topBarCollapsed \.leading:not\(:empty\),\s*\.topBarCollapsed \.trailing:not\(:empty\)\s*\{([^}]*)\}/,
    )?.[1] ?? "";
  expect(capsules).toContain("border-radius: var(--radius-pill)");
  expect(capsules).toContain("pointer-events: auto");
});

test("floated Overview opens outside the collapsed header and Escape restores focus to the header Overview button", async () => {
  const restoreViewport = installMobileViewport();
  const restoreSession = registerScrollablePaneForTests("session");
  const client = activityClient();
  connectionStore.getState().connect(client);
  const user = userEvent.setup();
  try {
    await act(async () =>
      render(
        <ClientProvider client={client}>
          <MotionProvider>
            <StackHost routeDeferred />
          </MotionProvider>
        </ClientProvider>,
      ),
    );
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:floated-overview" });
    });
    const bar = screen.getByTestId("topbar");
    const scroller = await screen.findByTestId("pane-scroll");
    act(() => {
      scroller.scrollTop = 200;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));

    const overviewButton = within(bar).getByRole("button", { name: "Overview" });
    await user.click(overviewButton);

    const dialog = await screen.findByRole("dialog", { name: "Overview" });
    // Rendered in StackHost's full-viewport activityOverlay sibling, never
    // inside the collapsed header's blur-capsule containing block or paneColumn.
    expect(document.body.contains(dialog)).toBe(true);
    expect(bar.contains(dialog)).toBe(false);
    expect(bar.parentElement?.contains(dialog)).toBe(false);

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Overview" })).toBeNull());
    expect(document.activeElement).toBe(overviewButton);
    expect(bar.getAttribute("data-collapsed")).toBe("true");
  } finally {
    cleanup();
    restoreSession();
    restoreViewport();
  }
});

test("resizing across the tablet breakpoint keeps the mounted scroll body active for header collapse and saved offsets in both directions", async () => {
  const originalMedia = window.matchMedia;
  const originalWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
  const tabletMedia = Object.assign(new EventTarget(), {
    matches: true,
    media: TABLET_QUERY,
    onchange: null,
    addListener() {},
    removeListener() {},
  });
  const setViewport = async (width: number, tablet: boolean) => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
    await act(async () => {
      tabletMedia.matches = tablet;
      tabletMedia.dispatchEvent(Object.assign(new Event("change"), { matches: tablet }));
      window.dispatchEvent(new Event("resize"));
    });
  };
  window.matchMedia = ((query: string) =>
    query === TABLET_QUERY
      ? tabletMedia
      : Object.assign(new EventTarget(), {
          matches: query === MOBILE_QUERY,
          media: query,
          onchange: null,
          addListener() {},
          removeListener() {},
        })) as typeof window.matchMedia;
  const restoreSession = registerScrollablePaneForTests("session");
  try {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
    const host = await act(async () =>
      render(<StackHost routeDeferred railSlot={<div data-testid="board-scroll" />} />),
    );
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:tablet-scroll" });
    });
    const bar = screen.getByTestId("topbar");
    const body = stackBody();
    const scroller = await screen.findByTestId("pane-scroll");
    const offsets = scrollOffsetsForTests();
    if (!offsets) throw new Error("StackHost did not expose its scroll offsets");

    // 800px tablet: board scroll never collapses the header or writes reader offsets.
    expect(screen.getByTestId("board-column")).toBeTruthy();
    const boardScroller = screen.getByTestId("board-scroll");
    act(() => {
      boardScroller.scrollTop = 300;
      boardScroller.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    expect(offsets.has(boardScroller)).toBe(false);

    // 800px tablet: scrolling the open session collapses the header and records offset.
    act(() => {
      scroller.scrollTop = 200;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(offsets.get(scroller)).toEqual([200, 0]);

    // 800px tablet -> 400px phone: same mounted body & scroller keep responding.
    await setViewport(400, false);
    expect(screen.queryByTestId("board-column")).toBeNull();
    expect(stackBody()).toBe(body);
    expect(screen.getByTestId("pane-scroll")).toBe(scroller);
    expect(scroller.scrollTop).toBe(200);
    expect(offsets.get(scroller)).toEqual([200, 0]);

    act(() => {
      scroller.scrollTop = 10;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(offsets.get(scroller)).toEqual([10, 0]);

    act(() => {
      scroller.scrollTop = 240;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(offsets.get(scroller)).toEqual([240, 0]);

    // 400px phone -> 800px tablet: same mounted body & scroller keep responding.
    await setViewport(800, true);
    expect(screen.getByTestId("board-column")).toBeTruthy();
    expect(stackBody()).toBe(body);
    expect(screen.getByTestId("pane-scroll")).toBe(scroller);
    expect(scroller.scrollTop).toBe(240);
    expect(offsets.get(scroller)).toEqual([240, 0]);

    act(() => {
      scroller.scrollTop = 10;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(offsets.get(scroller)).toEqual([10, 0]);

    act(() => {
      scroller.scrollTop = 180;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(offsets.get(scroller)).toEqual([180, 0]);

    host.unmount();
  } finally {
    restoreSession();
    window.matchMedia = originalMedia;
    if (originalWidth) Object.defineProperty(window, "innerWidth", originalWidth);
  }
});

test("scrolling a nested list inside a session pane leaves the top bar and nested scroll position untouched", async () => {
  window.history.replaceState({}, "", "/");
  const restoreSession = registerPaneForTests({
    id: "session",
    component: lazy(() =>
      Promise.resolve({
        default: () => (
          <div style={{ height: "100%" }}>
            <div data-reading-scroll-host="" style={{ height: "100%" }}>
              <div data-testid="pane-scroll" style={{ height: "100%", overflowY: "auto" }}>
                <div style={{ height: 2400 }}>
                  <div data-testid="nested-rail" style={{ maxHeight: 220, overflowY: "auto" }}>
                    <div style={{ height: 800 }} />
                  </div>
                </div>
              </div>
            </div>
            <div data-composer="">
              <div data-testid="nested-menu" style={{ maxHeight: 220, overflowY: "auto" }}>
                <div style={{ height: 800 }} />
              </div>
            </div>
          </div>
        ),
      }),
    ),
    title: () => "session",
  });
  try {
    const host = await act(async () => render(<StackHost routeDeferred />));
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:nested" });
    });
    const bar = screen.getByTestId("topbar");
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 56 });
    const reader = await screen.findByTestId("pane-scroll");
    const nestedMenu = screen.getByTestId("nested-menu");
    const nestedRail = screen.getByTestId("nested-rail");

    // Scrolling a nested composer menu or inline rail past barHeight + 12
    // must never float the bar or rewrite the nested list's scrollTop.
    act(() => {
      nestedMenu.scrollTop = 140;
      nestedMenu.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    expect(nestedMenu.scrollTop).toBe(140);

    act(() => {
      nestedRail.scrollTop = 150;
      nestedRail.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    expect(nestedRail.scrollTop).toBe(150);

    // Scrolling the primary reading scroller still collapses and compensates.
    act(() => {
      reader.scrollTop = 260;
      reader.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(reader.scrollTop).toBe(204);

    // While collapsed, scrolling the nested menu upward must keep the bar
    // collapsed and leave both scroll positions untouched.
    act(() => {
      nestedMenu.scrollTop = 120;
      nestedMenu.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).toBe("true");
    expect(nestedMenu.scrollTop).toBe(120);
    expect(reader.scrollTop).toBe(204);

    // An upward gesture on the primary reading scroller still reveals the bar.
    act(() => {
      reader.scrollTop = 194;
      reader.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(reader.scrollTop).toBe(250);
    host.unmount();
  } finally {
    restoreSession();
  }
});

test("near-bottom collapse compensates from the pre-flip scroll offset instead of post-layout clamped scrollTop", async () => {
  window.history.replaceState({}, "", "/");
  const restoreSession = registerScrollablePaneForTests("session");
  try {
    const host = await act(async () => render(<StackHost routeDeferred />));
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:near-bottom" });
    });
    const bar = screen.getByTestId("topbar");
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 56 });
    const scroller = await screen.findByTestId("pane-scroll");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 1000 });

    // Simulate browser viewport growth on float: solid bar maxScroll is 500,
    // collapsed bar grows the viewport by 56px so maxScroll clamps to 444.
    let rawScrollTop = 0;
    const maxScroll = () => (bar.getAttribute("data-collapsed") === "true" ? 444 : 500);
    Object.defineProperty(scroller, "scrollTop", {
      configurable: true,
      get: () => Math.min(rawScrollTop, maxScroll()),
      set: (value: number) => {
        rawScrollTop = Math.max(0, Math.min(value, maxScroll()));
      },
    });

    // Initial height observation (0 -> 1000) is position-neutral.
    act(() => {
      scroller.scrollTop = 100;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");

    // Scroll within one barHeight of the bottom (490 of 500): on float the
    // browser clamps scrollTop to 444 before useLayoutEffect runs, so
    // compensation must compute 490 - 56 = 434 rather than 444 - 56 = 388.
    act(() => {
      scroller.scrollTop = 490;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    expect(scroller.scrollTop).toBe(434);

    // Scrolling up 14px reveals and compensates from 420 -> 420 + 56 = 476.
    act(() => {
      scroller.scrollTop = 420;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(scroller.scrollTop).toBe(476);
    host.unmount();
  } finally {
    restoreSession();
  }
});

test("floating top bar collapse and reveal keep compensated TranscriptBody scrollTop without reflow undo", async () => {
  window.history.replaceState({}, "", "/");
  let topBar: HTMLElement | null = null;
  let extraHeightOffset = 0;
  const currentViewportHeight = () =>
    (topBar?.getAttribute("data-collapsed") === "true" ? 556 : 500) + extraHeightOffset;
  const geometry = installTranscriptGeometry(() => ({
    width: 390,
    viewportHeight: currentViewportHeight(),
    rowHeights: [1200, 0],
  }));
  const frames = holdReaderFrames();
  const model = {
    ...makeTranscriptPreviewModel(),
    ref: "local:floating-bar-reflow",
    turns: [
      {
        id: "turn-1",
        status: "completed" as const,
        items: [
          {
            id: "entry-1",
            turnId: "turn-1",
            type: "userMessage" as const,
            text: "hello",
            status: "completed" as const,
          },
        ],
      },
    ],
  };
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const SessionPane = () => {
    const listRef = useRef<VirtualListHandle>(null);
    const flow = useTranscriptScroll({
      ref: model.ref,
      viewId: "floating-bar-reflow-view",
      model,
      listRef,
      loadOlder: async () => {},
      renderedRowCount: 2,
    });
    return (
      <TranscriptBody
        model={model}
        config={config}
        surface="live"
        disclosureScope="live:floating-bar-reflow"
        sessionRef={model.ref}
        viewId="floating-bar-reflow-view"
        followingBottom={flow.isFollowingBottom}
        listRef={listRef}
        onMeasurementsChange={flow.restoreViewAnchorAfterMeasurement}
      />
    );
  };
  const restoreSession = registerPaneForTests({
    id: "session",
    component: lazy(() => Promise.resolve({ default: SessionPane })),
    title: () => "session",
  });
  try {
    const host = await act(async () => render(<StackHost routeDeferred />));
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: model.ref });
    });
    const bar = screen.getByTestId("topbar");
    topBar = bar;
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 56 });
    const list = await screen.findByTestId("transcript-virtual-list");
    const scroller = list.firstElementChild as HTMLElement;
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(700);
    // Deliver the native scroll event for the initial end landing (jsdom does not
    // dispatch scroll events on programmatic scrollTop writes) and flush the
    // virtualizer's one-frame mount reconciliation before scrolling away.
    act(() => {
      scroller.dispatchEvent(new Event("scroll"));
    });
    await act(async () => frames.release());

    // The usable viewport can shrink after the first measured tail lands.
    // Following the extended tail is still automatic positioning.
    extraHeightOffset = -40;
    act(() => {
      scroller.scrollTop = 740;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    extraHeightOffset = 0;
    act(() => {
      scroller.scrollTop = 700;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(scroller.closest("[data-notes-hidden]")).toBeNull();

    // A native scrollbar drag away from the settled end can deliver only
    // scroll events. Its first movement still establishes the reading direction.
    act(() => {
      scroller.scrollTop = 300;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(300);
    expect(scroller.closest("[data-notes-hidden]")).not.toBeNull();
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");

    // Downward scroll collapses the bar: viewportHeight grows by 56 on commit
    // and StackHost shifts scrollTop by -56 (350 -> 294). TranscriptBody's
    // viewport reflow must keep 294.
    act(() => {
      scroller.scrollTop = 350;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(294);

    // Scroll to the bottom while collapsed (1200 - 556 = 644), then scroll up 15px (629):
    // StackHost reveals the bar (viewportHeight shrinks 556 -> 500 on commit) and
    // compensates +56 (629 -> 685). TranscriptBody's viewport reflow must keep 685
    // (15px away from 700), never undoing to 629 or slamming back to the bottom.
    act(() => {
      scroller.scrollTop = 644;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(644);

    act(() => {
      scroller.scrollTop = 629;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(685);
    expect(bar.getAttribute("data-collapsed")).not.toBe("true");

    // Control: scrolling back to the bottom collapses the bar to 644 (1200 - 556);
    // an uncompensated viewport shrink (such as the keyboard opening) while
    // following the bottom still re-pins the reader to the new end (1200 - 456 = 744).
    act(() => {
      scroller.scrollTop = 700;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(644);
    extraHeightOffset = -100;
    await act(async () => geometry.notify());
    expect(scroller.scrollTop).toBe(744);

    host.unmount();
  } finally {
    restoreSession();
    geometry.restore();
    frames.restore();
  }
});

test("floating top bar ignores bottom and top elastic overscroll rebounds", async () => {
  window.history.replaceState({}, "", "/");
  const restoreSession = registerScrollablePaneForTests("session");
  try {
    const host = await act(async () => render(<StackHost routeDeferred />));
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:overscroll" });
    });
    const bar = screen.getByTestId("topbar");
    Object.defineProperty(bar, "offsetHeight", { configurable: true, value: 61 });
    const scroller = await screen.findByTestId("pane-scroll");
    const viewportHeight = () => (bar.getAttribute("data-collapsed") === "true" ? 784 : 723);
    const maxScroll = () => 1227 - viewportHeight();
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 1227 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, get: viewportHeight });

    let liveScrollTop = 0;
    const programmaticWrites: number[] = [];
    Object.defineProperty(scroller, "scrollTop", {
      configurable: true,
      get: () => liveScrollTop,
      set: (value: number) => {
        const clamped = Math.max(0, Math.min(value, maxScroll()));
        programmaticWrites.push(clamped);
        liveScrollTop = clamped;
      },
    });
    const nativeScroll = (top: number) => {
      act(() => {
        liveScrollTop = top;
        scroller.dispatchEvent(new Event("scroll"));
      });
    };

    // Initial height baseline at top, then top elastic rubber-band and rebound stay solid.
    nativeScroll(0);
    for (const top of [-40, -70, -30, 0]) {
      nativeScroll(top);
      expect(bar.getAttribute("data-collapsed")).not.toBe("true");
    }

    // Scroll down to the bottom so the bar floats and settles at maxScroll (443).
    nativeScroll(200);
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).toBe("true"));
    nativeScroll(443);
    expect(bar.getAttribute("data-collapsed")).toBe("true");
    programmaticWrites.length = 0;

    // Bottom elastic overscroll past maxScroll and its rebound must not flip the
    // bar or issue compensation writes.
    for (const top of [483, 533, 513, 483, 458, 443]) {
      nativeScroll(top);
      expect(bar.getAttribute("data-collapsed")).toBe("true");
    }
    expect(programmaticWrites).toEqual([]);

    // A genuine upward scroll inside [0, maxScroll] still reveals the bar and
    // compensates scrollTop by +barHeight (430 -> 491).
    nativeScroll(430);
    await waitFor(() => expect(bar.getAttribute("data-collapsed")).not.toBe("true"));
    expect(scroller.scrollTop).toBe(491);
    host.unmount();
  } finally {
    restoreSession();
  }
});
