// @vitest-environment node

import type { PreparedAnnotation } from "@evener/appwire-client";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { APPEND_STORES } from "./browserDraftRepository";
import { SAVE_STORES } from "./documentAnnotationIndexedDB";
import { type AnnotationFixtureWithRuntime, openAnnotationFixture, pollUntil } from "./testing/annotationFixture";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// A save is work whose outcome the retry runtime cannot see until it settles.
// A storage success arriving while its transaction is in flight names the
// commit that save's later failure may leave claimable: the row is durable but
// the completion event - and the wake it publishes - is exactly what the
// watchdog outlives. Retain that trigger across the save and spend it when the
// save settles with a recorded need, so the receipt and the reader wake no
// longer wait for unrelated activity.
test("a storage success during an outstanding save starts the recovery pass its failure needs", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);

  // The save transaction commits, but its completion event is held: the row is
  // durable while the save (and the wake it would publish) still waits.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const save = fixture.runtime.save(operation);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);

  // A drafts commit publishes while the save is outstanding. The annotation
  // adapter routes that publication to the runtime as storage progress.
  const draft = await fixture.drafts.load(fixture.key);
  if (!draft) throw new Error("fixture seeded no draft");
  await fixture.drafts.commitEdit(fixture.key, draft.token, { text: "typing", skillNames: [] }, () => true);

  // The watchdog rejects the save and its catch records the retry need.
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await save).toBeNull();
  hold.release();
  vi.useRealTimers();

  // No further external trigger: the retained storage success must start the
  // pass whose replay of the durable row publishes the lost wake.
  await fixture.runtime.whenIdle();
  expect(listener).toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// The adapter's seam treats every drafts-repository commit as storage
// progress: the drafts subscription is the one channel by which a commit that
// advances a composer's durable token wakes the retry runtime. Every other
// drafts write publishes; appendAnnotation was the only one that did not, so
// the seam's stated invariant was false for appends.
test("an applied append publishes its drafts commit to the repository subscription", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
  });
  const published = vi.fn();
  fixture.drafts.subscribe(published);

  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "applied",
  });
  expect(published).toHaveBeenCalledWith(
    expect.objectContaining({
      namespace: expect.objectContaining({ origin: fixture.key.namespace.origin }),
      sessionRef: fixture.key.sessionRef,
    }),
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// Reconciliation acknowledges the composer from the committed backing, and
// that acknowledgment is exactly the moment a handoff refused against the
// pre-append token becomes claimable again. Outside a pass nothing told the
// runtime: the applied receipt and the reader wake are not storage progress.
// An out-of-pass ordering shows the gap: A commits unobserved, B refuses
// against A's stale composer token, and a direct retry of A reconciles. The
// reconciliation's own signal must start the pass that applies B, with no
// other external trigger.
test("a reconciliation's acknowledgment signals the runtime so a refused handoff recovers", async () => {
  const composer: { preparation: ComposerPreparation | null } = { preparation: null };
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => composer.preparation,
    // A real composer adopts the committed token a reconciliation hands it.
    acknowledgeAnnotation: (preparation, token) => {
      composer.preparation = { ...preparation, token };
    },
  });
  composer.preparation = fixture.preparation;
  const operationB: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.runtime.save(operationB);
  fixture.storageSuccess.mockClear();

  // A's append transaction commits, but its completion event is held: the
  // receipt is applied and the composer's durable token advanced while the
  // handoff waits on an event the watchdog outlives.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const appendA = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await appendA).toEqual({ state: "pending", reason: "owner-unavailable" });
  hold.release();
  vi.useRealTimers();

  // B prepares against the composer's pre-append token, which A's committed
  // append advanced durably, so B refuses.
  expect(await fixture.runtime.handoff(fixture.document, operationB.operationId)).toEqual({
    state: "pending",
    reason: "revision-conflict",
  });

  // A direct retry of A reconciles the unobserved append: the acknowledgment
  // adopts the committed token, which is the moment B becomes claimable.
  expect(await fixture.runtime.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "already-applied",
  });
  // No other trigger follows: the reconciliation's signal must start the pass.
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});
