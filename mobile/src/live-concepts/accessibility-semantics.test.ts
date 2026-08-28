import { describe, expect, it } from "vitest";
import {
  connectionStatusAxLabel,
  mutationAxLabel,
  rosterAxLabel,
  rosterRowAxLabel,
  transcriptItemAxLabel,
  usageAxLabel,
  workItemAxLabel,
} from "./accessibility-semantics";
import type { LiveComposerView } from "./contract";
import type {
  LiveConnectionView,
  LiveRosterRow,
  LiveRosterView,
  LiveTranscriptItem,
  LiveWorkItem,
} from "./model";

const transcriptBase: Omit<LiveTranscriptItem, "kind"> = {
  key: "internal-item-key",
  label: "read_file",
  body: "private transcript body",
  tone: "running",
  streaming: false,
  truncated: false,
  questionKey: null,
  sequenceLabel: "internal-sequence",
};

const composerBase: LiveComposerView = {
  draft: "",
  canSend: true,
  canSteer: true,
  canQueue: true,
  canInterrupt: true,
  pending: null,
  accepted: null,
  error: null,
};

describe("accessible live concept semantics", () => {
  it("names connection evidence with display-safe identity only", () => {
    const connection: LiveConnectionView = {
      status: "connected",
      evidence: {
        serverName: "Office Hub",
        serverVersion: "2.0.0",
        protocolVersion: "evener-appwire-v3",
        appVersion: "0.1.0",
      },
    };

    expect(connectionStatusAxLabel(connection)).toBe(
      "Connected to Office Hub 2.0.0; protocol evener-appwire-v3; app version 0.1.0",
    );
    expect(connectionStatusAxLabel({ status: "reconnecting" })).toBe(
      "Reconnecting",
    );
  });

  it("names roster state and actions without opaque keys or projects", () => {
    const row: LiveRosterRow = {
      key: "opaque-session-ref",
      title: "Fix login flow",
      project: "private/project/path",
      summary: "Visible summary",
      updatedLabel: "now",
      tone: "attention",
      connectedWorkCount: 1,
    };
    const roster: LiveRosterView = {
      status: "ready",
      query: "",
      groups: [{ id: "needsYou", label: "Needs you", rows: [row] }],
      hasMore: false,
      error: null,
    };

    expect(rosterAxLabel("stillwater", roster)).toBe(
      "Stillwater sessions; 1 sessions; complete list",
    );
    expect(rosterRowAxLabel(row)).toBe("Open Fix login flow; status attention");
  });

  it.each([
    ["user", "Your message; completed"],
    ["assistant", "Assistant response; completed"],
    ["question", "Question; completed"],
    ["failure", "Error; completed"],
    ["attachment", "Attachment; completed"],
  ] as const)("names %s transcript items by role only", (kind, expected) => {
    const label = transcriptItemAxLabel({ ...transcriptBase, kind });
    expect(label).toBe(expected);
    expect(label).not.toContain(transcriptBase.key);
    expect(label).not.toContain(transcriptBase.body);
  });

  it("distinguishes tool and reasoning disclosures without exposing content", () => {
    expect(transcriptItemAxLabel({ ...transcriptBase, kind: "tool" })).toBe(
      "Tool read_file; completed",
    );
    expect(
      transcriptItemAxLabel({
        ...transcriptBase,
        kind: "tool",
        label: "Reasoning",
        streaming: true,
      }),
    ).toBe("Reasoning; streaming");
  });

  it("uses local mutation sequence and user-meaningful work labels", () => {
    expect(
      mutationAxLabel({
        ...composerBase,
        pending: {
          kind: "steer",
          status: "failed",
          draftSnapshot: "private draft",
          generation: 42,
        },
      }),
    ).toBe("Steer failed");
    expect(
      mutationAxLabel({
        ...composerBase,
        accepted: { kind: "queue", disposition: "applied" },
      }),
    ).toBe("Queue applied by Hub");
    expect(
      mutationAxLabel({
        ...composerBase,
        accepted: { kind: "send", disposition: "replayed" },
      }),
    ).toBe("Send replayed by Hub");

    const work: LiveWorkItem = {
      key: "opaque-work-key",
      kind: "delegate",
      title: "Review transport",
      detail: "private operational detail",
      tone: "running",
      children: [],
    };
    expect(workItemAxLabel(work)).toBe(
      "Delegate Review transport; status running",
    );
    expect(usageAxLabel()).toBe("Usage summary");
  });
});
