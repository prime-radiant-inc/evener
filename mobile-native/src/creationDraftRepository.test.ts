import { DatabaseSync } from "node:sqlite";
import { expect, it } from "vitest";
import { CreationDraftRepository } from "./creationDraftRepository";
import { type DraftDatabase, DraftRepository } from "./draftRepository";

function fixture() {
  const db = new DatabaseSync(":memory:");
  const adapter: DraftDatabase = {
    execSync: (sql) => db.exec(sql),
    runSync: (sql, ...args) => db.prepare(sql).run(...args),
    getFirstSync: <T>(sql: string, ...args: string[]) =>
      (db.prepare(sql).get(...args) as T | undefined) ?? null,
  };
  return {
    db,
    adapter,
    repository: () => new CreationDraftRepository(adapter),
  };
}
const draft = {
  cwd: "/project",
  prompt: "hello [image 1]",
  harness: "evener",
  model: { provider: "p", model: "m" },
  reasoning: "high",
  launchOverrides: { enabledPlugins: [], maxRounds: 7 },
  unconfirmed: false,
  images: [
    {
      id: "image",
      marker: 1,
      mediaType: "image/png",
      data: "AQID",
      name: "fixture.png",
    },
  ],
};
it("restores a complete creation draft from SQLite for its hub only", () => {
  const { db, repository } = fixture();
  try {
    repository().write("hub-a", draft);
    expect(repository().read("hub-a")).toEqual(draft);
    expect(repository().read("hub-b")).toBeNull();
    repository().clear("hub-a");
    expect(repository().read("hub-a")).toBeNull();
    expect(
      db.prepare("SELECT COUNT(*) AS count FROM creation_draft_images").get()
        ?.count,
    ).toBe(0);
  } finally {
    db.close();
  }
});
it("keeps image bytes immutable across text edits and rolls back a failed checkpoint", () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    repo.write("hub-a", draft);
    db.exec(
      "CREATE TRIGGER no_image_update BEFORE UPDATE ON creation_draft_images BEGIN SELECT RAISE(ABORT, 'immutable'); END",
    );
    repo.write("hub-a", { ...draft, prompt: "changed" });
    db.exec(
      "CREATE TRIGGER fail_draft BEFORE UPDATE ON creation_drafts BEGIN SELECT RAISE(ABORT, 'full'); END",
    );
    expect(() =>
      repo.write("hub-a", { ...draft, unconfirmed: true, images: [] }),
    ).toThrow();
    expect(repository().read("hub-a")).toEqual({ ...draft, prompt: "changed" });
  } finally {
    db.close();
  }
});
it("rejects corrupt saved metadata and missing image bytes without overwriting them", () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    repo.write("hub-a", draft);
    db.exec("DELETE FROM creation_draft_images");
    expect(() => repository().read("hub-a")).toThrow();
    db.exec("UPDATE creation_drafts SET draft = '{}'");
    expect(() => repository().read("hub-a")).toThrow();
  } finally {
    db.close();
  }
});

import {
  type ConversationClientLike,
  createNewSessionService,
} from "../../mobile/src/services/newSession";
import { createNewSessionStore } from "./newSession";

