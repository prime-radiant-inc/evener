// k7-footeroverflow: throwaway browser-verification harness for kata vybn
// (session footer overflow). NOT part of the shipped app - never imported by
// App.tsx, never built into dist, never committed. Deleted before this kata
// closes. Seeds a ThreadModel via the same FakeClient the vitest suite uses
// (protocol/testing/fakeClient), then renders SessionChrome inside a div
// whose width is driven by a <input type="range"> - a real, resizable BOX,
// not the window - so this reproduces the pane-narrowing trigger the kata
// describes, not a viewport media query.
import { useEffect } from "react";
import { createRoot } from "react-dom/client";
import { SessionChrome } from "../panes/session/chrome/SessionChrome";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import "../styles/tokens.css";
import "../styles/global.css";
import { connectionStore } from "../stores/connection";
import { threadsStore } from "../stores/threads";

const params = new URLSearchParams(window.location.search);
const theme = params.get("theme");
if (theme === "light" || theme === "dark") {
  document.documentElement.dataset.theme = theme;
}

const CAPABILITIES: ThreadCapabilities = {
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

// Realistic, on-the-longer-side content - a long provider/model pair, a
// live work clock, a real cost chip, context usage, and a queue depth - so
// the harness reproduces the row's actual worst-case width, not an
// unrealistically short model chip that would never wrap in the first place.
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
    serf: {
      ref,
      capabilities: CAPABILITIES,
      queue: {},
      cost: "~$0.42",
    },
    ...overrides,
  };
}

const REF = "k7harness";
const fake = new FakeClient("ready");
fake.on("thread/read", () => ({ thread: testThread(REF) }) satisfies ThreadReadResponse);
fake.on("serf/tasks/list", () => ({ data: [] }));
connectionStore.getState().connect(fake);

const root = document.getElementById("root");
if (!root) throw new Error("k7harness.html is missing #root");

function Harness() {
  return (
    <div style={{ padding: 24, fontFamily: "sans-serif" }}>
      <div style={{ marginBottom: 16, display: "flex", gap: 12, alignItems: "center" }}>
        <label htmlFor="k7-width">pane width</label>
        <input
          id="k7-width"
          type="range"
          min={200}
          max={1000}
          defaultValue={700}
          onChange={(e) => {
            const pane = document.getElementById("k7-pane");
            const label = document.getElementById("k7-width-label");
            if (pane) pane.style.width = `${e.target.value}px`;
            if (label) label.textContent = `${e.target.value}px`;
          }}
        />
        <span id="k7-width-label">700px</span>
      </div>
      <div id="k7-pane" style={{ width: 700, border: "1px dashed var(--edge)", background: "var(--surface-1)" }}>
        <PaneContent />
      </div>
    </div>
  );
}

function PaneContent() {
  useEffect(() => {
    void threadsStore.getState().ensureThread(REF);
  }, []);
  return <SessionChrome ref={REF} />;
}

createRoot(root).render(<Harness />);
