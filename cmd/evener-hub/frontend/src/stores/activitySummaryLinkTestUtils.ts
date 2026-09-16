// A fake summary side of ActivitySummaryLink, for suites that drive the panel
// store's continuation protocol without importing ./activitySummary.
import { vi } from "vitest";
import { type ActivitySummaryLink, linkActivitySummary } from "./activityPanel";

export interface FakeActivitySummaryLink {
  settled: ReturnType<typeof vi.fn<ActivitySummaryLink["onContinuationSettled"]>>;
  unlink: () => void;
}

export function linkFakeActivitySummary(generation?: number): FakeActivitySummaryLink {
  const settled = vi.fn<ActivitySummaryLink["onContinuationSettled"]>();
  const unlink = linkActivitySummary({ summaryGeneration: () => generation, onContinuationSettled: settled });
  return { settled, unlink };
}
