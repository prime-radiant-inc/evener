import { expect, it } from "vitest";
import { manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { readOrganizationNavigation } from "./organizationNavigation";

const id = "034Kc9793pXlhHyCRXdeAk";
const remote = "paradise-park:x";
const receipt = {
	generation_id: "g",
	targets: [{ kind: "project" as const, projectKey: "p", revision: 40 }],
};
function checkpoint(kind: "archive" | "favorite", acknowledged = true): NavigationActionCheckpoint {
	return {
		id: "intent",
		operation:
			kind === "favorite"
				? { kind, params: { kind: "project", id: "p", favorited: true } }
				: {
						kind,
						params: {
							kind: "project",
							id: "p",
							workingDir: "/workspace",
							archived: true,
						},
					},
		receipt: acknowledged ? receipt : null,
	};
}
function fixture() {
	const calls: { method: string; params: Record<string, unknown> }[] = [];
	let generation = "g",
		projectRevision = 40,
		archived = true,
		favorite = true;
	let fail = false,
		missing = false,
		changeGeneration = false;
	const client = {
		request: async (method: string, params: Record<string, unknown>) => {
			calls.push({ method, params });
			if (fail) throw Error("offline");
			let body: unknown = manifest(),
				revision = 2;
			if (params.resource === "project") {
				body = {};
				revision = projectRevision;
			}
			if (params.resource === "location")
				body = {
					session:
						params.ref === remote
							? { ref: remote, session_id: "x", host_id: "paradise-park" }
							: { ref: `local:${id}`, session_id: id },
					tier: archived ? "archived" : "recent",
					project_key: "p",
				};
			if (params.resource === "catalog") {
				revision = 5;
				body =
					params.catalog === "archived_projects" && !missing
						? params.offset === 0
							? {
									projects: Array.from({ length: 50 }, (_, i) => ({
										key: `other-${i}`,
									})),
									remaining: 1,
								}
							: {
									projects: [
										{
											key: "p",
											name: "Workspace",
											working_dir: "/workspace",
											is_archived: archived,
											favorite,
										},
									],
									remaining: 0,
								}
						: { projects: [], remaining: 0 };
				if (changeGeneration) generation = "new";
			}
			const response = wireSnapshot(params as never, body, '"fresh"', revision, generation);
			if (params.resource === "location") {
				const metadata = (response.data as { metadata: Record<string, unknown> }).metadata;
				metadata.tier = archived ? "archived" : "recent";
				metadata.project_key = "p";
			}
			return response;
		},
	} as unknown as ConversationClientLike;
	return {
		client,
		calls,
		stale: () => {
			projectRevision = 39;
		},
		restart: () => {
			generation = "new";
		},
		conflicting: () => {
			archived = false;
			favorite = false;
		},
		fail: () => {
			fail = true;
		},
		missing: () => {
			missing = true;
		},
		changeGeneration: () => {
			changeGeneration = true;
		},
	};
}
it("finds the exact moved project beyond the first page and compares independent receipt revisions", async () => {
	const f = fixture();
	expect(await readOrganizationNavigation(f.client, checkpoint("archive"), () => true, true)).toMatchObject({
		title: "Workspace",
		state: "archived",
		settled: true,
	});
	expect(f.calls.filter((c) => c.params.catalog === "archived_projects").map((c) => c.params.offset)).toEqual([0, 50]);
	expect(f.calls.every((c) => c.method === "evener/navigation/read")).toBe(true);
	f.stale();
	await expect(readOrganizationNavigation(f.client, checkpoint("archive"), () => true, true)).rejects.toThrow();
});
it("requires review for unknown archive decisions even when effective placement matches", async () => {
	const f = fixture();
	expect(await readOrganizationNavigation(f.client, checkpoint("archive", false), () => true)).toMatchObject({
		state: "archived",
		settled: false,
	});
	const session: NavigationActionCheckpoint = {
		id: "session",
		operation: {
			kind: "archive",
			params: { kind: "session", id, archived: true },
		},
		receipt: null,
	};
	expect(await readOrganizationNavigation(f.client, session, () => true)).toMatchObject({
		state: "archived",
		settled: false,
	});
	expect(f.calls.some((c) => c.params.ref === `local:${id}`)).toBe(true);
});
it("settles a favorite only when the exact project's current value matches", async () => {
	const f = fixture();
	expect(await readOrganizationNavigation(f.client, checkpoint("favorite", false), () => true)).toMatchObject({
		state: "pinned",
		settled: true,
	});
	f.conflicting();
	expect(await readOrganizationNavigation(f.client, checkpoint("favorite"), () => true)).toMatchObject({
		state: "unpinned",
		settled: false,
	});
	expect(await readOrganizationNavigation(f.client, checkpoint("archive"), () => true)).toMatchObject({
		state: "projects",
		settled: false,
	});
});
it("retains recovery on missing targets or failed target reads", async () => {
	for (const mode of ["missing", "fail"] as const) {
		const f = fixture();
		f[mode]();
		await expect(readOrganizationNavigation(f.client, checkpoint("favorite"), () => true)).rejects.toThrow();
	}
});
it("requires an explicit fresh read after restart and rejects a restart during the check", async () => {
	const f = fixture();
	f.restart();
	await expect(readOrganizationNavigation(f.client, checkpoint("archive"), () => true, true)).rejects.toThrow();
	expect(await readOrganizationNavigation(f.client, checkpoint("archive"), () => true)).toMatchObject({
		generationId: "new",
		settled: true,
	});
	const changing = fixture();
	changing.changeGeneration();
	await expect(readOrganizationNavigation(changing.client, checkpoint("archive"), () => true)).rejects.toThrow();
});
it("rejects unrelated recovery, an archive id that names no session, and obsolete scope", async () => {
	const f = fixture();
	for (const operation of [
		{ kind: "unpin", params: { sessionRef: `local:${id}` } },
		{
			kind: "archive",
			params: { kind: "session", id: "not-a-session", archived: true },
		},
	] as const)
		await expect(
			readOrganizationNavigation(f.client, { ...checkpoint("archive"), operation }, () => true),
		).rejects.toThrow();
	expect(f.calls).toHaveLength(0);
	let current = true;
	const pending = readOrganizationNavigation(f.client, checkpoint("archive"), () => current);
	current = false;
	await expect(pending).rejects.toThrow();
});

it("checks another host's session archive by reading that row's location by its ref (ruling 20)", async () => {
	const f = fixture();
	const archive = (sessionId: string): NavigationActionCheckpoint => ({
		id: "session",
		operation: {
			kind: "archive",
			params: { kind: "session", id: sessionId, archived: true },
		},
		receipt: null,
	});
	expect(await readOrganizationNavigation(f.client, archive(remote), () => true)).toMatchObject({
		state: "archived",
		settled: false,
	});
	expect(f.calls.some((c) => c.params.resource === "location" && c.params.ref === remote)).toBe(true);
	await expect(readOrganizationNavigation(f.client, archive("elsewhere:y"), () => true)).rejects.toThrow(
		"This session's current organization could not be confirmed.",
	);
});
