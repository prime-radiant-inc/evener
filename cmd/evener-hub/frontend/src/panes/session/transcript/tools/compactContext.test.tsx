// @vitest-environment jsdom

// compact_context descriptor tests. Ground truth: agent/session_tools_compact.go
// (the tool's arguments and its printed sentences), housekeepingSteps.ts (the
// shared summary words web, native and run lines all use), and the wire fixture
// call_compact_context in agent/testdata/toolwire/calls.json (the note-clearing
// call). The spec of record is
// docs/superpowers/specs/2026-10-08-compact-context-renderer-design.md.

import { type ItemModel, makeTranscriptDisplayConfig, type TurnModel } from "@evener/appwire-client";
import { toolWireStep } from "@evener/appwire-client/testing/toolWireFixtures";
import { render, screen, within } from "@testing-library/react";
import { expect, test } from "vitest";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { ToolCallItem } from "../ToolCallItem";
import { toolRendererFor } from "../toolRenderers";
import "./compactContext";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test("the descriptor keeps the shared step words and pins the fold icon without folding runs", () => {
  const d = toolRendererFor("compact_context");
  const call = item({
    toolName: "compact_context",
    argumentsJSON: JSON.stringify({ note_to_self: "Next: run the race detector." }),
  });
  expect(d.summary(call)).toBe("Asked for a context compaction");
  expect(d.icon).toBe("fold");
  expect(d.fold).toBeUndefined();
});

test("a call that only clears its note keeps the shared cleared-note words", () => {
  const d = toolRendererFor("compact_context");
  expect(d.summary(toolWireStep("call_compact_context"))).toBe("Cleared its compaction note");
});

test("the body renders the note and instructions as labeled markdown, the skills as a mono list, and the output as plain text", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({
          note_to_self: "Session `local:034a`.\n\n## Next steps\n\n- run the race detector",
          compaction_instructions: "Preserve exact current state and next actions from note.",
          reload_skills: ["test-driven-development", "systematic-debugging"],
        }),
        output:
          "Note pinned. A compaction will run at the seam, honoring your instructions; your note will be handed back to you right after.",
      })}
      live={false}
    />,
  );
  expect(screen.getByText("Note to self")).toBeTruthy();
  expect(screen.getByText("Compaction instructions")).toBeTruthy();
  expect(screen.getByText("Skills to reload")).toBeTruthy();
  // The note is markdown, not literal text: a heading renders as a heading.
  expect(screen.getByRole("heading", { name: "Next steps" })).toBeTruthy();
  expect(screen.getByText("run the race detector")).toBeTruthy();
  expect(screen.getByText("Preserve exact current state and next actions from note.")).toBeTruthy();
  expect(screen.getByText("test-driven-development, systematic-debugging")).toBeTruthy();
  expect(
    screen.getByText(
      "Note pinned. A compaction will run at the seam, honoring your instructions; your note will be handed back to you right after.",
    ),
  ).toBeTruthy();
});

test("an explicit empty reload_skills renders None, the tool's own semantics for an empty array", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "n", reload_skills: [] }),
      })}
      live={false}
    />,
  );
  expect(screen.getByText("Skills to reload")).toBeTruthy();
  expect(screen.getByText("None.")).toBeTruthy();
});

test("an absent or null reload_skills renders no skills block", () => {
  const Body = toolRendererFor("compact_context").body!;
  const { container } = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "n", reload_skills: null }),
      })}
      live={false}
    />,
  );
  expect(within(container).queryByText("Skills to reload")).toBeNull();
});

test("an intent argument matching the row's stated intent is not rendered again in the body", () => {
  const Body = toolRendererFor("compact_context").body!;
  const intent = "Freeing context at the evidence-intake boundary.";
  const { container } = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ intent, note_to_self: "n" }),
        description: intent,
      })}
      live={false}
    />,
  );
  expect(within(container).queryByText("Intent")).toBeNull();
});

test("an intent argument with no stated intent on the row renders under an Intent label", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ intent: "Freeing context before the diagnosis run." }),
      })}
      live={false}
    />,
  );
  expect(screen.getByText("Intent")).toBeTruthy();
  expect(screen.getByText("Freeing context before the diagnosis run.")).toBeTruthy();
});

test("an unexpected argument renders in the additional-arguments block, not dropped", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "n", mystery: "x" }),
      })}
      live={false}
    />,
  );
  const extra = screen.getByLabelText("Additional arguments");
  expect(extra.textContent).toContain("mystery");
});

test("a known key with a wrong-typed value lands in the additional-arguments block instead of vanishing", () => {
  const Body = toolRendererFor("compact_context").body!;
  const { container } = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: 42, compaction_instructions: "keep" }),
      })}
      live={false}
    />,
  );
  expect(within(container).queryByText("Note to self")).toBeNull();
  const extra = screen.getByLabelText("Additional arguments");
  expect(extra.textContent).toContain("42");
});

test("a live call renders the request fields before any output exists", () => {
  const Body = toolRendererFor("compact_context").body!;
  const { container } = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "Keep the delegate id dlg_123." }),
      })}
      live
    />,
  );
  expect(screen.getByText("Note to self")).toBeTruthy();
  expect(screen.getByText("Keep the delegate id dlg_123.")).toBeTruthy();
  expect(within(container).queryByText(/Note pinned/)).toBeNull();
});

test("the note-clearing fixture's body shows the tool's own sentence and no note block", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(<Body item={toolWireStep("call_compact_context")} live={false} />);
  expect(screen.getByText("Note cleared. No compaction requested.")).toBeTruthy();
  expect(screen.queryByText("Note to self")).toBeNull();
});

test("renders through the real tool row: summary, stated intent, and the labeled body", () => {
  const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };
  const call = item({
    id: "item_cc",
    toolName: "compact_context",
    argumentsJSON: JSON.stringify({
      note_to_self: "Ship the renderer.",
      compaction_instructions: "Keep the spec's field order.",
    }),
    description: "Freeing context between tasks.",
    output:
      "Note pinned. A compaction will run at the seam, honoring your instructions; your note will be handed back to you right after.",
  });
  render(
    <TranscriptRenderProvider
      config={makeTranscriptDisplayConfig({ kind: "preset", level: "full" })}
      surface="readOnly"
      disclosureScope="test:cc"
    >
      <ToolCallItem item={call} turn={turn} live={false} />
    </TranscriptRenderProvider>,
  );
  const row = screen.getByTestId("tool-call-item");
  expect(row.getAttribute("data-tool-name")).toBe("compact_context");
  expect(within(row).getByTestId("tool-row-summary").textContent).toBe("Asked for a context compaction");
  const body = within(row).getByTestId("tool-call-body");
  expect(within(body).getByText("Note to self")).toBeTruthy();
  expect(within(body).getByText("Ship the renderer.")).toBeTruthy();
  expect(within(body).getByText("Compaction instructions")).toBeTruthy();
});