it("restores form edits and validates the saved model after binding", async () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    repo.write("a", draft);
    const form = createNewSessionStore("a", () => repo);
    form.getState().bind(
      createNewSessionService({
        onNotification: () => () => {},
        request: async (method: string) => {
          if (method === "model/list")
            return {
              data: [
                { provider: "p", model: "m", reasoningEffortLevels: ["high"] },
              ],
            };
          throw new Error(method);
        },
      } as ConversationClientLike),
    );
    await form.getState().loadModels();
    expect(form.getState()).toMatchObject({
      model: { provider: "p", model: "m" },
      reasoning: "high",
      images: draft.images,
    });
    form.getState().setPrompt("edited");
    const reopened = createNewSessionStore("a", repository);
    expect(reopened.getState()).toMatchObject({
      cwd: "/project",
      prompt: "edited",
      launchOverrides: draft.launchOverrides,
      images: draft.images,
    });
    expect(createNewSessionStore("b", repository).getState().prompt).toBe("");
  } finally {
    db.close();
  }
});
it("checkpoints uncertainty before dispatch and clears only after confirmed creation", async () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    let resolve!: (value: unknown) => void;
    const started = new Promise<unknown>((yes) => {
      resolve = yes;
    });
    const form = createNewSessionStore("a", () => repo);
    form.getState().bind(
      createNewSessionService({
        onNotification: () => () => {},
        request: async (method: string) => {
          expect(method).toBe("thread/start");
          expect(repo.read("a")?.unconfirmed).toBe(true);
          return started;
        },
      } as ConversationClientLike),
    );
    await form.getState().setCwd("/project", false);
    form.getState().setPrompt("keep this");
    const pending = form.getState().submit();
    expect(
      createNewSessionStore("a", repository).getState().unconfirmedCreation,
    ).toBe(true);
    resolve({ thread: { evener: { ref: "local:created" } }, turn: {} });
    expect(await pending).toMatchObject({ status: "created" });
    expect(repo.read("a")).toBeNull();
  } finally {
    db.close();
  }
});
it("blocks creation when the uncertainty checkpoint cannot be saved", async () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    const calls: string[] = [];
    const form = createNewSessionStore("a", () => repo);
    form.getState().bind(
      createNewSessionService({
        onNotification: () => () => {},
        request: async (method: string) => {
          calls.push(method);
          return {};
        },
      } as ConversationClientLike),
    );
    await form.getState().setCwd("/project", false);
    db.exec(
      "CREATE TRIGGER no_checkpoint BEFORE UPDATE ON creation_drafts WHEN json_extract(NEW.draft, '$.unconfirmed') = 1 BEGIN SELECT RAISE(ABORT, 'full'); END",
    );
    expect(await form.getState().submit()).toEqual({ status: "blocked" });
    expect(calls).toEqual([]);
    expect(form.getState().storageError).not.toBeNull();
    expect(repo.read("a")?.unconfirmed).toBe(false);
  } finally {
    db.close();
  }
});
it("does not overwrite an unreadable saved draft with a blank form", async () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    repo.write("a", draft);
    db.exec("UPDATE creation_drafts SET draft = '{}'");
    const form = createNewSessionStore("a", () => repo);
    expect(form.getState().storageLoaded).toBe(false);
    form.getState().setPrompt("new input");
    expect(await form.getState().submit()).toEqual({ status: "blocked" });
    expect(
      db.prepare("SELECT draft FROM creation_drafts WHERE hub_id = 'a'").get()
        ?.draft,
    ).toBe("{}");
    repo.write("a", draft);
    form.getState().retryStorage();
    expect(form.getState()).toMatchObject({
      storageLoaded: true,
      prompt: draft.prompt,
    });
  } finally {
    db.close();
  }
});

it("retains the uncertain draft when creation reply delivery fails", async () => {
  const { db, repository } = fixture();
  try {
    const form = createNewSessionStore("a", repository);
    form.getState().bind(
      createNewSessionService({
        onNotification: () => () => {},
        request: async () => {
          throw new Error("reply lost");
        },
      } as ConversationClientLike),
    );
    await form.getState().setCwd("/project", false);
    const image = draft.images[0];
    if (!image) throw new Error("Missing fixture");
    form.getState().addImage(image);
    expect(await form.getState().submit()).toEqual({ status: "failed" });
    expect(createNewSessionStore("a", repository).getState()).toMatchObject({
      unconfirmedCreation: true,
      images: draft.images,
    });
  } finally {
    db.close();
  }
});

import { creationImageDraft } from "./creationImageDraft";
import { ImageSelection } from "./imageSelection";

it("does not open the picker before the saved draft can be loaded", async () => {
  const { db, repository } = fixture();
  try {
    repository().write("a", draft);
    db.exec("UPDATE creation_drafts SET draft = '{}'");
    const form = createNewSessionStore("a", repository);
    let picks = 0;
    const picker = new ImageSelection(creationImageDraft(form), {
      pick: async () => {
        picks++;
        return [];
      },
      encode: async () => "",
      id: () => "new",
    });
    await picker.choose();
    expect(picks).toBe(0);
  } finally {
    db.close();
  }
});
it("preserves earlier uncertainty when a later checkpoint fails", async () => {
  const { db, repository } = fixture();
  try {
    const repo = repository();
    repo.write("a", {
      ...draft,
      model: null,
      launchOverrides: {},
      unconfirmed: true,
    });
    const form = createNewSessionStore("a", () => repo);
    form.getState().bind(
      createNewSessionService({
        onNotification: () => () => {},
        request: async () => {
          throw new Error("unexpected request");
        },
      } as ConversationClientLike),
    );
    db.exec(
      "CREATE TRIGGER no_checkpoint BEFORE UPDATE ON creation_drafts WHEN json_extract(NEW.draft, '$.unconfirmed') = 1 BEGIN SELECT RAISE(ABORT, 'full'); END",
    );
    form.getState().setPrompt("another attempt");
    expect(await form.getState().submit()).toEqual({ status: "blocked" });
    expect(form.getState().unconfirmedCreation).toBe(true);
    expect(repo.read("a")?.unconfirmed).toBe(true);
  } finally {
    db.close();
  }
});

it("removes creation metadata and images only for the removed hub", () => {
  const { db, adapter } = fixture();
  try {
    const repo = new DraftRepository(adapter);
    repo.creation.write("a", draft);
    repo.creation.write("b", draft);
    repo.removeHub("a");
    expect(repo.creation.read("a")).toBeNull();
    expect(repo.creation.read("b")).toEqual(draft);
    expect(
      db
        .prepare(
          "SELECT COUNT(*) AS count FROM creation_draft_images WHERE hub_id = 'a'",
        )
        .get()?.count,
    ).toBe(0);
  } finally {
    db.close();
  }
});
