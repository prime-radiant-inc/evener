import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
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
});

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
