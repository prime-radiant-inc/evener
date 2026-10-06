// Automatic memory refreshes as the daemon actually sends them
// (agent/testdata/memorycontextwire), through the real projection path:
// hydrateThread -> projectThread -> TurnBlock -> the memory renderer. The
// producer fixture is the source of truth for the wire shape; these tests feed
// it through the shared adapter and assert the production behavior the spec
// requires.
import {
  hydrateThread,
  makeTranscriptDisplayConfig,
  type ProjectedTurn,
  projectThread,
  type Thread,
  type ThreadItem,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import {
  type MemoryContextWireCase,
  memoryContextWireCases,
  memoryContextWireItem,
} from "@evener/appwire-client/testing/memoryContextWireFixtures";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { TurnBlock } from "../TurnBlock";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const PRESETS = ["chat", "intent", "tools", "activity", "full"] as const;

function preset(level: (typeof PRESETS)[number]): TranscriptDisplayConfigV1 {
  return makeTranscriptDisplayConfig({ kind: "preset", level }, { systemEvents: true });
}

function project(items: ThreadItem[], config: TranscriptDisplayConfigV1): ProjectedTurn {
  const thread = {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 0,
    updatedAt: 0,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    turns: [{ id: "turn_1", itemsView: "full", status: "completed", items }],
    evener: { ref: "ref-1", queue: { revision: 0 } },
  } as unknown as Thread;
  const model = hydrateThread({ thread }, "ref-1", 0);
  const turn = projectThread(model, config).turns[0];
  if (!turn) throw new Error("no projected turn");
  return turn;
}

function renderItem(item: ThreadItem, config: TranscriptDisplayConfigV1) {
  return render(
    <TranscriptRenderProvider config={config} surface="readOnly" disclosureScope="mem:wire" sessionRef="session-1">
      <TurnBlock turn={project([item], config)} sessionRef="session-1" />
    </TranscriptRenderProvider>,
  );
}

function open(): void {
  const details = screen.getByTestId("memory-context-item") as HTMLDetailsElement;
  const summary = details.querySelector("summary");
  if (!summary) throw new Error("no summary");
  fireEvent.click(summary);
}

// The one case whose raw the producer deliberately corrupts, so the renderer
// must fall back to the complete original Text.
const MALFORMED: MemoryContextWireCase = "malformed-project";

// --- every case, every preset: compact heading, closed, no raw in the row ----

test.each(PRESETS)("at the %s preset every recorded refresh is a closed compact heading", (level) => {
  const config = preset(level);
  for (const name of memoryContextWireCases()) {
    cleanup();
    resetDisclosureStoreForTests();
    renderItem(memoryContextWireItem(name), config);
    const details = screen.getByTestId("memory-context-item") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(screen.getByTestId("memory-context-label").textContent).toBe("Refreshed my memory");
    // The collapsed row never shows the model-facing envelope.
    const summary = details.querySelector("summary");
    expect(summary?.textContent ?? "").not.toContain("Quoted index data:");
    expect(summary?.textContent ?? "").not.toContain("Memory scope");
  }
});

// --- opening a real recorded refresh reveals decoded, formatted content -------

test("a current refresh opens to its scope/state and decoded index, with the exact Text in Source", () => {
  const item = memoryContextWireItem("current-personal");
  renderItem(item, preset("tools"));
  open();
  expect(screen.getByTestId("memory-context-meta").textContent).toContain("Personal memory · current");
  expect(screen.getByTestId("memory-context-content").textContent).toContain("a note");
  expect(screen.getByTestId("memory-context-source-text").textContent).toBe(item.text);
});

test("project and session scopes name themselves truthfully", () => {
  for (const [name, expected] of [
    ["current-project", "Project memory · current"],
    ["current-session", "Session memory · current"],
  ] as const) {
    cleanup();
    resetDisclosureStoreForTests();
    renderItem(memoryContextWireItem(name), preset("tools"));
    open();
    expect(screen.getByTestId("memory-context-meta").textContent).toContain(expected);
  }
});

test("an empty current index opens to the empty-index marker, not an error", () => {
  renderItem(memoryContextWireItem("empty-project"), preset("tools"));
  open();
  expect(screen.getByTestId("memory-context-empty").textContent).toBe("Empty index");
  expect(screen.queryByTestId("memory-context-content")).toBeNull();
});

test("missing, revoked and unavailable stay distinguishable", () => {
  for (const [name, state] of [
    ["missing-project", "missing"],
    ["revoked-project", "revoked"],
    ["unavailable-project", "unavailable"],
  ] as const) {
    cleanup();
    resetDisclosureStoreForTests();
    renderItem(memoryContextWireItem(name), preset("tools"));
    // Unavailable/revoked are on the collapsed row; missing is not (it is not
    // an implied successful read, but it is also not a broken access).
    if (state === "revoked" || state === "unavailable") {
      expect(screen.getByTestId("memory-context-state").textContent).toBe(state);
    }
    open();
    expect(screen.getByTestId("memory-context-scope-state").textContent).toContain(state);
  }
});

test("a truncated refresh says truncated and never implies the missing remainder", () => {
  const item = memoryContextWireItem("truncated-project");
  renderItem(item, preset("tools"));
  open();
  expect(screen.getByTestId("memory-context-truncated").textContent).toBe("truncated");
  expect(screen.getByTestId("memory-context-source-text").textContent).toBe(item.text);
});

test("quoted Unicode and tab content survives into the formatted body and the literal Source", () => {
  const item = memoryContextWireItem("quoted-project");
  renderItem(item, preset("tools"));
  open();
  expect(screen.getByTestId("memory-context-content").textContent).toContain("café");
  expect(screen.getByTestId("memory-context-source-text").textContent).toBe(item.text);
});

test("a delegate's read-only session suffix is preserved in Source", () => {
  const item = memoryContextWireItem("suffixed-session");
  renderItem(item, preset("tools"));
  open();
  const source = screen.getByTestId("memory-context-source-text").textContent ?? "";
  expect(source).toContain("Session memory belongs to your root session: you can read it, not write it.");
});

test("the malformed envelope keeps the exact Text on open, with no manufactured index", () => {
  const item = memoryContextWireItem(MALFORMED);
  expect(item.raw).toBeFalsy();
  renderItem(item, preset("tools"));
  expect(screen.getByTestId("memory-context-label").textContent).toBe("Refreshed my memory");
  open();
  expect(screen.getByTestId("memory-context-fallback").textContent).toBe(item.text);
  expect(screen.queryByTestId("memory-context-content")).toBeNull();
  expect(screen.queryByTestId("memory-context-meta")).toBeNull();
});

test("Full does not auto-open a producer-backed refresh", () => {
  renderItem(memoryContextWireItem("current-project"), preset("full"));
  expect((screen.getByTestId("memory-context-item") as HTMLDetailsElement).open).toBe(false);
});
