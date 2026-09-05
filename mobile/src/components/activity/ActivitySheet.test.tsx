// Component tests for the ActivitySheet — the medium/large detent bottom sheet
// with four sections (Tasks, Work, Usage, Controls). Verifies detents, section
// disclosure, focus reachability (via the underlying Sheet), capability-gated
// controls, destructive shutdown confirmation, and structured "Unavailable for
// this source" explanations. Uses a fake ConversationService and a literal
// ActivityView so no real projection or wire is required.

import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MutationReceipt } from "../../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { MobileCapabilities } from "../../conversation/model";
import type {
  ActivityView,
  TaskGroup,
  WorkEntry,
  WorkTone,
} from "../../services/activity";
import type { ConversationService } from "../../services/conversation";
import { __resetSheetHistory, __sheetHistorySettled } from "../../ui/Sheet";
import { ActivitySheet } from "./ActivitySheet";

// --- fixture helpers ---------------------------------------------------------

const ALL_TRUE_CAPS: MobileCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function makeView(over: Partial<ActivityView> = {}): ActivityView {
  return {
    tasks: [],
    work: [],
    usage: {},
    capabilities: ALL_TRUE_CAPS,
    ...over,
  };
}

function makeTaskGroups(): TaskGroup[] {
  return [
    { status: "active", count: 0 },
    { status: "open", count: 3 },
    { status: "done", count: 2 },
  ];
}

function makeWork(): WorkEntry[] {
  return [
    {
      kind: "delegate",
      label: "Subagent",
      tone: "running" as WorkTone,
      durationMs: 5000,
      children: [
        {
          kind: "job",
          label: "shell",
          tone: "running" as WorkTone,
          outputSummary: "1.2 KB",
          diagnostics: {
            rawId: "job-1",
            operationName: "shell",
            statusClass: "running",
            outputBytes: 1200,
          },
        },
      ],
      diagnostics: {
        rawId: "dlg-1",
        operationName: "subagent",
        statusClass: "running",
      },
    },
  ];
}

function makeReceipt(): MutationReceipt {
  return {
    clientMutationId: "cmid-1",
    disposition: "applied",
    threadId: "thread-1",
    projectionState: "current",
  };
}

// A fake ConversationService that records calls without any network.
class FakeConversationService implements ConversationService {
  compactCalls = 0;
  interruptCalls = 0;
  shutdownCalls = 0;
  renameCalls: string[] = [];
  changeModelCalls: { provider: string; model: string }[] = [];
  setReasoningEffortCalls: string[] = [];
  shutdownShouldReject: Error | null = null;
  renameShouldReject: Error | null = null;

  ref: string | null = null;
  openConv = {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    modelProvider: "anthropic",
    status: "ready",
    items: [],
    capabilities: ALL_TRUE_CAPS,
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
  };

  async open(ref: string) {
    this.ref = ref;
    return this.openConv;
  }
  async loadOlder() {
    return { items: [] };
  }
  subscribeNotifications() {
    return () => {};
  }
  async send() {
    return makeReceipt();
  }
  async steer() {
    return makeReceipt();
  }
  async queue() {
    return makeReceipt();
  }
  async interrupt() {
    this.interruptCalls += 1;
    return makeReceipt();
  }
  async compact() {
    this.compactCalls += 1;
  }
  async shutdown() {
    this.shutdownCalls += 1;
    if (this.shutdownShouldReject !== null) throw this.shutdownShouldReject;
  }
  async changeModel(provider: string, model: string) {
    this.changeModelCalls.push({ provider, model });
  }
  async setReasoningEffort(effort: string) {
    this.setReasoningEffortCalls.push(effort);
  }
  async rename(name: string) {
    this.renameCalls.push(name);
    if (this.renameShouldReject !== null) throw this.renameShouldReject;
  }
  async cancelQueued() {
    return { removedText: "", receipt: makeReceipt() };
  }
  close() {}
}

afterEach(async () => {
  cleanup();
  await __sheetHistorySettled();
  __resetSheetHistory();
  history.replaceState(null, "");
});

// --- tests -------------------------------------------------------------------

