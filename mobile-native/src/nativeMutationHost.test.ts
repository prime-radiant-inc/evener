import { expect, test, vi } from "vitest";
import { createNativeMutationHost } from "./nativeMutationHost";

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((resolvePromise, rejectPromise) => {
		resolve = resolvePromise;
		reject = rejectPromise;
	});
	return { promise, resolve, reject };
}

function runtimeFixture() {
	const leases = [{ targetKey: "key-1" }, { targetKey: "key-2" }];
	const runtime = {
		registerTarget: vi.fn(() => vi.fn()),
		start: vi.fn(async () => undefined),
		beginAuthoritativeRead: vi.fn(() => leases[0]),
		reconcileAuthoritativeRead: vi.fn(async () => "reconciled" as const),
	};
	return { runtime, leases };
}

test("construction does not register a target before the committed start", async () => {
	const { runtime } = runtimeFixture();
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	expect(runtime.registerTarget).not.toHaveBeenCalled();
	expect(host.beginRead("ref-1")).toBeUndefined();
	await host.start();
	expect(runtime.registerTarget).toHaveBeenCalledTimes(1);
});

test("re-registers after a committed effect is disposed and restarted", async () => {
	const { runtime, leases } = runtimeFixture();
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	await host.start();
	host.dispose();
	await host.start();

	expect(runtime.registerTarget).toHaveBeenCalledTimes(2);
	expect(host.beginRead("ref-1")).toBe(leases[0]);
});

test("retries registration after the current startup rejects", async () => {
	const { runtime, leases } = runtimeFixture();
	runtime.start
		.mockRejectedValueOnce(new Error("startup failed"))
		.mockResolvedValueOnce(undefined);
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	await expect(host.start()).rejects.toThrow("startup failed");
	await host.start();

	expect(runtime.registerTarget).toHaveBeenCalledTimes(2);
	expect(host.beginRead("ref-1")).toBe(leases[0]);
});

test("a stale startup rejection cannot unregister a restarted owner", async () => {
	const firstStart = deferred<undefined>();
	const { runtime, leases } = runtimeFixture();
	runtime.start
		.mockImplementationOnce(() => firstStart.promise)
		.mockImplementationOnce(async () => undefined);
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	const first = host.start();
	host.dispose();
	const restarted = host.start();
	await restarted;
	firstStart.reject(new Error("first startup failed"));
	await expect(first).rejects.toThrow("first startup failed");

	expect(runtime.registerTarget).toHaveBeenCalledTimes(2);
	expect(host.beginRead("ref-1")).toBe(leases[0]);
});
