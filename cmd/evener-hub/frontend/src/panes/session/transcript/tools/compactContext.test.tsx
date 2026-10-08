// @vitest-environment jsdom

// compact_context descriptor tests. Ground truth: agent/session_tools_compact.go
// (the tool's arguments and its printed sentences), housekeepingSteps.ts (the
// shared summary words web, native and run lines all use), the wire fixture
// call_compact_context in agent/testdata/toolwire/calls.json (the note-clearing
// call), and ToolRow.tsx's statedIntentOf (the row's own stated-intent rule:
// blank means absent). The spec of record is
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

// precedes pins DOM order: the spec numbers the body's fields (note,
// instructions, skills, intent), so the labels must appear in that order.
function precedes(a: HTMLElement, b: HTMLElement): boolean {
  return (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
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

test("the body renders the fields in the spec's order, and the tool's output as plain text", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({
          note_to_self: "Session `local:034a`.\n\n## Next steps\n\n- run the race detector",
          compaction_instructions: "Preserve exact current state and next actions from note.",
          reload_skills: ["test-driven-development", "systematic-debugging"],
          intent: "Freeing context before the diagnosis run.",
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
  expect(screen.getByText("Intent")).toBeTruthy();
  // The note is markdown, not literal text: a heading renders as a heading.
  expect(screen.getByRole("heading", { name: "Next steps" })).toBeTruthy();
  expect(screen.getByText("run the race detector")).toBeTruthy();
  expect(screen.getByText("Preserve exact current state and next actions from note.")).toBeTruthy();
  expect(screen.getByText("test-driven-development, systematic-debugging")).toBeTruthy();
  const outputLine = screen.getByText(
    "Note pinned. A compaction will run at the seam, honoring your instructions; your note will be handed back to you right after.",
  );
  expect(outputLine).toBeTruthy();
  // Plain text, not a code block: the spec's one field that must not render
  // as machine evidence (a CodeBlock would wrap it in pre).
  expect(outputLine.closest("pre, code")).toBeNull();
  // The spec's numbered order: note, then instructions, then skills, then intent.
  expect(precedes(screen.getByText("Note to self"), screen.getByText("Compaction instructions"))).toBe(true);
  expect(precedes(screen.getByText("Compaction instructions"), screen.getByText("Skills to reload"))).toBe(true);
  expect(precedes(screen.getByText("Skills to reload"), screen.getByText("Intent"))).toBe(true);
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
  const nulled = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "n", reload_skills: null }),
      })}
      live={false}
    />,
  );
  expect(within(nulled.container).queryByText("Skills to reload")).toBeNull();
  const absent = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "n" }),
      })}
      live={false}
    />,
  );
  expect(within(absent.container).queryByText("Skills to reload")).toBeNull();
});

test("an intent argument matching the row's stated intent is not rendered again in the body, at label or value level", () => {
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
  expect(within(container).queryByText(intent)).toBeNull();
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

test("an intent argument that differs from the row's stated intent renders under an Intent label", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ intent: "Freeing context before the diagnosis run." }),
        description: "Some other stated reason.",
      })}
      live={false}
    />,
  );
  expect(screen.getByText("Intent")).toBeTruthy();
  expect(screen.getByText("Freeing context before the diagnosis run.")).toBeTruthy();
});

test("a whitespace-only description counts as no stated intent, matching the row's own rule", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ intent: "Freeing context before the diagnosis run." }),
        description: "   ",
      })}
      live={false}
    />,
  );
  expect(screen.getByText("Intent")).toBeTruthy();
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

test("null-valued arguments land in the additional-arguments block too, not dropped", () => {
  const Body = toolRendererFor("compact_context").body!;
  const { container } = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ compaction_instructions: "keep", note_to_self: null, mystery: null }),
      })}
      live={false}
    />,
  );
  expect(within(container).queryByText("Note to self")).toBeNull();
  const extra = screen.getByLabelText("Additional arguments");
  expect(extra.textContent).toContain("note_to_self");
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

test("malformed or non-object argumentsJSON renders the raw text the generic body would show, and keeps the row expandable", () => {
  const d = toolRendererFor("compact_context");
  const Body = d.body!;
  const malformed = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: "{not json",
      })}
      live={false}
    />,
  );
  const malformedArgs = within(malformed.container).getByLabelText("Tool call arguments");
  expect(malformedArgs.textContent).toContain("{not json");
  expect(d.hasBody?.(item({ argumentsJSON: "{not json" }))).toBe(true);
  const nonObject = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: '"just text"',
      })}
      live={false}
    />,
  );
  expect(within(nonObject.container).getByLabelText("Tool call arguments").textContent).toContain('"just text"');
  expect(d.hasBody?.(item({ argumentsJSON: '"just text"' }))).toBe(true);
});

test("an intent argument whose trimmed value matches the row's stated intent stays suppressed despite surrounding whitespace", () => {
  const Body = toolRendererFor("compact_context").body!;
  const { container } = render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ intent: "  Freeing context at the boundary.  " }),
        description: "Freeing context at the boundary.",
      })}
      live={false}
    />,
  );
  expect(within(container).queryByText("Intent")).toBeNull();
  expect(within(container).queryByText("Freeing context at the boundary.")).toBeNull();
});

test("a whitespace-only note renders its block: the tool pins it rather than clearing it", () => {
  const Body = toolRendererFor("compact_context").body!;
  render(
    <Body
      item={item({
        toolName: "compact_context",
        argumentsJSON: JSON.stringify({ note_to_self: "   " }),
      })}
      live={false}
    />,
  );
  expect(screen.getByText("Note to self")).toBeTruthy();
});

test("hasBody is false exactly when the body would render nothing", () => {
  const d = toolRendererFor("compact_context");
  // A live note-clearing call: no fields, no leftovers, no output yet.
  expect(d.hasBody?.(item({ argumentsJSON: JSON.stringify({ note_to_self: "" }) }))).toBe(false);
  // Argless and blank: nothing at all.
  expect(d.hasBody?.(item({}))).toBe(false);
  // A note block, an output sentence, and leftovers each earn the body.
  expect(d.hasBody?.(item({ argumentsJSON: JSON.stringify({ note_to_self: "n" }) }))).toBe(true);
  expect(
    d.hasBody?.(
      item({
        argumentsJSON: JSON.stringify({ note_to_self: "" }),
        output: "Note cleared. No compaction requested.",
      }),
    ),
  ).toBe(true);
  expect(d.hasBody?.(item({ argumentsJSON: JSON.stringify({ note_to_self: "", mystery: "x" }) }))).toBe(true);
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
  const statedIntent = "Freeing context between tasks.";
  const call = item({
    id: "item_cc",
    toolName: "compact_context",
    argumentsJSON: JSON.stringify({
      intent: statedIntent,
      note_to_self: "Ship the renderer.",
      compaction_instructions: "Keep the spec's field order.",
    }),
    description: statedIntent,
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
  // The stated intent rides the row's own intent line; the body must not
  // render it again under an Intent label.
  expect(within(row).getByTestId("tool-row-intent").textContent).toBe(statedIntent);
  const body = within(row).getByTestId("tool-call-body");
  expect(within(body).queryByText("Intent")).toBeNull();
  expect(within(body).getByText("Note to self")).toBeTruthy();
  expect(within(body).getByText("Ship the renderer.")).toBeTruthy();
  expect(within(body).getByText("Compaction instructions")).toBeTruthy();
});
