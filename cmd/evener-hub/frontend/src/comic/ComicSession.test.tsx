import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetTranscriptPagingForTests } from "../panes/session/transcript/useTranscript";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { ComicSession } from "./ComicSession";

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetWorkspaceStoreForTests();
});
test("a selected existing session retains a direct regular-client link without creating workspace panes", () => {
  const mounted = render(<ComicSession sessionRef="local:existing" onReplaceRef={() => {}} onSelectRef={() => {}} />);
  expect(screen.getByRole("link", { name: /regular/i }).getAttribute("href")).toContain("local%3Aexisting");
  expect(workspaceStore.getState().panes).toHaveLength(0);
  expect(threadsStore.getState().threads.size).toBe(0);
  mounted.unmount();
});

test("selection A→B releases A's foreground claim and ignores its late response", async () => {
  const client = new FakeClient("ready");
  let completeA!: (value: ThreadReadResponse) => void;
  const delayedA = new Promise<ThreadReadResponse>((resolve) => {
    completeA = resolve;
  });
  const makeThread = (ref: string) => ({
    id: `thread-${ref}`,
    sessionId: `session-${ref}`,
    preview: ref,
    ephemeral: false,
    modelProvider: "openai/gpt-test",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "idle" },
    cwd: "/tmp",
    cliVersion: "1",
    source: "evener",
    evener: { ref, capabilities: { send: false }, queue: { revision: 0 } },
    turns: [
      {
        id: `turn-${ref}`,
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: `item-${ref}`,
            type: "userMessage",
            text: `INPUT_${ref}`,
            position: { entry: 1, item: 0 },
            version: 1,
          },
        ],
      },
    ],
  });
  const makeRead = (ref: string) => ({ thread: makeThread(ref) }) as unknown as ThreadReadResponse;
  client.on("thread/read", (params) => (params.ref === "local:A" ? delayedA : makeRead("local:B")));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const mount = (ref: string) => (
    <ComicSession key={ref} sessionRef={ref} onReplaceRef={() => {}} onSelectRef={() => {}} />
  );
  const mounted = render(mount("local:A"));
  await act(async () => {
    mounted.rerender(mount("local:B"));
    await Promise.resolve();
  });
  await act(async () => {
    completeA(makeRead("local:A"));
    await Promise.resolve();
  });
  expect(screen.getByRole("link", { name: /regular/i }).getAttribute("href")).toBe("/s/local%3AB");
  expect(workspaceStore.getState().panes).toHaveLength(0);
  expect(threadsStore.getState().threads.has("local:A")).toBe(false);
  const methods = client.calls.map((call) => call.method);
  expect(methods.filter((method) => method === "turn/start")).toHaveLength(0);
  expect(methods.filter((method) => method === "thread/resume")).toHaveLength(0);
  mounted.unmount();
});
