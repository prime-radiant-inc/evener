import { describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
	createHubUpgradeController,
	type UpgradeCheckpoint,
	type UpgradeStorage,
} from "./hubUpgrade";

const response = {
	release: "v1",
	channel: "stable",
	url: "https://example.test/update",
	archive: "archive",
	prefix: "prefix",
	binDir: "bin",
	shareBinDir: "share",
	installed: ["evener"],
	restartMessage: "restart",
};
const overview = { hub: { version: "running", commit: "abc" } };
function fixture(read: UpgradeStorage["read"] = () => null) {
	let current: UpgradeCheckpoint | null = null;
	const storage: UpgradeStorage = {
		read: (hubId) => read(hubId) ?? current,
		write: vi.fn((checkpoint) => {
			current = checkpoint;
		}),
		remove: vi.fn((_hubId, attemptId) => {
			if (current?.attemptId === attemptId) current = null;
		}),
	};
	const requests: Array<{ method: string; params: unknown }> = [];
	const client = {
		request: vi.fn(async (method: string, params: unknown) => {
			requests.push({ method, params });
			return method === "evener/upgrade" ? response : overview;
		}),
	} as unknown as ConversationClientLike;
	return { storage, requests, client };
}
describe("hub upgrade", () => {
	it("does not dispatch when another controller persisted an attempt first", async () => {
		const checkpoints = new Map<string, UpgradeCheckpoint>();
		const storage: UpgradeStorage = {
			read: (hubId) => checkpoints.get(hubId) ?? null,
			write: (checkpoint) => {
				checkpoints.set(checkpoint.hubId, checkpoint);
			},
			remove: (hubId, attemptId) => {
				if (checkpoints.get(hubId)?.attemptId === attemptId)
					checkpoints.delete(hubId);
			},
		};
		const first = fixture();
		const second = fixture();
		const firstController = createHubUpgradeController(
			"hub-a",
			first.client,
			storage,
			() => "first",
		);
		const secondController = createHubUpgradeController(
			"hub-a",
			second.client,
			storage,
			() => "second",
		);
		await firstController.start();
		await secondController.start();
		expect(second.client.request).not.toHaveBeenCalled();
	});

	it("accepts an overview whose commit is omitted", async () => {
		const f = fixture();
		vi.mocked(f.client.request)
			.mockImplementationOnce(async () => response as never)
			.mockResolvedValueOnce({
				hub: { version: "running" },
			} as never);
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "attempt-a",
		);
		await controller.start();
		expect(controller.getSnapshot()).toMatchObject({
			kind: "installed",
			overview: { hub: { version: "running" } },
		});
	});

	it("writes before dispatch and retains installed result plus readback", async () => {
		const f = fixture();
		const request = vi.mocked(f.client.request).getMockImplementation();
		if (!request) throw new Error("fixture request is missing");
		vi.mocked(f.client.request).mockImplementation(((
			method: string,
			params: unknown,
		) => {
			if (method === "evener/upgrade") {
				expect(f.storage.read("hub-a")).toMatchObject({
					hubId: "hub-a",
					pending: true,
					attemptId: expect.any(String),
				});
			}
			return request(method as never, params as never);
		}) as ConversationClientLike["request"]);
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "attempt-a",
		);
		await controller.start();
		expect(f.requests).toEqual([
			{ method: "evener/upgrade", params: { requested: "" } },
			{ method: "evener/settings/overview", params: {} },
		]);
		expect(f.storage.write).toHaveBeenCalledTimes(2);
		expect(controller.getSnapshot()).toEqual({
			kind: "installed",
			response,
			overview,
		});
	});
	it("fails closed when checkpoint storage cannot be read or written", async () => {
		const f = fixture(() => {
			throw new Error("disk");
		});
		const blocked = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "blocked",
		);
		expect(blocked.getSnapshot().kind).toBe("storageUnavailable");
		await blocked.start();
		expect(f.client.request).not.toHaveBeenCalled();
		const writable = fixture();
		vi.mocked(writable.storage.write).mockImplementation(() => {
			throw new Error("full");
		});
		const controller = createHubUpgradeController(
			"hub-a",
			writable.client,
			writable.storage,
			() => "writable",
		);
		await controller.start();
		expect(controller.getSnapshot().kind).toBe("storageUnavailable");
		expect(writable.client.request).not.toHaveBeenCalled();
	});
	it("retains uncertainty after lost reply and reads overview without replay", async () => {
		const f = fixture();
		vi.mocked(f.client.request).mockRejectedValueOnce(new Error("closed"));
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "attempt-a",
		);
		await controller.start();
		vi.mocked(f.client.request).mockResolvedValueOnce(overview as never);
		await controller.reconcileAfterReconnect();
		expect(controller.getSnapshot()).toMatchObject({
			kind: "uncertain",
			overview,
		});
		expect(
			vi.mocked(f.client.request).mock.calls.map(([method]) => method),
		).toEqual(["evener/upgrade", "evener/settings/overview"]);
	});
	it("restores an installed checkpoint without replaying it", async () => {
		const f = fixture(() => ({
			hubId: "hub-a",
			pending: true,
			attemptId: "attempt-a",
			startedAt: 1,
			response,
		}));
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "attempt-a",
		);
		expect(controller.getSnapshot()).toEqual({ kind: "installed", response });
		await controller.start();
		expect(f.client.request).not.toHaveBeenCalled();
	});
	it("requires readback before deliberate rearm", async () => {
		const f = fixture(() => ({
			hubId: "hub-a",
			pending: true,
			attemptId: "attempt-a",
			startedAt: 1,
			response,
		}));
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "error",
		);
		const review = await controller.reviewAnotherUpdate();
		expect(controller.getSnapshot()).toMatchObject({
			kind: "installed",
			overview,
		});
		expect(f.storage.remove).not.toHaveBeenCalled();
		controller.rearm(review);
		expect(f.storage.remove).toHaveBeenCalledWith("hub-a", "attempt-a");
		expect(controller.getSnapshot()).toEqual({ kind: "idle" });
	});
	it("does not expose server error text", async () => {
		const f = fixture();
		vi.mocked(f.client.request).mockRejectedValueOnce(
			new Error("token=secret"),
		);
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "late",
		);
		await controller.start();
		expect(controller.getSnapshot()).toEqual({
			kind: "uncertain",
			message:
				"Upgrade outcome is uncertain. Refresh to verify before retrying.",
		});
	});
	it("blocks late readback publication after disposal", async () => {
		let resolve!: (value: unknown) => void;
		const pending = new Promise((done) => {
			resolve = done;
		});
		const f = fixture();
		const entered = Promise.withResolvers<void>();
		vi.mocked(f.client.request).mockImplementation((async (
			method: string,
			params: unknown,
		) => {
			f.requests.push({ method, params });
			if (method === "evener/upgrade") return response;
			entered.resolve();
			return await pending;
		}) as never);
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "original",
		);
		const run = controller.start();
		await entered.promise;
		controller.dispose();
		resolve(overview);
		await run;
		expect(controller.getSnapshot()).toEqual({ kind: "running" });
	});
});

