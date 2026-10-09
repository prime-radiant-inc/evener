import type { ThreadModel } from "@evener/appwire-client";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import * as paneActions from "../../../../shell/paneActions";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { DocOpeningProvider } from "../../../doc/docOpeningContext";
import "../../../doc";
import "../../index";
import { AgentMarkdown } from "./AgentMarkdown";

// The link walk runs over Markdown's sanitized DOM. No real markdown source
// can put an anchor inside a diagram (mermaid's SVG forbids anchors at both
// sanitize layers), so this test mounts the walker against a hand-built root:
// the mocked Markdown widget stands in for that DOM, letting one anchor sit in
// prose and the identical href sit inside a [data-mermaid-diagram] subtree.
// This tests the walker's exclusion, not mermaid rendering.
const markdownHtml = vi.hoisted(() => ({ current: "" }));

vi.mock("../../../../widgets/markdown", async () => {
  const { forwardRef } = await import("react");
  return {
    Markdown: forwardRef<HTMLDivElement, Record<string, unknown>>(function MockMarkdown(_props, ref) {
      // biome-ignore lint/security/noDangerouslySetInnerHtml: test-only mock standing in for the Markdown widget's sanitized DOM - production sanitization is justified at the widgets/markdown call sites.
      return <div ref={ref} dangerouslySetInnerHTML={{ __html: markdownHtml.current }} />;
    }),
  };
});

const thread: ThreadModel = {
  ref: "local:034MXwo6BpPH0QQCgdICSf",
  threadId: "034MXwo6BpPH0QQCgdICSf",
  name: "Diagram links",
  status: { type: "idle" },
  modelProvider: "",
  model: "",
  visionModel: "",
  askPending: false,
  pendingEscalations: [],
  turns: [],
  queue: null,
  tasks: null,
  jobsUpdatedAt: null,
  jobsTreeRevision: null,
  lastFrameAt: 0,
  capabilities: {
    send: false,
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
    rename: false,
    sharedNotes: false,
  },
  goal: null,
  humanNote: "",
  agentNote: "",
  sessionUrls: [],
  contextUsed: 0,
  contextWindow: 0,
  contextPressure: 0,
  usage: null,
  workMillis: 0,
  reasoningEffortLevels: [],
  supportsReasoning: false,
  cwd: "/workspace/project",
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  markdownHtml.current = "";
});

test("a file link inside a diagram gets no Open beside portal, while the same href in prose does", () => {
  markdownHtml.current =
    '<p><a href="docs/report.md">prose</a></p>' +
    '<div data-mermaid-diagram=""><a href="docs/report.md">diagram</a></div>';
  render(
    <TranscriptRenderProvider thread={thread}>
      <AgentMarkdown source="x" />
    </TranscriptRenderProvider>,
  );

  // Prose link: rewritten to the session-scoped doc URL and given one portal.
  const prose = screen.getByRole("link", { name: "prose" });
  expect(new URL(prose.getAttribute("href") ?? "", "https://hub.example").pathname).toBe("/doc/file");
  expect(screen.getAllByRole("button", { name: "Open beside: docs/report.md" })).toHaveLength(1);

  // Diagram link: untouched, no portal.
  const diagram = screen.getByRole("link", { name: "diagram" });
  expect(diagram.getAttribute("href")).toBe("docs/report.md");
  expect(diagram.closest("[data-mermaid-diagram]")).not.toBeNull();
});

// The prose-link producer captures the opening origin exactly as the file-card
// producer does, and the diagram exclusion is unchanged while it does. The
// provider is the pending source seam: until it lands, importing it fails this
// file's collection (a setup failure, reported as such, not a behavior RED).
test("a prose file link under a browser-origin provider captures the origin and still leaves diagram links alone", () => {
  markdownHtml.current =
    '<p><a href="docs/report.md">prose</a></p>' +
    '<div data-mermaid-diagram=""><a href="docs/report.md">diagram</a></div>';
  const origin = { kind: "browser-origin" as const, paneId: "owner-pane", compositionOwnerId: "owner-A" };
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  render(
    <TranscriptRenderProvider thread={thread}>
      <DocOpeningProvider origin={origin}>
        <AgentMarkdown source="x" />
      </DocOpeningProvider>
    </TranscriptRenderProvider>,
  );
  // Exactly one portal - the prose link - even with the provider present.
  expect(screen.getAllByRole("button", { name: "Open beside: docs/report.md" })).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Open beside: docs/report.md" }));
  expect(spy).toHaveBeenCalledWith({
    type: "doc",
    params: { session: thread.ref, path: "docs/report.md", kind: "file", origin },
  });
  const diagram = screen.getByRole("link", { name: "diagram" });
  expect(diagram.getAttribute("href")).toBe("docs/report.md");
  spy.mockRestore();
});
