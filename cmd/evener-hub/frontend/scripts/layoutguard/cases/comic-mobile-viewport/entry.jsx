// Geometry fixture, not network E2E: real components with static store data,
// as in src/dev/surface-sections/rail.tsx. No client or RPC responses are mocked.
import React from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { AppwireClient, hydrateThread } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { ComicPicker } from "/src/comic/ComicPicker";
import { ComicSession } from "/src/comic/ComicSession";
import styles from "/src/comic/comic.module.css";
import { ClientProvider } from "/src/shell/clientContext";
import { useKeyboardInset } from "/src/shell/useKeyboardInset";
import { navigationStore } from "/src/stores/navigation/store";
import { threadsStore } from "/src/stores/threads";

// Headless Chrome cannot open Safari's software keyboard. Replace only that
// browser API boundary; use the real hook and CSS below it. ComicApp.test.tsx
// separately proves that the actual app mounts and releases the hook.
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

function render(kind, count = 0) {
  if (kind === "picker") {
    const key = { kind: "section", section: "live", offset: 0, limit: 50 };
    navigationStore.setState({
      mode: "v3",
      visibleResourceKeys: new Set([keyID(key)]),
      resources: new Map([
        [
          keyID(key),
          {
            key,
            data: {
              generation_id: "geometry",
              revision: 1,
              remaining: count ? 1 : 0,
              truncated: !!count,
              sessions: Array.from({ length: count }, (_, i) => ({
                ref: `local:geometry-${i}`,
                title: `GEOMETRY_ROW_${i}`,
                children: [],
              })),
            },
            loadedRevision: 1,
            targetRevision: 1,
            forceToken: 0,
            etag: "geometry",
            loading: false,
            stale: false,
            error: null,
            generationID: "geometry",
          },
        ],
      ]),
    });
  }
  flushSync(() =>
    root.render(
      <ClientProvider client={client}>
        <KeyboardInset key={String(simulatedViewport)} />
        <main key={kind} className={styles.comicRoot} data-theme="light" data-comic-route={kind}>
          {kind === "picker" ? (
            <ComicPicker
              onSelect={(selected) => {
                window.selectedComicRef = selected;
              }}
            />
          ) : (
            <ComicSession sessionRef={ref} onReplaceRef={() => false} onSelectRef={() => {}} />
          )}
        </main>
      </ClientProvider>,
    ),
  );
}

window.comicGeometry = {
  render,
  source,
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
render("picker");
