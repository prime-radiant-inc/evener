// Retention of a store-driven resume across the views that share one
// conversation. ConversationBody follows `resumedIdentities`; more than one
// workspace view (an ordinary pane, a cascade column) can retain the same ref,
// and dockview unmounts an inactive pane's tree, so an entry must survive for a
// view that is not mounted at the moment the resume lands.
//
// The finding: the retention test only named the ref the inactive view still
// held, so a chain was lost. Two views retain `A`; the mounted one resumes
// `A -> B`, then `B -> C`; the `A -> B` entry survives because the second view
// still names `A`, but `B -> C` was cleared because no view named `B`, and the
// second view could only ever reach `B`. These tests pin the successive-resume
// shape, and the single-view clear the retention must keep.

import { act, cleanup, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { useMemo, useState } from "react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { ConversationBody, type ConversationSeat } from "./ConversationBody";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { readDraft, writeDraft } from "./composer/draft";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";

const SOURCE_REF = "local:root";
const A = "local:A";
const B = "local:B";
const C = "local:C";

// A cascade pane whose derived path covers `leaf` (no client context, so the
// params' own edge chain names every scope).
function cascadeParams(leaf: string) {
  return {
    ref: leaf,
    source: { type: "session" as const, params: { ref: SOURCE_REF } },
    edges: [{ ownerRef: SOURCE_REF, childRef: leaf, delegateId: "d1" }],
  };
}

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetAskDockStoreForTests();
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetWorkspaceStoreForTests();
});

// A mounted conversation that replaces its own scope in place when a resume
// lands, the way a cascade column or a session pane does. The pane record it
// stands for is retyped onto the resumed ref, so it stops covering the
// superseded one.
function Column({ paneId, testId, onFollow }: { paneId: string; testId: string; onFollow?: (ref: string) => void }) {
  const [ref, setRef] = useState(A);
  const seat = useMemo<ConversationSeat>(
    () => ({
      readView: null,
      composerSource: null,
      keepViewRemount: () => true,
      viewFallbackId: paneId,
      focused: false,
      keyboardScrollPaneId: null,
      onResumedIdentity: (resumedRef) => {
        workspaceStore.setState((state) => ({
          panes: state.panes.map((pane) =>
            pane.id === paneId ? { ...pane, params: cascadeParams(resumedRef) } : pane,
          ),
        }));
        setRef(resumedRef);
        onFollow?.(resumedRef);
        return true;
      },
    }),
    [paneId, onFollow],
  );
  return (
    <ConversationBody
      ref={ref}
      seat={seat}
      render={({ content }) => (
        <div data-testid={testId} data-ref={ref}>
          {content}
        </div>
      )}
    />
  );
}

test("a second cascade walks two successive resumes to the newest identity when it mounts", async () => {
  const active: OpenPaneRecord = { id: "cascade-a", type: "sessionZoom", slot: "main", params: cascadeParams(A) };
  const inactive: OpenPaneRecord = {
    id: "cascade-b",
    type: "sessionZoom",
    slot: "secondary",
    params: cascadeParams(A),
  };
  workspaceStore.setState({ panes: [active, inactive], focusedPaneId: active.id });
  writeDraft(A, "draft typed before the resumes");

  function Harness({ inactiveMounted }: { inactiveMounted: boolean }) {
    return (
      <>
        <Column paneId="cascade-a" testId="active-column" />
        {inactiveMounted && <Column paneId="cascade-b" testId="inactive-column" />}
      </>
    );
  }

  const { rerender } = render(<Harness inactiveMounted={false} />);
  await act(async () => {});

  // First resume: A -> B. The mounted column follows; the entry must survive
  // for the second cascade, which still covers A.
  act(() => {
    threadsStore.setState((state) => ({ resumedIdentities: new Map(state.resumedIdentities).set(A, B) }));
  });
  await act(async () => {});
  expect(screen.getByTestId("active-column").getAttribute("data-ref")).toBe(B);
  expect(threadsStore.getState().resumedIdentities.get(A)).toBe(B);

  // Second resume: B -> C, before the second cascade mounts.
  act(() => {
    threadsStore.setState((state) => ({ resumedIdentities: new Map(state.resumedIdentities).set(B, C) }));
  });
  await act(async () => {});
  expect(screen.getByTestId("active-column").getAttribute("data-ref")).toBe(C);

  // The lost link: the second cascade still retains A, so its chain passes
  // through B and B -> C must survive even though no mounted view names B.
  expect(threadsStore.getState().resumedIdentities.get(B)).toBe(C);

  // Mount the second cascade: it must walk A -> B -> C, not stop at B.
  rerender(<Harness inactiveMounted={true} />);
  await act(async () => {});
  expect(screen.getByTestId("inactive-column").getAttribute("data-ref")).toBe(C);
  expect(readDraft(C)).toBe("draft typed before the resumes");
  // Both views have followed, so nothing is retained.
  expect(threadsStore.getState().resumedIdentities.size).toBe(0);
});

test("a single view's resume still clears normally", async () => {
  const active: OpenPaneRecord = { id: "cascade-a", type: "sessionZoom", slot: "main", params: cascadeParams(A) };
  workspaceStore.setState({ panes: [active], focusedPaneId: active.id });

  render(<Column paneId="cascade-a" testId="active-column" />);
  await act(async () => {});

  act(() => {
    threadsStore.setState((state) => ({ resumedIdentities: new Map(state.resumedIdentities).set(A, B) }));
  });
  await act(async () => {});
  expect(screen.getByTestId("active-column").getAttribute("data-ref")).toBe(B);

  act(() => {
    threadsStore.setState((state) => ({ resumedIdentities: new Map(state.resumedIdentities).set(B, C) }));
  });
  await act(async () => {});
  expect(screen.getByTestId("active-column").getAttribute("data-ref")).toBe(C);
  expect(threadsStore.getState().resumedIdentities.size).toBe(0);
});