describe("ActivitySheet — detents", () => {
  it("renders nothing when closed", () => {
    const service = new FakeConversationService();
    const { container } = render(
      <ActivitySheet
        open={false}
        onClose={() => {}}
        view={makeView()}
        conversationService={service}
      />,
    );
    expect(container.firstChild).toBeNull();
  });

  it("applies the large detent by default", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView()}
        conversationService={service}
      />,
    );
    const sheet = screen.getByRole("dialog");
    expect(sheet.getAttribute("data-activity-detent")).toBe("large");
  });

  it("applies the medium detent when requested", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView()}
        conversationService={service}
        detent="medium"
      />,
    );
    const sheet = screen.getByRole("dialog");
    expect(sheet.getAttribute("data-activity-detent")).toBe("medium");
  });
});

describe("ActivitySheet — sections", () => {
  it("renders all four section headers", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView()}
        conversationService={service}
      />,
    );
    expect(screen.getByText(/tasks/i)).toBeInTheDocument();
    expect(screen.getByText(/work/i)).toBeInTheDocument();
    expect(screen.getByText(/usage/i)).toBeInTheDocument();
    expect(screen.getByText(/controls/i)).toBeInTheDocument();
  });

  it("discloses task detail on tap", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ tasks: makeTaskGroups() })}
        conversationService={service}
      />,
    );
    // Summary is visible by default.
    expect(screen.getByText(/2 done/i)).toBeInTheDocument();
    // Tap the tasks section header to expand.
    const header = screen.getByRole("button", { name: /tasks/i });
    fireEvent.click(header);
    // Expanded detail shows the active/open/done breakdown.
    expect(screen.getByText(/open.*3/i)).toBeInTheDocument();
  });

  it("discloses work entries on tap", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ work: makeWork() })}
        conversationService={service}
      />,
    );
    // Work summary visible.
    expect(screen.getByText(/1 delegate/i)).toBeInTheDocument();
    const header = screen.getByRole("button", { name: /work/i });
    fireEvent.click(header);
    // Expanded shows the delegate label.
    expect(screen.getByText("Subagent")).toBeInTheDocument();
  });

  it("shows raw identifiers only inside diagnostics disclosure", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ work: makeWork() })}
        conversationService={service}
      />,
    );
    // Expand work section.
    fireEvent.click(screen.getByRole("button", { name: /work/i }));
    // The raw ID is NOT in the default expanded view.
    expect(screen.queryByText("dlg-1")).not.toBeInTheDocument();
    // Tap the diagnostics disclosure to reveal raw IDs.
    const diagButton = screen.getByRole("button", { name: /diagnostics/i });
    fireEvent.click(diagButton);
    expect(screen.getByText("dlg-1")).toBeInTheDocument();
  });
});

describe("ActivitySheet — usage", () => {
  it("shows token, cost, and context summaries", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({
          usage: {
            inputTokens: 100,
            outputTokens: 50,
            cacheReadTokens: 200,
            totalTokens: 350,
            cost: "$0.04",
            contextUsed: 50_000,
            contextWindow: 200_000,
            contextRemaining: 150_000,
            contextPressure: 0.25,
          },
        })}
        conversationService={service}
      />,
    );
    expect(screen.getByText(/350/i)).toBeInTheDocument();
    expect(screen.getByText(/\$0\.04/i)).toBeInTheDocument();
    expect(screen.getByText(/25%/i)).toBeInTheDocument();
  });

  it("discloses detailed token breakdown on tap", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({
          usage: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 200 },
        })}
        conversationService={service}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /usage/i }));
    expect(screen.getByText(/input.*100/i)).toBeInTheDocument();
    expect(screen.getByText(/output.*50/i)).toBeInTheDocument();
    expect(screen.getByText(/cache.*200/i)).toBeInTheDocument();
  });
});

