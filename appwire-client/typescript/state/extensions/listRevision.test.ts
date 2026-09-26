// One case per row of listRevision.ts's table, driven through the fence
// itself. The stores that wrap it assert that their reads and writes go
// through it; what the orderings mean is decided here, once.

import { describe, expect, test } from "vitest";
import { createListRevision, readRevisioned, writeRevisioned } from "./listRevision";

/** Records what each revision published, in the order it was published. */
function recorder() {
  const published: string[] = [];
  return { published, write: (label: string) => () => published.push(label) };
}

describe("the owner publishes, nothing else does", () => {
  test("the only request in flight owns the list", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const only = revisions.next();
    revisions.publish(only, write("only"));
    expect(published).toEqual(["only"]);
  });

  test("a request that something newer superseded publishes nothing when it answers", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const older = revisions.next();
    revisions.next();
    revisions.publish(older, write("older"));
    expect(published).toEqual([]);
  });

  test("the owner's answer retires everything below it, so an older answer can never apply", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const older = revisions.next();
    const owner = revisions.next();
    revisions.publish(owner, write("owner"));
    revisions.publish(older, write("older"));
    expect(published).toEqual(["owner"]);
  });
});

describe("a retraction hands ownership down", () => {
  test("the answer the retracted request was holding out publishes now", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const read = revisions.next();
    const write2 = revisions.next();
    revisions.publish(read, write("read")); // held: the write owns the list
    expect(published).toEqual([]);

    revisions.retract(write2);
    expect(published).toEqual(["read"]);
  });

  test("a request still on the wire publishes when it answers, having become the owner", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const read = revisions.next();
    const failed = revisions.next();
    revisions.retract(failed);
    revisions.publish(read, write("read"));
    expect(published).toEqual(["read"]);
  });

  // Ownership walks to the highest revision still live, not one step down: the
  // lower of two retractions happening first must not strand the read beneath
  // them.
  test("two retractions in either order leave the read below them the owner", () => {
    for (const order of [
      [0, 1],
      [1, 0],
    ]) {
      const revisions = createListRevision();
      const { published, write } = recorder();
      const read = revisions.next();
      const writes = [revisions.next(), revisions.next()];
      for (const index of order) {
        const revision = writes[index];
        if (revision === undefined) throw new Error("both writes exist");
        revisions.retract(revision);
      }
      revisions.publish(read, write("read"));
      expect(published).toEqual(["read"]);
    }
  });

  test("retracting a revision something newer superseded changes nothing", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const superseded = revisions.next();
    const owner = revisions.next();
    revisions.retract(superseded);
    revisions.publish(owner, write("owner"));
    expect(published).toEqual(["owner"]);
  });
});

describe("a fence ends everything", () => {
  test("a request on the wire when the fence ran publishes nothing", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const fenced = revisions.next();
    revisions.fence();
    revisions.publish(fenced, write("fenced"));
    expect(published).toEqual([]);
  });

  test("an answer being held when the fence ran is dropped, not published by a later retraction", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const held = revisions.next();
    const owner = revisions.next();
    revisions.publish(held, write("held"));
    revisions.fence();
    revisions.retract(owner);
    expect(published).toEqual([]);
  });

  test("a request issued after the fence owns the list", () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    revisions.next();
    revisions.fence();
    const fresh = revisions.next();
    revisions.publish(fresh, write("fresh"));
    expect(published).toEqual(["fresh"]);
  });
});

describe("the two conventions", () => {
  test("a read publishes its answer, and publishes its failure as list state", async () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    await readRevisioned(revisions, async () => "rows", {
      onAnswer: () => write("rows"),
      onFailure: () => write("failed"),
    });
    expect(published).toEqual(["rows"]);

    await readRevisioned(revisions, () => Promise.reject(new Error("offline")), {
      onAnswer: () => write("rows"),
      onFailure: () => write("failed"),
    });
    expect(published).toEqual(["rows", "failed"]);
  });

  test("a write that will not publish its answer gives its revision back too", async () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    const read = revisions.next();
    // null is a store saying "this answer is not mine to publish" - a fence of
    // its own ended the generation the write was issued in. Publishing nothing
    // and keeping the list would strand the read underneath it.
    await writeRevisioned(
      revisions,
      async () => "ignored",
      () => null,
    );
    revisions.publish(read, write("read"));
    expect(published).toEqual(["read"]);
  });

  test("a write publishes its answer and rejects without owning anything when it fails", async () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    await writeRevisioned(
      revisions,
      async () => "saved",
      () => write("saved"),
    );
    expect(published).toEqual(["saved"]);

    // The read below is issued first and outrun by the failing write; the
    // write publishes nothing, so the read's answer is what lands.
    const read = revisions.next();
    await expect(
      writeRevisioned(
        revisions,
        () => Promise.reject(new Error("refused")),
        () => write("never"),
      ),
    ).rejects.toThrow("refused");
    revisions.publish(read, write("read"));
    expect(published).toEqual(["saved", "read"]);
  });

  test("a failure publisher uses the write's revision and still rethrows", async () => {
    const revisions = createListRevision();
    const { published, write } = recorder();
    let rejectRequest!: (error: Error) => void;
    const writing = writeRevisioned(
      revisions,
      () =>
        new Promise<string>((_resolve, reject) => {
          rejectRequest = reject;
        }),
      () => null,
      () => write("applied"),
    );
    const newer = revisions.next();
    const failure = new Error("clone remains");
    rejectRequest(failure);

    await expect(writing).rejects.toBe(failure);
    expect(published).toEqual([]);
    revisions.retract(newer);
    expect(published).toEqual(["applied"]);
  });
});

describe("hasLive", () => {
  // A read and a write both issue through next(), so both leave the same
  // trace here - this is the seam a store's own "does it want this list"
  // question can read a mutation's intent off, symmetrically with a read's.
  test("a read leaves hasLive true only while its answer is outstanding", async () => {
    const revisions = createListRevision();
    expect(revisions.hasLive()).toBe(false);
    let resolveRequest!: (rows: string) => void;
    const reading = readRevisioned(revisions, () => new Promise<string>((resolve) => (resolveRequest = resolve)), {
      onAnswer: () => () => {},
      onFailure: () => () => {},
    });
    expect(revisions.hasLive()).toBe(true);
    resolveRequest("rows");
    await reading;
    expect(revisions.hasLive()).toBe(false);
  });

  test("a write leaves hasLive true the same way a read does", async () => {
    const revisions = createListRevision();
    let resolveRequest!: (rows: string) => void;
    const writing = writeRevisioned(
      revisions,
      () => new Promise<string>((resolve) => (resolveRequest = resolve)),
      () => () => {},
    );
    expect(revisions.hasLive()).toBe(true);
    resolveRequest("rows");
    await writing;
    expect(revisions.hasLive()).toBe(false);
  });

  test("fence clears hasLive along with every other trace of what it fenced", () => {
    const revisions = createListRevision();
    revisions.next();
    expect(revisions.hasLive()).toBe(true);
    revisions.fence();
    expect(revisions.hasLive()).toBe(false);
  });
});
