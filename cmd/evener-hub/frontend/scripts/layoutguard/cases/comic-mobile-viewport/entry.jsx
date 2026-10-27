// Geometry fixture, not network E2E: real components with static store data,
// as in src/dev/surface-sections/rail.tsx. No client or RPC responses are mocked.
import React from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { AppwireClient, hydrateThread } from "@evener/appwire-client";
import { ComicConversationBody } from "/src/comic/ComicConversationBody";
import { createComposerSourceState } from "/src/panes/session/composer/sourceState";
import shellStyles from "/src/shell/AppShell.module.css";
import { ClientProvider } from "/src/shell/clientContext";
import { useKeyboardInset } from "/src/shell/useKeyboardInset";
import { threadsStore } from "/src/stores/threads";
import { PaneScaffold } from "/src/widgets";

// Headless Chrome cannot open Safari's software keyboard. Replace only that
// browser API boundary; use the real shell hook and CSS below it.
const viewport = Object.assign(new EventTarget(), { height: 620, offsetTop: 0, scale: 1 });
const nativeViewport = window.visualViewport;
let simulatedViewport = false;
function KeyboardInset() {
  useKeyboardInset();
  return null;
}

const root = createRoot(document.getElementById("root"), {
  onUncaughtError: (error) => {
    window.comicGeometryError = error.stack;
  },
});
// Intentionally never connected: this case exercises layout, not server reads.
const client = new AppwireClient({ url: `ws://${location.host}/unused-geometry` });
const ref = "local:comic-geometry";
const source = "COMIC_GEOMETRY_SOURCE_0123456789 ".repeat(100);
const model = hydrateThread(
  {
    thread: {
      id: "comic-geometry",
      sessionId: "comic-geometry",
      preview: "",
      ephemeral: false,
      modelProvider: "",
      createdAt: 1,
      updatedAt: 1,
      status: { type: "idle" },
      cwd: "/geometry",
      cliVersion: "",
      source: "evener",
      evener: {
        ref,
        capabilities: {
          send: true,
          steer: false,
          interrupt: false,
          compact: false,
          clear: false,
          forkFromTurn: false,
          shutdown: false,
          changeModel: false,
          changeVisionModel: false,
          queue: false,
          goal: false,
          sharedNotes: false,
          rename: false,
        },
        queue: { revision: 0 },
      },
      turns: [
        {
          id: "geometry-turn",
          status: "completed",
          itemsView: "full",
          items: [{ id: "geometry-item", type: "agentMessage", text: source }],
        },
      ],
    },
  },
  ref,
  0,
);
threadsStore.setState({ threads: new Map([[ref, model]]) });

const seat = {
  readView: null,
  composerSource: createComposerSourceState(ref, "comic-geometry-pane"),
  keepViewRemount: () => true,
  viewFallbackId: "comic-geometry-pane",
  focused: true,
  keyboardScrollPaneId: null,
  sessionMenu: true,
};

function render() {
  flushSync(() =>
    root.render(
      <ClientProvider client={client}>
        <KeyboardInset key={String(simulatedViewport)} />
        <div className={shellStyles.shell} data-geometry-shell>
          <div style={{ flex: "none", height: 24 }} data-geometry-host-chrome>Pane host</div>
          <div className={shellStyles.content}>
            <div className={shellStyles.workspaceColumn} data-geometry-pane>
              <ComicConversationBody ref={ref} seat={seat} render={({ content, footer, cadence }) => (
                <PaneScaffold paneId={seat.viewFallbackId} title="Comic geometry" scaffoldMarker={`session:${ref}`}
                  focused bodyScroll={false} cadence={cadence} footer={footer}>
                  {content}
                </PaneScaffold>
              )} />
            </div>
          </div>
        </div>
      </ClientProvider>,
    ),
  );
}

window.comicGeometry = {
  render,
  source,
  running(active) {
    flushSync(() => threadsStore.setState({
      threads: new Map([[ref, {
        ...model,
        status: { type: active ? "active" : "idle" },
        capabilities: { ...model.capabilities, interrupt: active },
      }]]),
    }));
  },
  simulateViewport() {
    Object.defineProperty(window, "visualViewport", { configurable: true, value: viewport });
    simulatedViewport = true;
    render("session");
  },
  restoreViewport() {
    Object.defineProperty(window, "visualViewport", { configurable: true, value: nativeViewport });
    simulatedViewport = false;
    render("session");
  },
  viewport(event, values) {
    Object.assign(viewport, values);
    viewport.dispatchEvent(new Event(event));
  },
};
window.measure = () => ({ ready: true });
render();