describe("ActivitySheet — controls (capability states)", () => {
  it("shows Compact and calls compact() when capability is true", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, compact: true } })}
        conversationService={service}
      />,
    );
    const btn = screen.getByRole("button", { name: /compact/i });
    fireEvent.click(btn);
    expect(service.compactCalls).toBe(1);
  });

  it("hides Compact and shows Unavailable when capability is false", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, compact: false } })}
        conversationService={service}
      />,
    );
    // The compact action button is gone…
    expect(screen.queryByRole("button", { name: /compact/i })).toBeNull();
    // …and a structured "Unavailable for this source" explanation appears.
    const unavailable = screen.getAllByText(/unavailable for this source/i);
    expect(unavailable.length).toBeGreaterThan(0);
  });

  it("shows Interrupt and calls interrupt() when capability is true", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, interrupt: true } })}
        conversationService={service}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /interrupt/i }));
    expect(service.interruptCalls).toBe(1);
  });

  it("hides Interrupt when capability is false", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({
          capabilities: { ...ALL_TRUE_CAPS, interrupt: false },
        })}
        conversationService={service}
      />,
    );
    expect(screen.queryByRole("button", { name: /interrupt/i })).toBeNull();
  });

  it("shows current model and effort when changeModel is true (V1 placeholder)", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({
          capabilities: { ...ALL_TRUE_CAPS, changeModel: true },
          reasoningEffort: "high",
        })}
        conversationService={service}
      />,
    );
    expect(screen.getByText(/high/i)).toBeInTheDocument();
  });

  it("hides model/effort when changeModel is false", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({
          capabilities: { ...ALL_TRUE_CAPS, changeModel: false },
        })}
        conversationService={service}
      />,
    );
    // No model/effort display control.
    const controls = screen.getByTestId("activity-controls");
    expect(controls.textContent).not.toMatch(/effort/i);
  });

  it("shows Rename input and calls rename() when capability is true", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, rename: true } })}
        conversationService={service}
      />,
    );
    const input = screen.getByLabelText(/rename/i);
    fireEvent.change(input, { target: { value: "New Name" } });
    fireEvent.blur(input);
    expect(service.renameCalls).toEqual(["New Name"]);
  });

  it("hides Rename when capability is false", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, rename: false } })}
        conversationService={service}
      />,
    );
    expect(screen.queryByLabelText(/rename/i)).toBeNull();
  });
});

describe("ActivitySheet — shutdown confirmation", () => {
  it("requires confirmation before calling shutdown()", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, shutdown: true } })}
        conversationService={service}
      />,
    );
    // Tapping shutdown does NOT immediately call shutdown().
    fireEvent.click(screen.getByRole("button", { name: /shutdown/i }));
    expect(service.shutdownCalls).toBe(0);
    // A confirmation dialog appears.
    expect(screen.getByText(/confirm shutdown/i)).toBeInTheDocument();
    // Confirming calls shutdown().
    fireEvent.click(screen.getByRole("button", { name: /confirm/i }));
    expect(service.shutdownCalls).toBe(1);
  });

  it("cancel confirmation does not call shutdown()", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, shutdown: true } })}
        conversationService={service}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /shutdown/i }));
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(service.shutdownCalls).toBe(0);
    // Confirmation dialog dismissed.
    expect(screen.queryByText(/confirm shutdown/i)).toBeNull();
  });

  it("hides Shutdown when capability is false", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, shutdown: false } })}
        conversationService={service}
      />,
    );
    expect(screen.queryByRole("button", { name: /shutdown/i })).toBeNull();
  });

  it("surfaces a shutdown error as a structured message", async () => {
    const service = new FakeConversationService();
    service.shutdownShouldReject = new Error("server refused");
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView({ capabilities: { ...ALL_TRUE_CAPS, shutdown: true } })}
        conversationService={service}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /shutdown/i }));
    fireEvent.click(screen.getByRole("button", { name: /confirm/i }));
    expect(await screen.findByText(/server refused/i)).toBeInTheDocument();
  });
});

describe("ActivitySheet — focus reachability", () => {
  it("focuses the Done button (first focusable) when open", async () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={makeView()}
        conversationService={service}
      />,
    );
    const done = screen.getByRole("button", { name: "Done" });
    await vi.waitFor(() => expect(done).toHaveFocus());
  });
});

describe("ActivitySheet — onClose", () => {
  it("Done button calls onClose", async () => {
    const service = new FakeConversationService();
    const onClose = vi.fn();
    render(
      <ActivitySheet
        open
        onClose={onClose}
        view={makeView()}
        conversationService={service}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    await __sheetHistorySettled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("ActivitySheet — empty/null view", () => {
  it("renders empty-state placeholders when view is null", () => {
    const service = new FakeConversationService();
    render(
      <ActivitySheet
        open
        onClose={() => {}}
        view={null}
        conversationService={service}
      />,
    );
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/no activity/i)).toBeInTheDocument();
  });
});
