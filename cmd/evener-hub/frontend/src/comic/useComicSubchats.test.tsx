import "fake-indexeddb/auto";
import {
  hydrateThread,
  type ThreadModel,
  type ThreadReadParams,
  type ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import {
  subagentOutcomesDelegatesResponse,
  subagentWireStep,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { readComposerDraftSnapshot, writeComposerDraft } from "../panes/session/composer/draft";
import {
  resetTranscriptPagingForTests,
  type UseTranscriptResult,
  useTranscriptReader,
} from "../panes/session/transcript/useTranscript";
import { connectionStore } from "../stores/connection";
import { resetThreadsStoreForTests } from "../stores/threads";
import { useThreadModel } from "../stores/useThreadModel";
import { VirtualList } from "../widgets";
import { useComicActivity } from "./useComicActivity";
import * as comicReader from "./useComicReader";
import { type ComicSubchatState, useComicSubchats } from "./useComicSubchats";

const fixture = subagentOutcomesDelegatesResponse();
const root = fixture.context.ref;
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  connectionStore.setState({ state: "idle", client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
});
function clientFor() {
  const client = new FakeClient("ready");
  client.on("thread/read", (p) => ({
    thread: wireThread(p.ref, {
      turns: [
        {
          id: "t",
          status: "completed",
          itemsView: "full",
          items: [{ id: "answer", type: "agentMessage", text: `CONTENT_${p.ref}` }],
        },
      ],
    }),
  }));
  client.on("thread/unsubscribe", () => ({}));
  client.on("evener/thread/activity/read", (p) => ({
    context: { ...fixture.context, ref: p.ref },
    scope: "session",
    delegates: { known: true, total: p.ref === root ? 3 : 0, active: 0, failed: 0, completed: 0 },
    jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
    watches: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
  }));
  client.on("evener/thread/delegates/list", (p) => ({
    ...fixture,
    context: { ...fixture.context, ref: p.ref },
    scope: "session",
    delegates: p.ref === root ? fixture.delegates : [],
  }));
  connectionStore.getState().connect(client);
  return client;
}

test("keeps one owned strip when its causal event arrives", async () => {
  const actual = comicReader.useComicReader;
  const handles = new Map<string, ReturnType<typeof actual>>();
  vi.spyOn(comicReader, "useComicReader").mockImplementation((ref) => {
    const handle = actual(ref);
    handles.set(ref, handle);
    return handle;
  });
  const client = clientFor();
  writeComposerDraft(root, { text: "PARENT_UNSENT", skillNames: ["fixture-skill"] });
  const draft = readComposerDraftSnapshot(root);
  let activity!: ReturnType<typeof useComicActivity>;
  let state!: ComicSubchatState;
  const model = hydrateThread({ thread: wireThread(root) }, root, 0);
  function Harness({ model, showRows = true }: { model: ThreadModel; showRows?: boolean }) {
    activity = useComicActivity(root, model);
    const subchats = useComicSubchats(root, undefined);
    state = subchats.state;
    return (
      <>
        {subchats.lifetimes}
        {showRows && <VirtualList count={1} estimateSize={() => 100} renderRow={() => <div>Virtual strip</div>} />}
      </>
    );
  }
  const mounted = render(<Harness model={model} />);
  await waitFor(() => expect(activity.strips).toHaveLength(3));
  expect(
    client.calls
      .filter((c) => c.method === "thread/read" && (c.params as ThreadReadParams).includeTurns !== false)
      .map((c) => (c.params as ThreadReadParams).ref),
  ).not.toContain("local:child-dlg_reported");
  const strip = activity.strips.find((s) => s.delegateId === "dlg_reported")!;
  act(() => state.expand([], strip));
  await waitFor(() => expect(state.readers.size).toBe(1));
  const path = [strip.key];
  const before = {
    strips: activity.strips,
    expandedPaths: state.expandedPaths,
    childReaderID: handles.get(strip.childRef)!.reader.id,
  };
  const recorded = subagentWireStep("call_delegate_1");
  const causalFixtureItemID = "causal-receipt";
  const arrived = {
    ...model,
    turns: [
      {
        id: "causal",
        status: "completed",
        items: [
          {
            ...recorded,
            id: causalFixtureItemID,
            output: JSON.stringify({
              ...JSON.parse(recorded.output!),
              delegate_id: strip.delegateId,
              transcript_ref: strip.childRef,
            }),
          },
        ],
      },
    ],
  };
  mounted.rerender(<Harness model={arrived} />);
  const after = {
    strips: activity.strips,
    expandedPaths: state.expandedPaths,
    childReaderID: handles.get(strip.childRef)!.reader.id,
    sourceItemID: activity.strips.find((s) => s.key === strip.key)?.source?.itemId,
  };
  expect(after.strips.map((strip) => strip.key)).toEqual(before.strips.map((strip) => strip.key));
  expect(after.expandedPaths).toEqual(before.expandedPaths);
  expect(after.childReaderID).toBe(before.childReaderID);
  expect(after.sourceItemID).toBe(causalFixtureItemID);
  expect(
    client.calls.filter(
      (c) =>
        c.method === "thread/read" &&
        (c.params as ThreadReadParams).includeTurns !== false &&
        (c.params as ThreadReadParams).ref === strip.childRef,
    ),
  ).toHaveLength(1);
  mounted.rerender(<Harness model={arrived} showRows={false} />);
  expect(handles.get(strip.childRef)!.reader.alive).toBe(true);
  expect(state.readers.size).toBe(1);
  expect(readComposerDraftSnapshot(root)).toEqual(draft);
  act(() => state.collapse(path));
  await waitFor(() => expect(state.readers.size).toBe(0));
  expect(handles.get(strip.childRef)!.reader.alive).toBe(false);
  expect(readComposerDraftSnapshot(root)).toEqual(draft);
  expect(client.calls.some((c) => c.method === "turn/start" || c.method === "thread/resume")).toBe(false);
  mounted.unmount();
});

test("opens two qualified siblings and one explicit grandchild, rejecting cycles", async () => {
  const client = clientFor();
  let activity!: ReturnType<typeof useComicActivity>;
  let state!: ComicSubchatState;
  const model = hydrateThread({ thread: wireThread(root) }, root, 0);
  function Harness() {
    activity = useComicActivity(root, model);
    const subchats = useComicSubchats(root, undefined);
    state = subchats.state;
    return subchats.lifetimes;
  }
  const mounted = render(<Harness />);
  await waitFor(() => expect(activity.strips).toHaveLength(3));
  const [first, second] = activity.strips;
  act(() => {
    state.expand([], first!);
    state.expand([], second!);
  });
  await waitFor(() => expect(state.readers.size).toBe(2));
  const path = [first!.key];
  act(() =>
    state.expand(path, {
      ...second!,
      key: "grandchild-key",
      ownerRef: first!.childRef,
      childRef: "remote:host/grandchild",
    }),
  );
  await waitFor(() => expect(state.readers.size).toBe(3));
  act(() =>
    state.expand([...path, "grandchild-key"], { ...first!, ownerRef: "remote:host/grandchild", childRef: root }),
  );
  expect(state.readers.size).toBe(3);
  expect(
    client.calls
      .filter((c) => c.method === "thread/read" && (c.params as ThreadReadParams).includeTurns !== false)
      .map((c) => (c.params as ThreadReadParams).ref)
      .sort(),
  ).toEqual([first!.childRef, second!.childRef, "remote:host/grandchild"].sort());
  act(() => state.collapse(path));
  await waitFor(() => expect([...state.readers.values()].map((r) => r.ref)).toEqual([second!.childRef]));
  mounted.unmount();
});

test("collapsing a delayed child leaves an independent reader's retry alive across parent selection", async () => {
  const client = clientFor();
  client.on("thread/read", (p) => ({
    thread: wireThread(p.ref, {
      turns: [
        {
          id: "current",
          status: "completed",
          itemsView: "full",
          items: [{ id: "loaded", type: "agentMessage", text: "LOADED_CHILD" }],
        },
      ],
    }),
    olderCursor: "older",
  }));
  let rejectPage!: (error: Error) => void;
  let attempts = 0;
  client.on("thread/turns/list", () => {
    attempts++;
    if (attempts === 1)
      return new Promise<ThreadTurnsListResponse>((_resolve, reject) => {
        rejectPage = reject;
      });
    return {
      data: [
        {
          id: "older-child",
          status: "completed",
          itemsView: "full",
          items: [{ id: "older-answer", type: "agentMessage", text: "OLDER_CHILD" }],
        },
      ],
    };
  });
  let activity!: ReturnType<typeof useComicActivity>;
  let state!: ComicSubchatState;
  let independent!: UseTranscriptResult;
  const model = hydrateThread({ thread: wireThread(root) }, root, 0);
  function Root({ selected = root }: { selected?: string }) {
    activity = useComicActivity(root, model);
    const subchats = useComicSubchats(selected, undefined);
    state = subchats.state;
    return subchats.lifetimes;
  }
  function Independent({ refID }: { refID: string }) {
    useThreadModel(refID);
    const reader = comicReader.useComicReader(refID);
    independent = useTranscriptReader(refID, reader.reader);
    return null;
  }
  const mounted = render(<Root />);
  await waitFor(() => expect(activity.strips.length).toBe(3));
  const strip = activity.strips[0]!;
  act(() => state.expand([], strip));
  await waitFor(() => expect(state.readers.size).toBe(1));
  const other = render(<Independent refID={strip.childRef} />);
  act(() => {
    state.readers.get(JSON.stringify([strip.key]))!.history.loadOlderReportingError();
    independent.loadOlderReportingError();
  });
  await waitFor(() => expect(attempts).toBe(1));
  act(() => state.collapse([strip.key]));
  mounted.rerender(<Root selected="remote:other/selection" />);
  await act(async () => {
    rejectPage(new Error("CHILD_OFFLINE"));
  });
  expect(
    independent.model?.turns.some((turn) =>
      turn.items.some((item) => item.type === "agentMessage" && item.text === "LOADED_CHILD"),
    ),
  ).toBe(true);
  act(() => independent.loadOlderReportingError());
  await waitFor(() => expect(independent.hasOlder).toBe(false), { timeout: 4000 });
  expect(attempts).toBe(2);
  expect(state.readers.size).toBe(0);
  expect(client.calls.some((call) => call.method === "turn/start" || call.method === "thread/resume")).toBe(false);
  other.unmount();
  mounted.unmount();
});
