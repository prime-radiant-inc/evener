import { AppwireClient } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { describe, expect, test, vi } from "vitest";
import { DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS } from "./backgroundTranscriptOptions";
import { createTranscriptResidency } from "./transcriptResidency";

// The 6d removal reduced transcriptResidency to hydration scheduling and
// lifecycle. The two cases the deletion authority marked `migrated`
// (background-hydration slots) survive here; the retired recorded
// fold/rewrite coordinator cases are deleted under disposition.
function setup(pendingBytes = DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS.pendingBytes) {
  const residency = createTranscriptResidency(
    {},
    {
      ...DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS,
      pendingBytes,
    },
  );
  residency.connection(1, true);
  return { residency };
}

describe("transcript residency: hydration scheduling", () => {
  test("limits background hydration to two slots and starts queued foreground work first", async () => {
    const { residency } = setup();
    const refs = ["local:one", "local:two", "local:three", "local:four"];
    const intents = new Map<string, "background" | "foreground">([
      ["local:one", "background"],
      ["local:two", "background"],
      ["local:three", "background"],
      ["local:four", "foreground"],
    ]);
    const gates = new Map(refs.map((ref) => [ref, deferred<void>()]));
    const started: Array<{ ref: string; intent: string }> = [];
    const tasks = refs.map((ref) =>
      residency.scheduleHydration(ref, intents.get(ref) ?? "background", async () => {
        started.push({ ref, intent: intents.get(ref) ?? "background" });
        await gates.get(ref)?.promise;
      }),
    );
    try {
      await vi.waitFor(() => expect(started.map(({ ref }) => ref)).toEqual(["local:one", "local:two"]));
      // A queued foreground task runs before queued background work once a slot frees.
      expect(residency.snapshot().queuedHydrationRefs).toEqual(["local:four", "local:three"]);
      expect(residency.snapshot().networkHydrations).toBe(2);
      gates.get("local:one")?.resolve();
      await vi.waitFor(() => expect(started.map(({ ref }) => ref)).toEqual(["local:one", "local:two", "local:four"]));
      expect(started[2]).toEqual({ ref: "local:four", intent: "foreground" });
      gates.get("local:two")?.resolve();
      gates.get("local:four")?.resolve();
      await vi.waitFor(() =>
        expect(started.map(({ ref }) => ref)).toEqual(["local:one", "local:two", "local:four", "local:three"]),
      );
      gates.get("local:three")?.resolve();
      await Promise.all(tasks);
      expect(residency.snapshot().networkHydrations).toBe(0);
    } finally {
      residency.dispose();
      await Promise.allSettled(tasks);
    }
  });

  test("queued foreground hydration wins the next real Appwire request slot", async () => {
    const { residency } = setup();
    const socket = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
    const connected = client.connect();
    socket.open();
    await connected;

    const readFrames = () =>
      socket.sent
        .map((frame) => JSON.parse(frame) as { id?: number; method?: string; params?: { ref?: string } })
        .filter(
          (frame): frame is { id: number; method: string; params: { ref: string } } =>
            frame.method === "thread/read" && typeof frame.id === "number" && typeof frame.params?.ref === "string",
        )
        .map(({ id, params }) => ({ id, ref: params.ref }));
    const read = (ref: string) => async () => {
      await client.request("thread/read", {
        ref,
        includeTurns: false,
        itemsView: "full",
        subscribe: true,
        itemLimit: 80,
      });
    };
    const tasks = [
      residency.scheduleHydration("local:active-one", "background", read("local:active-one")),
      residency.scheduleHydration("local:active-two", "background", read("local:active-two")),
      residency.scheduleHydration("local:retry", "background", read("local:retry")),
      residency.scheduleHydration("local:healthy", "background", read("local:healthy")),
      residency.scheduleHydration("local:foreground", "foreground", read("local:foreground")),
    ];
    try {
      await vi.waitFor(() =>
        expect(readFrames().map(({ ref }) => ref)).toEqual(["local:active-one", "local:active-two"]),
      );
      expect(residency.snapshot().queuedHydrationRefs).toEqual(["local:foreground", "local:retry", "local:healthy"]);
      expect(residency.snapshot().networkHydrations).toBe(2);

      const first = readFrames()[0]!;
      socket.receive({ id: first.id, result: currentRead(first.ref) });
      await vi.waitFor(() => expect(readFrames()).toHaveLength(3));
      expect(readFrames()[2]?.ref).toBe("local:foreground");

      const second = readFrames()[1]!;
      socket.receive({ id: second.id, result: currentRead(second.ref) });
      await vi.waitFor(() => expect(readFrames()).toHaveLength(4));
      expect(readFrames()[3]?.ref).toBe("local:retry");

      const retry = readFrames()[3]!;
      socket.receive({ id: retry.id, result: currentRead(retry.ref) });
      await vi.waitFor(() => expect(readFrames()).toHaveLength(5));
      expect(readFrames()[4]?.ref).toBe("local:healthy");

      for (const request of [readFrames()[2]!, readFrames()[4]!]) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all(tasks);
      expect(residency.snapshot().networkHydrations).toBe(0);
      expect(readFrames().map(({ ref }) => ref)).toEqual([
        "local:active-one",
        "local:active-two",
        "local:foreground",
        "local:retry",
        "local:healthy",
      ]);
    } finally {
      client.close();
      residency.dispose();
      await Promise.allSettled(tasks);
    }
  });

  test("a disconnected connection retires an issued hydration and its slot survives until the run settles", async () => {
    const { residency } = setup();
    const gate = deferred<void>();
    const started = deferred<void>();
    const task = residency.scheduleHydration("local:one", "background", async () => {
      started.resolve();
      await gate.promise;
    });
    await started.promise;
    expect(residency.snapshot().networkHydrations).toBe(1);
    residency.connection(1, false);
    // The logical claim is retired, but the issued request keeps its slot.
    await task;
    expect(residency.snapshot().networkHydrations).toBe(1);
    gate.resolve();
    await vi.waitFor(() => expect(residency.snapshot().networkHydrations).toBe(0));
    residency.dispose();
  });

  test("retireClaims retires in-flight hydration and releaseClearClaim re-issues it", async () => {
    const { residency } = setup();
    const runs: string[] = [];
    const first = deferred<void>();
    const task = residency.scheduleHydration("local:one", "background", async () => {
      runs.push("run");
      if (runs.length === 1) await first.promise;
    });
    expect(residency.snapshot().networkHydrations).toBe(1);
    residency.retireClaims();
    await task; // the owner-facing wait is retired synchronously
    first.resolve();
    await vi.waitFor(() => expect(residency.snapshot().networkHydrations).toBe(0));
    // The issued request kept its slot until its real promise settled, then the
    // abort re-issued a fresh run.
    residency.releaseClearClaim();
    await vi.waitFor(() => expect(runs.length).toBe(2));
    residency.dispose();
  });

  test("dispose retires an in-flight hydration", async () => {
    const { residency } = setup();
    const gate = deferred<void>();
    const started = deferred<void>();
    const task = residency.scheduleHydration("local:one", "background", async () => {
      started.resolve();
      await gate.promise;
    });
    await started.promise;
    residency.dispose();
    await task;
    gate.resolve();
    await vi.waitFor(() => expect(residency.snapshot().networkHydrations).toBe(0));
  });
});
