// @vitest-environment node

import type { PreparedAnnotation } from "@evener/appwire-client";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { createDocumentAnnotationRuntime } from "./documentAnnotationRuntime";
import {
  type AnnotationFixture,
  type AnnotationFixtureWithRuntime,
  currentPreparation,
  deferred,
  openAnnotationFixture,
  sampleChangedContent,
} from "./testing/annotationFixture";

afterEach(() => {
  vi.restoreAllMocks();
});

// Disposal is the runtime's only terminal state. The runtime cancels its own
// loop between handoffs, but a handoff already awaiting the composer's
// preparation must observe that cancellation too: otherwise the resolved
// preparation appends to the retained draft and acknowledges a composer that
// application teardown already discarded. The repository's handoff fences
// closure; the runtime must propagate its disposal the same way.
test("dispose while a handoff's preparation is outstanding stops the resumed handoff", async () => {
  const preparationStarted = deferred<void>();
  const preparation = deferred<ComposerPreparation | null>();
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime",
    origin: "https://runtime.example",
    prepareAnnotation: () => {
      preparationStarted.resolve();
      return preparation.promise;
    },
  });
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  const handoff = runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparationStarted.promise;
  fixture.storageSuccess.mockClear();
  runtime.dispose();
  preparation.resolve(fixture.preparation);

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  // The append never ran: the retained draft keeps its pre-annotation text.
  expect((await fixture.drafts.load(fixture.key))?.text).toBe("");
  fixture.repository.close();
  fixture.drafts.close();
});

// The append's own reads are its last asynchronous boundary before the final
// eligibility guard. Disposal there must reach the guard through the same
// handoff cancellation, or the resumed append commits and acknowledges the
// discarded composer.
test("dispose during the append's reads stops the final commit", async () => {
  let fixture: AnnotationFixture | undefined;
  let runtime: ReturnType<typeof createDocumentAnnotationRuntime> | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime",
    origin: "https://runtime.example",
    prepareAnnotation: async () => fixture?.preparation ?? null,
    onAppendReadsIssued: () => runtime?.dispose(),
  });
  runtime = createDocumentAnnotationRuntime(fixture.repository);
  fixture.storageSuccess.mockClear();

  expect(await runtime.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  expect((await fixture.drafts.load(fixture.key))?.text).toBe("");
  fixture.repository.close();
  fixture.drafts.close();
});

// A recovery trigger that arrives while a pass is running names state that pass
// may already have visited: the composer its handoff skipped can have become
// claimable after the sweep moved on. The trigger must be retained and run one
// coalesced follow-up, or the annotation stays pending until unrelated activity
// starts a later pass.
test("a storage success during an active pass runs one follow-up after the sweep skipped the operation", async () => {
  const secondHandoffStarted = deferred<void>();
  const secondHandoffReleased = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime",
    origin: "https://runtime.example",
    prepareAnnotation: async () => {
      preparations += 1;
      // The first operation's handoff is skipped: no composer claims it.
      if (preparations === 1) return null;
      // Block the second handoff so the pass is still active after the sweep
      // visited the first operation.
      if (preparations === 2) {
        secondHandoffStarted.resolve();
        await secondHandoffReleased.promise;
        return null;
      }
      // Every later pass sees the composer, now that it became available.
      return fixture?.preparation ?? null;
    },
  });
  const secondOperation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.repository.saveAnnotation(secondOperation);
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  const pass = runtime.retryPending("composer-open");
  await secondHandoffStarted.promise;
  // The sweep already visited operation-1 and is blocked in operation-2's
  // handoff. The composer's storage commits land during that window, and both
  // coalesce into one follow-up pass.
  runtime.storageSucceeded();
  runtime.storageSucceeded();
  secondHandoffReleased.resolve();
  await pass;
  await runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  // A persisted applied row lists as already-applied: the follow-up's append
  // committed, so the annotation is no longer pending.
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  // Two operations in the skipped pass plus two in the one follow-up; a second
  // follow-up would prepare the still-pending operation again.
  expect(preparations).toBe(4);

  fixture.repository.close();
  fixture.drafts.close();
});

