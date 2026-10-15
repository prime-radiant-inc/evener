import recorded from "../../../agent/testdata/summaryfailurewire/events.json?raw";
import type { ThreadItem, ThreadReadResponse } from "../types.gen";
import { currentRead } from "./currentTranscript";

interface SummaryFailureWireFixture {
  warning: {
    message: string;
    source: string;
    code: string;
    title: string;
    hint: string;
    summary_failure: {
      terminal: {
        provider: string;
        model: string;
        route: string;
        kind: string;
        stage: string;
        status: number;
        retryable: boolean;
      };
    };
  };
  item: ThreadItem;
  recovery: ThreadItem;
}

/** Recorded by TestSummaryFailureWireFixtures, never authored client copy. */
export function summaryFailureWireFixture(): SummaryFailureWireFixture {
  return JSON.parse(recorded) as SummaryFailureWireFixture;
}

/** A versioned read carrying the real warning and successful recovery notices. */
export function summaryFailureWireRead(): ThreadReadResponse {
  const fixture = summaryFailureWireFixture();
  const read = currentRead("local:summary-fixture");
  return {
    ...read,
    thread: {
      ...read.thread,
      turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
    },
    overlay: [fixture.item, fixture.recovery].map((item) => ({
      key: item.id,
      kind: "notice",
      turnId: "turn_1",
      item,
    })),
  };
}
