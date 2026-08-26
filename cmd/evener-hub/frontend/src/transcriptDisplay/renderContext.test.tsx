import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import {
  disclosureDefault,
  isDisclosureOpen,
  resetDisclosureStoreForTests,
  scopedDisclosureId,
  setDisclosureOpen,
} from "../widgets/disclosure/disclosureStore";
import { makeTranscriptDisplayConfig } from "./config";
import type { TranscriptMetadataVisibility } from "./projector";
import {
  type TranscriptRenderContextValue,
  TranscriptRenderProvider,
  useTranscriptRenderContext,
} from "./renderContext";

const metadata: TranscriptMetadataVisibility = {
  roundTimings: false,
  tokenCounts: false,
  estimatedCost: false,
  systemEvents: false,
  promptEvents: false,
  hookExits: "none",
};

let observedContext: TranscriptRenderContextValue | undefined;

function ContextProbe() {
  const context = useTranscriptRenderContext();
  observedContext = context;
  return (
    <output
      data-testid="context-probe"
      data-config={context.config.content.kind === "preset" ? context.config.content.level : "custom"}
      data-eligible={context.eligibleDisclosureIds.join(",")}
    />
  );
}

afterEach(() => {
  cleanup();
  observedContext = undefined;
  resetDisclosureStoreForTests();
});

beforeEach(() => resetDisclosureStoreForTests());

test("keeps provider context identity across projection objects with equal semantic content", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  const firstProjection = { metadata: { ...metadata }, eligibleDisclosureIds: ["tool_a", "tool_b"] };
  const { rerender } = render(
    <TranscriptRenderProvider
      config={config}
      projection={firstProjection}
      surface="live"
      disclosureScope="live:token-flood"
    >
      <ContextProbe />
    </TranscriptRenderProvider>,
  );
  const firstContextValue = observedContext;
  if (firstContextValue === undefined) throw new Error("provider probe did not render");
  const firstContext = screen.getByTestId("context-probe").getAttribute("data-eligible");
  const secondProjection = { metadata: { ...metadata }, eligibleDisclosureIds: ["tool_a", "tool_b"] };
  rerender(
    <TranscriptRenderProvider
      config={{ ...config, advanced: { ...config.advanced } }}
      projection={secondProjection}
      surface="live"
      disclosureScope="live:token-flood"
    >
      <ContextProbe />
    </TranscriptRenderProvider>,
  );

  expect(observedContext).toBe(firstContextValue);
  expect(screen.getByTestId("context-probe").getAttribute("data-eligible")).toBe(firstContext);

  rerender(
    <TranscriptRenderProvider
      config={config}
      projection={{ metadata: { ...metadata, systemEvents: true }, eligibleDisclosureIds: ["tool_a", "tool_b"] }}
      surface="live"
      disclosureScope="live:token-flood"
    >
      <ContextProbe />
    </TranscriptRenderProvider>,
  );
  expect(observedContext).not.toBe(firstContextValue);
  expect(screen.getByTestId("context-probe").getAttribute("data-config")).toBe("activity");
});

function DisclosureProbe({ id }: { id: string }) {
  const context = useTranscriptRenderContext();
  const scopedId = scopedDisclosureId(context.disclosureScope, id);
  const fallback = disclosureDefault(context.disclosureScope, id, false);
  return (
    <output data-testid={`disclosure-${id}`} data-open={isDisclosureOpen(scopedId, fallback) ? "true" : "false"} />
  );
}

test("refreshes a mounted Full baseline for new rows without reopening a manual close", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const { rerender } = render(
    <TranscriptRenderProvider
      config={config}
      surface="live"
      disclosureScope="live:baseline"
      eligibleDisclosureIds={["old"]}
    >
      <DisclosureProbe id="old" />
      <DisclosureProbe id="new" />
    </TranscriptRenderProvider>,
  );
  expect(screen.getByTestId("disclosure-old").getAttribute("data-open")).toBe("true");
  act(() => setDisclosureOpen(scopedDisclosureId("live:baseline", "old"), false));
  rerender(
    <TranscriptRenderProvider
      config={config}
      surface="live"
      disclosureScope="live:baseline"
      eligibleDisclosureIds={["old", "new"]}
    >
      <DisclosureProbe id="old" />
      <DisclosureProbe id="new" />
    </TranscriptRenderProvider>,
  );
  expect(screen.getByTestId("disclosure-old").getAttribute("data-open")).toBe("false");
  expect(screen.getByTestId("disclosure-new").getAttribute("data-open")).toBe("true");
});

test("clears a stale pre-baseline close when a row becomes eligible", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  act(() => setDisclosureOpen(scopedDisclosureId("live:stale", "new"), false));
  render(
    <TranscriptRenderProvider
      config={config}
      surface="live"
      disclosureScope="live:stale"
      eligibleDisclosureIds={["new"]}
    >
      <DisclosureProbe id="new" />
    </TranscriptRenderProvider>,
  );
  expect(screen.getByTestId("disclosure-new").getAttribute("data-open")).toBe("true");
});