// A direct handoff queued during an active pass is work that pass's follow-up
// must run behind, never work the pass itself waits for. A storage commit while
// the handoff waits asks the pass for a follow-up; if the handoff also waited
// for the follow-up to settle, each would wait for the other and neither the
// handoff, nor any later retry, nor whenIdle() would ever settle.
test("a direct handoff queued during an active pass settles when the pass runs a follow-up", async () => {
  const sweepBlocked = deferred<void>();
  const sweepReleased = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime",
    origin: "https://runtime.example",
    prepareAnnotation: async () => {
      preparations += 1;
      // The sweep's handoff is skipped: no composer claims it. Holding it open
      // keeps the pass active while the reader saves and storage commits.
      if (preparations === 1) {
        sweepBlocked.resolve();
        await sweepReleased.promise;
        return null;
      }
      return fixture?.preparation ?? null;
    },
  });
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  void runtime.retryPending("composer-open");
  await sweepBlocked.promise;
  // A save during the active pass reaches the direct handoff path, which joins
  // the handoff chain behind the running pass. The storage commit then asks
  // that pass for one follow-up.
  const handoff = runtime.handoff(fixture.document, fixture.operation.operationId);
  runtime.storageSucceeded();
  const settled = Promise.all([handoff, runtime.whenIdle()]).then(() => "settled");
  sweepReleased.resolve();

  // A stalled handoff must read as a stall here, not hold the suite until its
  // timeout.
  await expect(
    Promise.race([settled, new Promise<string>((resolve) => setTimeout(() => resolve("stalled"), 1000))]),
  ).resolves.toBe("settled");
  await expect(handoff).resolves.toMatchObject({ state: "applied" });
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// A direct handoff records its refusal only when it RETURNS, so a storage
// commit arriving while its preparation is outstanding finds retryNeeded still
// false and no pass running. Dropping that trigger strands the annotation: the
// handoff then refuses, and nothing starts the pass the recovered composer
// needs until unrelated activity arrives.
test("a storage success during a direct handoff's preparation recovers the refused handoff", async () => {
  const handoffPreparing = deferred<void>();
  const preparationReleased = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime",
    origin: "https://runtime.example",
    prepareAnnotation: async () => {
      preparations += 1;
      // Hold the direct handoff's preparation open while the composer's storage
      // commits land, then refuse: the composer is unavailable to THIS handoff.
      if (preparations === 1) {
        handoffPreparing.resolve();
        await preparationReleased.promise;
        return null;
      }
      // The retained trigger's pass sees the composer, now that it recovered.
      return fixture?.preparation ?? null;
    },
  });
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  const handoff = runtime.handoff(fixture.document, fixture.operation.operationId);
  await handoffPreparing.promise;
  runtime.storageSucceeded();
  preparationReleased.resolve();

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  // No further external trigger: the retained storage success must be what
  // starts the pass that completes the now-claimable handoff.
  await runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// One retained trigger covers the whole outstanding set, so spending it must
// read the work still pending, not the last handoff's own answer. The first
// handoff refuses and the second - a different destination identity, so the
// first's pending receipt cannot FIFO-block it - applies: a spend condition
// that only saw the successful last settlement would clear the trigger and
// strand the refused annotation.
test("a storage success during two direct handoffs recovers the first refusal after the second applies", async () => {
  const firstPreparing = deferred<void>();
  const firstReleased = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime",
    origin: "https://runtime.example",
    prepareAnnotation: async () => {
      preparations += 1;
      // Hold the first handoff's preparation open, so the second handoff is
      // queued and still outstanding when the composer's storage commits land.
      if (preparations === 1) {
        firstPreparing.resolve();
        await firstReleased.promise;
        return null;
      }
      // A fresh preparation for each later append: a committed append advances
      // the draft's session token, so the retained trigger's pass prepares again.
      return currentPreparation(() => fixture)();
    },
  });
  const destination = fixture.operation.destination;
  if (destination.kind !== "browser-origin") throw new Error(`unexpected destination: ${destination.kind}`);
  const secondOperation: PreparedAnnotation = {
    ...fixture.operation,
    operationId: "operation-2",
    destination: { ...destination, paneId: "pane-2" },
  };
  await fixture.repository.saveAnnotation(secondOperation);
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  const first = runtime.handoff(fixture.document, fixture.operation.operationId);
  const second = runtime.handoff(fixture.document, secondOperation.operationId);
  await firstPreparing.promise;
  runtime.storageSucceeded();
  firstReleased.resolve();

  expect(await first).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(await second).toMatchObject({ state: "applied" });
  // No further external trigger: the retained storage success must start the
  // pass that completes the first handoff's refused preparation.
  await runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  // One refused preparation, one for the applied second handoff, and one for
  // the pass that completes the refusal.
  expect(preparations).toBe(3);

  fixture.repository.close();
  fixture.drafts.close();
});

// A changed read confirms its suppression before the outside-transaction hook
// and fences after it. A handoff consulting inside that window sees a
// confirmation with no durable expiry row: the fence never ran, so the verdict
// must stay retryable and record the runtime's need. A hook rejection then
// releases the confirmation and publishes the settle - the only trigger that
// spends that need. Either half alone leaves the operation stalled pending.
test("a handoff consulting during a rejected fence hook is delivered after the release", async () => {
  const hookStarted = deferred<void>();
  const hookGate = deferred<void>();
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-runtime-rejected-fence-hook",
    origin: "https://runtime.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: currentPreparation(() => fixture),
    beforeExpiryFence: async () => {
      hookStarted.resolve();
      await hookGate.promise;
      throw new Error("hook fault");
    },
  });
  const ticket = await fixture.repository.beginRead(fixture.document);
  const acceptance = fixture.repository.acceptRead(ticket, sampleChangedContent());
  await hookStarted.promise;

  const consulting = await fixture.runtime.handoff(fixture.document, fixture.operation.operationId);

  hookGate.resolve();
  await expect(acceptance).rejects.toThrow("hook fault");

  // The release restored the epoch's eligibility and published the settle. The
  // pass that settle spends the recorded need on must re-drive the handoff and
  // commit the append.
  await fixture.runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  expect(fixture.acknowledge).toHaveBeenCalled();
  expect(consulting).toEqual({ state: "pending", reason: "owner-unavailable" });

  fixture.repository.close();
  fixture.drafts.close();
});