it("retains a late upgrade reply on its original hub after switching away", async () => {
	const checkpoints = new Map<string, UpgradeCheckpoint>();
	const storage: UpgradeStorage = {
		read: (hubId) => checkpoints.get(hubId) ?? null,
		write: (checkpoint) => {
			checkpoints.set(checkpoint.hubId, checkpoint);
		},
		remove: (hubId) => {
			checkpoints.delete(hubId);
		},
	};
	let complete: (value: typeof response) => void = () => {
		throw new Error("request not started");
	};
	const events: string[] = [];
	const a = fixture();
	vi.mocked(a.client.request).mockImplementation((() => {
		expect(checkpoints.get("hub-a")).toMatchObject({
			hubId: "hub-a",
			pending: true,
		});
		expect(checkpoints.get("hub-a")?.startedAt).toEqual(expect.any(Number));
		events.push("upgrade dispatched");
		return new Promise<typeof response>((resolve) => {
			complete = resolve;
		});
	}) as ConversationClientLike["request"]);
	const controllerA = createHubUpgradeController(
		"hub-a",
		a.client,
		storage,
		() => "original",
	);
	const published = vi.fn();
	controllerA.subscribe(published);
	const running = controllerA.start();
	expect(events).toEqual(["upgrade dispatched"]);
	controllerA.dispose();
	published.mockClear();
	const b = fixture();
	const controllerB = createHubUpgradeController(
		"hub-b",
		b.client,
		storage,
		() => "other",
	);
	complete(response);
	await running;
	expect(published).not.toHaveBeenCalled();
	expect(controllerB.getSnapshot()).toEqual({ kind: "idle" });
	expect(b.client.request).not.toHaveBeenCalled();
	expect(checkpoints.has("hub-b")).toBe(false);
	expect(checkpoints.get("hub-a")?.response).toEqual(response);
	const restored = createHubUpgradeController(
		"hub-a",
		a.client,
		storage,
		() => "restored",
	);
	expect(restored.getSnapshot()).toEqual({ kind: "installed", response });
	await restored.start();
	expect(a.client.request).toHaveBeenCalledTimes(1);
});

