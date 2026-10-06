import { makeTranscriptDisplayConfig, type ThreadModel } from "@evener/appwire-client";
import {
  subagentResumedDelegatesResponse,
  subagentWireStep,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { sessionActivitySnapshot } from "../../../stores/sessionActivity";
import { activityClient, activitySummary } from "../../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { TranscriptBody } from "./TranscriptBody";
import { seedCurrentDelegate } from "./tools/currentDelegate.testFixture";
import { currentDelegate, resetSubagentModuleStoreForTests } from "./tools/subagentModuleStore";
import { useEntityView } from "./useEntityView";

function SelectedSnapshot({ model }: { model: ThreadModel }) {
  const entities = useEntityView(model.ref, model);
  const selected = currentDelegate("dlg_reported", model.ref, model, entities, "local:child-dlg_reported");
  return <output data-testid="selected-snapshot">{JSON.stringify(selected)}</output>;
}
afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetSubagentModuleStoreForTests();
  resetThreadsStoreForTests();
  resetDisclosureStoreForTests();
});

test.each([
  [2, undefined, 2, "done", true],
  [2, 1, 2, "done", true],
  [2, 2, 2, "done", true],
  [2, 3, 3, "running", false],
  [2, 0, 2, "done", true],
  [0, 2, 2, "running", false],
  [0, 0, 0, "running", false],
] as const)(
  "API generation %s and diagnostics generation %s choose one current snapshot",
  async (apiGeneration, diagnosticGeneration, selectedGeneration, selectedKind, hasReport) => {
    const response = subagentResumedDelegatesResponse();
    const current = response.delegates.find((row) => row.delegateId === "dlg_reported");
    if (!current) throw new Error("recorded resumed delegate missing");
    expect(current.runGeneration).toBe(2);
    current.runGeneration = apiGeneration;
    expect(current).not.toHaveProperty("projectionRevision");
    const recorded = subagentWireStep("call_delegate_1");
    const receipt = {
      ...recorded,
      output: JSON.stringify({
        ...JSON.parse(recorded.output ?? "{}"),
        delegate_id: current.delegateId,
        transcript_ref: current.childRef,
      }),
    };
    const model = seedCurrentDelegate(response.context.ref, current.delegateId, "running", undefined, {
      runGeneration: diagnosticGeneration ?? 1,
      transcriptRef: current.childRef,
    });
    if (diagnosticGeneration === undefined) model.delegates = undefined;
    model.turns = [{ id: receipt.turnId, status: "completed", items: [receipt] }];
    const client = activityClient();
    client.on("evener/thread/delegates/list", () => ({ ...response, scope: "session" }));
    client.on("evener/thread/activity/read", ({ scope }) => ({
      ...activitySummary(model.ref, scope),
      context: response.context,
      scope: "session",
    }));
    client.on("evener/thread/jobs/list", () => ({
      context: response.context,
      scope: "session",
      jobs: [],
      page: { complete: true, issues: [] },
    }));
    connectionStore.setState({ client, state: "ready" });
    render(
      <>
        <TranscriptBody
          model={model}
          config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
          surface="preview"
          sessionRef={model.ref}
          disclosureScope="review:delegate-generation"
        />
        <SelectedSnapshot model={model} />
      </>,
    );
    await waitFor(() =>
      expect(sessionActivitySnapshot(client, model.ref, "session")?.delegates.rows).toHaveLength(
        response.delegates.length,
      ),
    );
    const selected = JSON.parse(screen.getByTestId("selected-snapshot").textContent ?? "null");
    expect.soft(selected.runGeneration).toBe(selectedGeneration);
    expect.soft(selected.reportPreview).toBe(hasReport ? current.reportPreview : undefined);
    expect.soft(screen.getByTestId("delegate-status-word").getAttribute("data-kind")).toBe(selectedKind);
    const bodyId = screen.getByTestId("tool-call-body").id;
    const toggle = screen.getByTestId("tool-row").querySelector(`button[aria-controls="${bodyId}"]`);
    if (!toggle) throw new Error("delegate disclosure missing");
    fireEvent.click(toggle);
    expect.soft(screen.getByTestId("delegate-lifecycle").getAttribute("data-kind")).toBe(selectedKind);
    expect(client.calls.filter((call) => call.method === "thread/read").map((call) => call.params)).toEqual([
      { ref: model.ref, includeTurns: false, subscribe: true, replaceSubscription: false },
    ]);
  },
);
