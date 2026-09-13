import { describe, expect, it } from "vitest";
import type {
	AnyNotification,
	NavigationReadParams,
	NavigationReadResponse,
} from "../../appwire-client/typescript/types.gen";
import { wireV2 } from "../../cmd/evener-hub/frontend/src/stores/navigation/testing";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { createProjectBrowserController } from "./projectBrowser";

function boundary() {
	const requests: Array<{
		params: NavigationReadParams;
		resolve: (value: NavigationReadResponse) => void;
		reject: (error: Error) => void;
	}> = [];
	const listeners = new Set<(event: AnyNotification) => void>();
	const client: ConversationClientLike = {
		request: (_method, params) =>
			new Promise((resolve, reject) => {
				requests.push({
					params: params as NavigationReadParams,
					resolve,
					reject,
				});
			}),
		onNotification: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
	return { client, requests, listeners };
}
function response(params: NavigationReadParams, data: unknown) {
	return wireV2(
		{
			...params,
			representationVersion: 2,
			offset: params.offset ?? 0,
			limit: params.limit ?? 50,
		},
		data,
		`etag-${params.offset ?? 0}`,
		1,
		"generation-test",
	);
}
const project = (key: string) => ({ key, name: key, session_count: 2 });
const session = (ref: string) => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "p",
	state: "idle",
	kind: "session",
	live: false,
	children: [],
});
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("project browser", () => {
	it("loads projects first and fetches sessions only for the initially expanded project", async () => {
		const { client, requests, listeners } = boundary();
		const controller = createProjectBrowserController(client);
		expect(listeners.size).toBe(0);
		const loading = controller.initialLoad();
		expect(listeners.size).toBe(1);
		expect(requests).toHaveLength(1);
		expect(requests[0]?.params.resource).toBe("catalog");
		requests[0]?.resolve(
			response(requests[0].params, {
				projects: [project("a"), project("b")],
				remaining: 0,
			}),
		);
		await tick();
		expect(
			requests.map(
				(request) =>
					`${request.params.projectKey ?? "catalog"}:${request.params.tier ?? ""}`,
			),
		).toEqual(["catalog:", "a:current", "a:recent"]);
		requests[1]?.resolve(
			response(requests[1].params, {
				key: "a",
				tier: "current",
				sessions: [session("a1")],
				remaining: 0,
				truncated: false,
			}),
		);
		requests[2]?.resolve(
			response(requests[2].params, {
				key: "a",
				tier: "recent",
				sessions: [session("a2")],
				remaining: 0,
				truncated: false,
			}),
		);
		await loading;
		expect(listeners.size).toBe(3);
		expect(
			controller
				.getSnapshot()
				.groups.map((group) => [group.project.key, group.expanded]),
		).toEqual([["a", true]]);
		expect(
			controller.getSnapshot().groups[0]?.sessions.map((row) => row.ref),
		).toEqual(["a1", "a2"]);
		controller.dispose();
		expect(listeners.size).toBe(0);
	});

	it("deduplicates equal session refs between current and recent tiers", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(
			response(requests[0].params, { projects: [project("a")], remaining: 0 }),
		);
		await tick();
		requests[1]?.resolve(
			response(requests[1].params, {
				key: "a",
				tier: "current",
				sessions: [session("same")],
				remaining: 0,
				truncated: false,
			}),
		);
		requests[2]?.resolve(
			response(requests[2].params, {
				key: "a",
				tier: "recent",
				sessions: [session("same")],
				remaining: 0,
				truncated: false,
			}),
		);
		await loading;
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(1);
		controller.dispose();
	});

	it("returns a stable snapshot and ignores a late catalog result after dispose", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		controller.dispose();
		requests[0]?.resolve(
			response(requests[0].params, {
				projects: [project("late")],
				remaining: 0,
			}),
		);
		await loading;
		const disposedSnapshot = controller.getSnapshot();
		expect(controller.getSnapshot()).toBe(disposedSnapshot);
		expect(disposedSnapshot.projects.rows).toHaveLength(0);
		expect(disposedSnapshot.groups).toHaveLength(0);
	});

	it("appends a 20-row session page once and keeps rows when the next page fails", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(
			response(requests[0].params, { projects: [project("a")], remaining: 0 }),
		);
		await tick();
		requests[1]?.resolve(
			response(requests[1].params, {
				key: "a",
				tier: "current",
				sessions: Array.from({ length: 20 }, (_, i) => session(`s${i}`)),
				remaining: 5,
				truncated: false,
			}),
		);
		requests[2]?.resolve(
			response(requests[2].params, {
				key: "a",
				tier: "recent",
				sessions: [],
				remaining: 0,
				truncated: false,
			}),
		);
		await loading;
		const more = controller.loadMoreSessions("a", "current");
		const duplicate = controller.loadMoreSessions("a", "current");
		await tick();
		expect(requests).toHaveLength(4);
		requests[3]?.reject(new Error("page unavailable"));
		await Promise.all([more, duplicate]);
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(20);
		await controller.loadMoreSessions("a", "current");
		expect(requests).toHaveLength(4);
		const retry = controller.retry("a", "current");
		await tick();
		expect(requests).toHaveLength(5);
		expect(requests[4]?.params.offset).toBe(20);
		expect(requests[4]?.params.limit).toBe(20);
		requests[4]?.resolve(
			response(requests[4].params, {
				key: "a",
				tier: "current",
				sessions: [
					session("s20"),
					session("s21"),
					session("s22"),
					session("s23"),
					session("s24"),
				],
				remaining: 0,
				truncated: false,
			}),
		);
		await retry;
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(25);
		controller.dispose();
	});

	it("does not refetch on repeated initialLoad after the catalog is loaded", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(
			response(requests[0].params, { projects: [], remaining: 0 }),
		);
		await loading;
		await controller.initialLoad();
		expect(requests).toHaveLength(1);
		controller.dispose();
	});
});