it("an older confirmation cannot authorize a later version review", async () => {
	const f = fixture();
	f.storage.write({
		hubId: "hub-a",
		pending: true,
		attemptId: "old",
		startedAt: 1,
		response,
	});
	const controller = createHubUpgradeController(
		"hub-a",
		f.client,
		f.storage,
		() => "new",
	);
	const oldReview = await controller.reviewAnotherUpdate();
	await controller.reconcileAfterReconnect();
	const currentReview = await controller.reviewAnotherUpdate();
	controller.rearm(oldReview);
	expect(f.storage.read("hub-a")?.attemptId).toBe("old");
	controller.rearm(currentReview);
	expect(f.storage.read("hub-a")).toBeNull();
});

it("keeps a new attempt when an old response arrives in the same clock tick", async () => {
	const clock = vi.spyOn(Date, "now").mockReturnValue(1234);
	try {
		const f = fixture();
		const pending = Promise.withResolvers<typeof response>();
		vi.mocked(f.client.request).mockImplementationOnce(
			() => pending.promise as never,
		);
		const oldController = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "old",
		);
		const oldRun = oldController.start();
		oldController.dispose();
		const current = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "new",
		);
		const review = await current.reviewAnotherUpdate();
		current.rearm(review);
		await current.start();
		const checkpoint = f.storage.read("hub-a");
		expect(checkpoint?.attemptId).toBe("new");
		pending.resolve({ ...response, release: "old-release" });
		await oldRun;
		expect(f.storage.read("hub-a")).toEqual(checkpoint);
		expect(current.getSnapshot()).toMatchObject({
			kind: "installed",
			response,
		});
	} finally {
		clock.mockRestore();
	}
});

it("rejects malformed installation and running identity replies without replay", async () => {
	for (const invalid of [
		{},
		{ ...response, installed: [] },
		{ ...response, release: "  " },
		{ ...response, installed: [4] },
	]) {
		const f = fixture();
		vi.mocked(f.client.request).mockResolvedValueOnce(invalid as never);
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "attempt",
		);
		await controller.start();
		expect(controller.getSnapshot().kind).toBe("uncertain");
		expect(f.storage.read("hub-a")?.response).toBeUndefined();
		await controller.start();
		expect(f.client.request).toHaveBeenCalledTimes(1);
	}
	for (const invalid of [
		{},
		{ hub: { version: " " } },
		{ hub: { version: "v1", commit: 4 } },
	]) {
		const f = fixture();
		f.storage.write({
			hubId: "hub-a",
			pending: true,
			attemptId: "old",
			startedAt: 1,
			response,
		});
		vi.mocked(f.client.request).mockResolvedValueOnce(invalid as never);
		const controller = createHubUpgradeController(
			"hub-a",
			f.client,
			f.storage,
			() => "new",
		);
		const review = await controller.reviewAnotherUpdate();
		expect(review).toBeFalsy();
		controller.rearm(review);
		expect(f.storage.read("hub-a")?.attemptId).toBe("old");
	}
});

it("preserves the checkpoint and shows a stable error if clearing storage fails", async () => {
	const f = fixture();
	f.storage.write({
		hubId: "hub-a",
		pending: true,
		attemptId: "old",
		startedAt: 1,
		response,
	});
	vi.mocked(f.storage.remove).mockImplementation(() => {
		throw new Error("secret path");
	});
	const controller = createHubUpgradeController(
		"hub-a",
		f.client,
		f.storage,
		() => "new",
	);
	const review = await controller.reviewAnotherUpdate();
	controller.rearm(review);
	expect(controller.getSnapshot()).toEqual({
		kind: "storageUnavailable",
		message: "Upgrade checkpoint storage is unavailable.",
	});
	await controller.start();
	expect(f.storage.read("hub-a")?.attemptId).toBe("old");
	expect(f.client.request).toHaveBeenCalledTimes(1);
});
