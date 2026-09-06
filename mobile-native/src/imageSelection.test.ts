import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, it } from "vitest";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { ImageSelection, type PickedImage } from "./imageSelection";

const databases: DatabaseSync[] = [];
afterEach(() => {
  for (const db of databases.splice(0)) db.close();
});
function setup(
  pick: () => Promise<PickedImage[]>,
  encode: (image: PickedImage) => Promise<string>,
) {
  const db = new DatabaseSync(":memory:");
  databases.push(db);
  const repository = new DraftRepository({
    execSync: (sql) => db.exec(sql),
    runSync: (sql, ...params) => db.prepare(sql).run(...params),
    getFirstSync: <T>(sql: string, ...params: string[]) =>
      (db.prepare(sql).get(...params) as T | undefined) ?? null,
  });
  const destination = { hubId: "hub", sessionRef: "session" };
  const document = new DraftDocument(() => repository, destination);
  let id = 0;
  return {
    document,
    selection: new ImageSelection(document, {
      pick,
      encode,
      id: () => `image-${++id}`,
    }),
  };
}
const photo = {
  uri: "file:///photo.jpg",
  name: "photo.jpg",
  type: "image/jpeg",
  size: 10,
};

it("enforces current-web type, count and source-size limits before encoding", async () => {
  let encoded = 0;
  const { document, selection } = setup(
    async () => [
      { ...photo, type: "text/plain" },
      { ...photo, size: 9 * 1024 * 1024 },
      ...Array.from({ length: 9 }, () => photo),
    ],
    async () => {
      encoded++;
      return "AQID";
    },
  );
  await selection.choose();
  expect(encoded).toBe(8);
  expect(document.getSnapshot().record.images).toHaveLength(8);
  expect(selection.getSnapshot().error).not.toBeNull();
  expect(selection.getSnapshot().busy).toBe(false);
});

it("removing a pending image prevents its late encoder result from entering the draft", async () => {
  let finish!: (data: string) => void;
  let started!: () => void;
  const encoding = new Promise<void>((resolve) => {
    started = resolve;
  });
  const { document, selection } = setup(
    async () => [photo],
    () => {
      started();
      return new Promise((resolve) => {
        finish = resolve;
      });
    },
  );
  const choosing = selection.choose();
  await encoding;
  selection.remove("image-1");
  finish("AQID");
  await choosing;
  expect(document.getSnapshot().record).toEqual({
    draft: "",
    unconfirmed: null,
  });
  expect(selection.getSnapshot().pending).toHaveLength(0);
});

it("cancels a destination's selection without applying its late result", async () => {
  let finish!: (images: PickedImage[]) => void;
  const { document, selection } = setup(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
    async () => "AQID",
  );
  const choosing = selection.choose();
  selection.cancel();
  finish([photo]);
  await choosing;
  expect(document.getSnapshot().record.images).toBeUndefined();
  expect(selection.getSnapshot().busy).toBe(false);
});

it("retains current text while decoding and does not reuse a removed marker", async () => {
  const { document, selection } = setup(
    async () => [photo],
    async () => {
      document.edit("newer ");
      return "AQID";
    },
  );
  await selection.choose();
  const first = document.getSnapshot().record.images?.[0];
  expect(first?.marker).toBe(1);
  document.removeImage(first?.id ?? "");
  await selection.choose();
  expect(document.getSnapshot().record.images?.[0]?.marker).toBe(2);
  expect(document.getSnapshot().record.draft.startsWith("newer ")).toBe(true);
});
