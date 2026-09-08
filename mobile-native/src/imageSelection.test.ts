import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, it } from "vitest";
import { MAX_ATTACHMENT_BYTES } from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/limits";
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

it("rejects an encoded PNG over the server byte limit without persisting it", async () => {
  const source = { ...photo, size: MAX_ATTACHMENT_BYTES - 1 };
  const valid = { ...source, name: "valid.png" };
  const encoded = Buffer.alloc(MAX_ATTACHMENT_BYTES + 1).toString("base64");
  const { document, selection } = setup(
    async () => [source, valid],
    async (image) => (image.name === source.name ? encoded : "AQID"),
  );

  await selection.choose();

  expect(document.getSnapshot().record.images).toHaveLength(1);
  expect(document.getSnapshot().record.images?.[0]?.name).toBe(valid.name);
  expect(document.getSnapshot().record.images?.[0]?.marker).toBe(2);
  expect(document.getSnapshot().record.draft).toBe("[image 2]");
  expect(selection.getSnapshot().error).toContain("maximum 8 MB");
  expect(selection.getSnapshot().pending).toHaveLength(0);
});

it("accepts an encoded PNG at the server byte limit", async () => {
  const source = { ...photo, size: MAX_ATTACHMENT_BYTES - 1 };
  const encoded = Buffer.alloc(MAX_ATTACHMENT_BYTES).toString("base64");
  const { document, selection } = setup(
    async () => [source],
    async () => encoded,
  );

  await selection.choose();

  expect(document.getSnapshot().record.images).toHaveLength(1);
  expect(document.imagePreviews()[0]?.data).toBe(encoded);
  expect(selection.getSnapshot().error).toBeNull();
});

it("accepts valid encoded PNGs through the padded size boundary", async () => {
  const source = { ...photo, size: MAX_ATTACHMENT_BYTES - 1 };
  const sizes = [
    MAX_ATTACHMENT_BYTES - 2,
    MAX_ATTACHMENT_BYTES - 1,
    MAX_ATTACHMENT_BYTES,
  ];
  const picked = sizes.map((size) => ({
    ...source,
    name: `image-${size}.png`,
  }));
  const { document, selection } = setup(
    async () => picked,
    async (image) =>
      Buffer.alloc(
        Number(image.name.slice("image-".length, -".png".length)),
      ).toString("base64"),
  );

  await selection.choose();

  expect(document.getSnapshot().record.images).toHaveLength(3);
  expect(selection.getSnapshot().error).toBeNull();
});

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
