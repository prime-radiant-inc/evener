import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../protocol/model";
import {
  disclosureDefault,
  isDisclosureOpen,
  resetDisclosureStoreForTests,
  scopedDisclosureId,
  setDisclosureOpen,
} from "../widgets/disclosure/disclosureStore";
import { makeTranscriptDisplayConfig, presetContent, type TranscriptDisplayConfigV1 } from "./config";
import type { TranscriptMetadataVisibility } from "./projector";
import {
  expandDetailsByDefault,
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
      data-cwd={context.thread?.cwd ?? ""}
      data-delegates={String(context.thread?.delegates?.length ?? 0)}
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
  const fallback = expandDetailsByDefault(context.config) || disclosureDefault(context.disclosureScope, id, false);
  return (
    <output data-testid={`disclosure-${id}`} data-open={isDisclosureOpen(scopedId, fallback) ? "true" : "false"} />
  );
}

const equivalentFull = makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("full") });
const namedFull = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });

function baselineProvider(config: TranscriptDisplayConfigV1, eligibleDisclosureIds: readonly string[]) {
  return (
    <TranscriptRenderProvider config={config} disclosureScope="scope" eligibleDisclosureIds={eligibleDisclosureIds}>
      <DisclosureProbe id="tool-a" />
    </TranscriptRenderProvider>
  );
}

test("only named Full establishes a baseline across equivalent Custom transitions", () => {
  const { rerender, unmount } = render(
    baselineProvider(makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }), ["tool-a"]),
  );

  act(() => setDisclosureOpen(scopedDisclosureId("scope", "tool-a"), false));
  rerender(baselineProvider(equivalentFull, ["tool-a"]));
  expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("false");

  rerender(baselineProvider(namedFull, ["tool-a"]));
  expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("true");

  act(() => setDisclosureOpen(scopedDisclosureId("scope", "tool-a"), false));
  rerender(baselineProvider(namedFull, ["tool-a"]));
  expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("false");

  rerender(baselineProvider(equivalentFull, ["tool-a"]));
  expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("false");

  unmount();
  resetDisclosureStoreForTests();
  render(baselineProvider(equivalentFull, ["tool-a"]));
  expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("true");
  cleanup();
  render(baselineProvider(namedFull, ["tool-a"]));
  expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("true");
});

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

test("refreshes snapshot-derived renderer inputs when only ThreadModel identity changes", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const firstThread = { cwd: "/first", delegates: [{ delegateId: "delegate-1" }] } as unknown as ThreadModel;
  const secondThread = {
    cwd: "/second",
    delegates: [{ delegateId: "delegate-1" }, { delegateId: "delegate-2" }],
  } as unknown as ThreadModel;
  const projection = { metadata: { ...metadata }, eligibleDisclosureIds: ["tool"] };
  const { rerender } = render(
    <TranscriptRenderProvider
      config={config}
      projection={projection}
      surface="live"
      disclosureScope="live:snapshot"
      thread={firstThread}
    >
      <ContextProbe />
    </TranscriptRenderProvider>,
  );
  const firstContextValue = observedContext;
  expect(screen.getByTestId("context-probe").getAttribute("data-cwd")).toBe("/first");
  expect(screen.getByTestId("context-probe").getAttribute("data-delegates")).toBe("1");
  rerender(
    <TranscriptRenderProvider
      config={{ ...config, advanced: { ...config.advanced } }}
      projection={{ metadata: { ...metadata }, eligibleDisclosureIds: ["tool"] }}
      surface="live"
      disclosureScope="live:snapshot"
      thread={secondThread}
    >
      <ContextProbe />
    </TranscriptRenderProvider>,
  );
  expect(observedContext).not.toBe(firstContextValue);
  expect(screen.getByTestId("context-probe").getAttribute("data-cwd")).toBe("/second");
  expect(screen.getByTestId("context-probe").getAttribute("data-delegates")).toBe("2");
});
