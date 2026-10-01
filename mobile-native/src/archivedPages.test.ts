// A project's archived sessions as a page source over the connection's shared
// archived list store: what the Project screen's Archived tab reads, since
// navigation v3 serves no archived rows.
import type { ArchivedListParams } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { completeSession } from "@evener/appwire-client/testing/navigation";
import { expect, it } from "vitest";
import { archivedListStoreFor } from "./archivedLists";
import { ArchivedPages } from "./archivedPages";

const row = (ref: string) => completeSession({ ref, title: ref, updated_at: "2026-09-01T00:00:00Z" });

function hub(pages: Record<string, { refs: string[]; total: number; nextCursor?: string }>) {
	const client = new FakeClient("ready");
	const seen: ArchivedListParams[] = [];
	client.on("evener/archived/list", (params) => {
		seen.push(params);
		const page = pages[params.cursor ?? ""];
		if (!page) throw new Error("no such page");
		return {
			sessions: page.refs.map(row),
			total: page.total,
			...(page.nextCursor ? { nextCursor: page.nextCursor } : {}),
		};
	});
	return { client, seen };
}

it("reads the project's archived list from its catalog, and pages on with the cursor", async () => {
	const { client, seen } = hub({
		"": { refs: ["local:a"], total: 2, nextCursor: "c1" },
		c1: { refs: ["local:b"], total: 2 },
	});
	const pages = new ArchivedPages(archivedListStoreFor(client), "archived_projects", "p");
	expect(pages.getSnapshot()).toEqual({
		loaded: false,
		rows: [],
		remaining: 0,
		loading: false,
		error: null,
		stale: false,
	});

	await pages.refresh();
	expect(pages.getSnapshot()).toMatchObject({ loaded: true, remaining: 1, loading: false, error: null });
	expect(pages.getSnapshot().rows.map((r) => r.ref)).toEqual(["local:a"]);

	await pages.more();
	expect(pages.getSnapshot()).toMatchObject({ loaded: true, remaining: 0 });
	expect(pages.getSnapshot().rows.map((r) => r.ref)).toEqual(["local:a", "local:b"]);
	expect(seen).toEqual([
		{ catalog: "archived_projects", projectKey: "p" },
		{ catalog: "archived_projects", projectKey: "p", cursor: "c1" },
	]);
});

it("keeps one snapshot until its own list changes, and tells only its own listeners", async () => {
	const { client } = hub({ "": { refs: ["local:a"], total: 1 } });
	const store = archivedListStoreFor(client);
	const mine = new ArchivedPages(store, "projects", "p");
	const other = new ArchivedPages(store, "projects", "q");
	let heard = 0;
	const stop = mine.subscribe(() => heard++);

	await other.refresh();
	expect(heard).toBe(0);
	const before = mine.getSnapshot();
	expect(mine.getSnapshot()).toBe(before);

	await mine.refresh();
	expect(heard).toBeGreaterThan(0);
	const loaded = mine.getSnapshot();
	expect(loaded).not.toBe(before);
	await other.refresh();
	expect(mine.getSnapshot()).toBe(loaded);
	stop();
});

// Only a cursor says another page exists; its count is the rows not loaded.
it("counts what remains only while the list has a next page", async () => {
	const { client } = hub({ "": { refs: ["local:a"], total: 5, nextCursor: "c1" } });
	const paged = new ArchivedPages(archivedListStoreFor(client), "projects", "p");
	await paged.refresh();
	expect(paged.getSnapshot().remaining).toBe(4);

	const { client: other } = hub({ "": { refs: ["local:a"], total: 5 } });
	const last = new ArchivedPages(archivedListStoreFor(other), "projects", "p");
	await last.refresh();
	expect(last.getSnapshot().remaining).toBe(0);

	// A total that lags the rows still leaves the cursor's next page to load.
	const { client: lagging } = hub({ "": { refs: ["local:a", "local:b"], total: 2, nextCursor: "c1" } });
	const behind = new ArchivedPages(archivedListStoreFor(lagging), "projects", "p");
	await behind.refresh();
	expect(behind.getSnapshot().remaining).toBe(1);
});

it("reports a failed read and keeps the loaded rows", async () => {
	const { client } = hub({ "": { refs: ["local:a"], total: 2, nextCursor: "c1" } });
	const pages = new ArchivedPages(archivedListStoreFor(client), "projects", "p");
	await pages.refresh();
	await pages.more();
	expect(pages.getSnapshot().rows.map((r) => r.ref)).toEqual(["local:a"]);
	expect(pages.getSnapshot().error).toContain("no such page");
});

// Archived rows aren't part of navigation: there is no navigation version to
// confirm a change against, and no invalidation to follow.
it("declares it has no navigation version", () => {
	const { client } = hub({});
	const pages = new ArchivedPages(archivedListStoreFor(client), "projects", "p");
	expect(pages.navigationVersioned).toBe(false);
	expect(pages.getResourceVersion()).toBeNull();
});

it("shares one store per connection", () => {
	const { client } = hub({});
	const before = client.listenerCount;
	expect(archivedListStoreFor(client)).toBe(archivedListStoreFor(client));
	expect(client.listenerCount).toBe(before + 1);
	expect(archivedListStoreFor(hub({}).client)).not.toBe(archivedListStoreFor(client));
});

// A client reconnects in place, and a recovered connection may serve rows
// that changed while it was away.
it("drops a connection's loaded lists when it recovers", async () => {
	const { client } = hub({ "": { refs: ["local:a"], total: 1 } });
	const pages = new ArchivedPages(archivedListStoreFor(client), "projects", "p");
	await pages.refresh();
	expect(pages.getSnapshot().loaded).toBe(true);

	client.emitStateChange("reconnecting");
	client.emitReady();

	expect(pages.getSnapshot()).toMatchObject({ loaded: false, rows: [] });
});
