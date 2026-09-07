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
	url: "",
	archive: "",
	prefix: "",
	binDir: "",
	shareBinDir: "",
	installed: ["evener"],
	restartMessage: "restart",
};
const overview = { hub: { version: "running", commit: "abc" } };
function fixture(read: UpgradeStorage["read"] = () => null) {
	const storage: UpgradeStorage = { read, write: vi.fn() };
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
	it("writes before dispatch and retains installed result plus readback", async () => {
		const f = fixture();
		const controller = createHubUpgradeController("hub-a", f.client, f.storage);
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
		const blocked = createHubUpgradeController("hub-a", f.client, f.storage);
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
		);
		await controller.start();
		expect(controller.getSnapshot().kind).toBe("storageUnavailable");
		expect(writable.client.request).not.toHaveBeenCalled();
	});
	it("retains uncertainty after lost reply and reads overview without replay", async () => {
		const f = fixture();
		vi.mocked(f.client.request).mockRejectedValueOnce(new Error("closed"));
		const controller = createHubUpgradeController("hub-a", f.client, f.storage);
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
			startedAt: 1,
			response,
		}));
		const controller = createHubUpgradeController("hub-a", f.client, f.storage);
		expect(controller.getSnapshot()).toEqual({ kind: "installed", response });
		await controller.start();
		expect(f.client.request).not.toHaveBeenCalled();
	});
	it("blocks late readback publication after disposal", async () => {
		let resolve!: (value: unknown) => void;
		const pending = new Promise((done) => {
			resolve = done;
		});
		const f = fixture();
		vi.mocked(f.client.request).mockImplementation((async (
			method: string,
			params: unknown,
		) => {
			f.requests.push({ method, params });
			return method === "evener/upgrade" ? response : await pending;
		}) as never);
		const controller = createHubUpgradeController("hub-a", f.client, f.storage);
		const run = controller.start();
		await Promise.resolve();
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
	const controllerA = createHubUpgradeController("hub-a", a.client, storage);
	const published = vi.fn();
	controllerA.subscribe(published);
	const running = controllerA.start();
	expect(events).toEqual(["upgrade dispatched"]);
	controllerA.dispose();
	published.mockClear();
	const b = fixture();
	const controllerB = createHubUpgradeController("hub-b", b.client, storage);
	complete(response);
	await running;
	expect(published).not.toHaveBeenCalled();
	expect(controllerB.getSnapshot()).toEqual({ kind: "idle" });
	expect(b.client.request).not.toHaveBeenCalled();
	expect(checkpoints.has("hub-b")).toBe(false);
	expect(checkpoints.get("hub-a")?.response).toEqual(response);
	const restored = createHubUpgradeController("hub-a", a.client, storage);
	expect(restored.getSnapshot()).toEqual({ kind: "installed", response });
	await restored.start();
	expect(a.client.request).toHaveBeenCalledTimes(1);
});
