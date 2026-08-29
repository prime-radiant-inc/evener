import { describe, expect, it } from "vitest";
import {
  connectionStatusAxLabel,
  conversationItemAxDescription,
  conversationItemAxLabel,
  mutationAxLabel,
  rosterAxLabel,
  rosterRowAxLabel,
  streamingAnnouncement,
  usageAxLabel,
  workItemAxLabel,
} from "./accessibility-semantics";
import type { LiveComposerView } from "./contract";
import type {
  ConversationDisplayItem,
  LiveConnectionView,
  LiveRosterRow,
  LiveRosterView,
  LiveWorkItem,
} from "./model";

const bounded = (text: string) => ({
  text,
  truncated: false,
  originalUtf8Bytes: new TextEncoder().encode(text).length,
});

const narrativeBase = {
  key: "internal-item-key",
  body: bounded("private transcript body"),
  label: null,
  tone: "running",
  streaming: false,
  questionKey: null,
  evidenceKey: null,
  sequence: "internal-sequence",
} as const;

const markerBase = {
  key: "internal-marker-key",
  sourceKind: "tool",
  semanticKind: "tool",
  label: bounded("read_file"),
  preview: bounded("private preview"),
  duration: null,
  tone: "running",
  state: "completed",
  evidenceKey: "internal-evidence-key",
  sequence: "internal-sequence",
} as const;

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
    ["user", false, null, "Your message; completed"],
    ["assistant", false, null, "Assistant message; completed"],
    ["assistant", true, null, "Assistant message; streaming"],
    ["question", false, null, "Question; response required"],
    ["question", false, "answer", "Question; resolved"],
    ["failure", false, null, "Failure; action required"],
  ] as const)(
    "names %s narrative items with exact shared semantics",
    (sourceKind, streaming, resolution, expected) => {
      const item: ConversationDisplayItem = {
        ...narrativeBase,
        sourceKind,
        streaming,
      };
      const label = conversationItemAxLabel(item, resolution);
      expect(label).toBe(expected);
      expect(label).not.toContain(narrativeBase.key);
      expect(label).not.toContain(narrativeBase.body.text);
    },
  );

  it.each([
    ["notice", "notice", "completed", "Notice; informational"],
    ["notice", "warning-notice", "completed", "Notice; attention required"],
    [
      "system",
      "system-context",
      "unavailable",
      "System context; details hidden",
    ],
    [
      "system",
      "system-activity",
      "completed",
      "System activity; informational",
    ],
    ["reasoning", "reasoning", "running", "Reasoning activity; running"],
    ["tool", "tool", "failed", "Tool activity, read_file; failed"],
    ["attachment", "attachment", "completed", "Attachment; completed"],
    ["unknown", "activity", "unavailable", "Activity; unavailable"],
  ] as const)(
    "names %s/%s markers without preview content",
    (sourceKind, semanticKind, state, expected) => {
      const item: ConversationDisplayItem = {
        ...markerBase,
        sourceKind,
        semanticKind,
        state,
      };
      const label = conversationItemAxLabel(item, null);
      expect(label).toBe(expected);
      expect(label).not.toContain(markerBase.preview.text);
      expect(label).not.toContain(markerBase.key);
      expect(conversationItemAxDescription(item)).toBe(markerBase.preview.text);
    },
  );

  it("returns no accessible description for narrative or previewless markers", () => {
    const narrative: ConversationDisplayItem = {
      ...narrativeBase,
      sourceKind: "user",
    };
    const previewless: ConversationDisplayItem = {
      ...markerBase,
      preview: null,
    };
    expect(conversationItemAxDescription(narrative)).toBeNull();
    expect(conversationItemAxDescription(previewless)).toBeNull();
  });

  it("announces streaming only at phrase boundaries or state transitions", () => {
    const previous: ConversationDisplayItem = {
      ...narrativeBase,
      sourceKind: "assistant",
      body: bounded("Hello"),
      streaming: true,
    };
    const rawDelta: ConversationDisplayItem = {
      ...previous,
      body: bounded("Hello world"),
    };
    const phrase: ConversationDisplayItem = {
      ...previous,
      body: bounded("Hello world."),
    };
    const completed: ConversationDisplayItem = {
      ...phrase,
      streaming: false,
    };
    expect(streamingAnnouncement(completed, rawDelta)).toBe(
      "Assistant message streaming",
    );
    expect(streamingAnnouncement(previous, rawDelta)).toBeNull();
    expect(streamingAnnouncement(rawDelta, phrase)).toBe("Hello world.");
    expect(streamingAnnouncement(phrase, completed)).toBe(
      "Assistant message completed",
    );
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
