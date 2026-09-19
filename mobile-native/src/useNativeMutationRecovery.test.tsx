import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { NativeMutationPersistenceSnapshot } from "./nativeMutationRuntime";
import { renderHook } from "./renderNative.testkit";
import {
	type NativeMutationRecoveryRuntime,
	useNativeMutationRecovery,
} from "./useNativeMutationRecovery";

const TARGET_A = JSON.stringify(["hub-a", "ref-a"]);
const TARGET_B = JSON.stringify(["hub-b", "ref-b"]);

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (reason: unknown) => void;
	const promise = new Promise<T>((resolvePromise, rejectPromise) => {
		resolve = resolvePromise;
		reject = rejectPromise;
	});
	return { promise, resolve, reject };
}

function snapshot(): NativeMutationPersistenceSnapshot {
	return { outbox: [], optimistic: [], recovery: [] };
}

function fakeRuntime() {
	type Listener = (targetRefs: readonly string[]) => void;
	const listeners = new Set<Listener>();
	let unsubscribeCount = 0;
	const reads =
		vi.fn<(targetKey: string) => Promise<NativeMutationPersistenceSnapshot>>();
	const runtime: NativeMutationRecoveryRuntime = {
		readTargetRecords: reads,
		subscribeStorage: vi.fn((listener: Listener) => {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
				unsubscribeCount += 1;
			};
		}),
	};
	return {
		runtime,
		reads,
		emit: (targetRefs: readonly string[]) => {
			for (const listener of listeners) listener(targetRefs);
		},
		listenerCount: () => listeners.size,
		unsubscribeCount: () => unsubscribeCount,
	};
}

async function flushAsyncWork() {
	await act(async () => {
		await Promise.resolve();
	});
}

it("loads the exact target snapshot on cold start", async () => {
	const read = deferred<NativeMutationPersistenceSnapshot>();
	const fake = fakeRuntime();
	fake.reads.mockReturnValueOnce(read.promise);

	const hook = renderHook(() =>
		useNativeMutationRecovery(fake.runtime, TARGET_A),
	);

	expect(fake.reads).toHaveBeenCalledWith(TARGET_A);
	expect(hook.result.current).toMatchObject({
		targetKey: TARGET_A,
		loading: true,
		snapshot: null,
	});

	const loaded = snapshot();
	await act(async () => {
		read.resolve(loaded);
		await read.promise;
	});

	expect(hook.result.current).toMatchObject({
		targetKey: TARGET_A,
		loading: false,
		snapshot: loaded,
	});
});

it("clears target A while target B is loading and ignores A completion", async () => {
	const readA = deferred<NativeMutationPersistenceSnapshot>();
	const readB = deferred<NativeMutationPersistenceSnapshot>();
	const fake = fakeRuntime();
	fake.reads
		.mockReturnValueOnce(readA.promise)
		.mockReturnValueOnce(readB.promise);
	let targetKey = TARGET_A;
	const hook = renderHook(() =>
		useNativeMutationRecovery(fake.runtime, targetKey),
	);

	targetKey = TARGET_B;
	hook.rerender();
	expect(hook.result.current).toMatchObject({
		targetKey: TARGET_B,
		loading: true,
		snapshot: null,
	});

	const stale = snapshot();
	await act(async () => {
		readA.resolve(stale);
		await readA.promise;
	});
	expect(hook.result.current.snapshot).toBeNull();

	const current = snapshot();
	await act(async () => {
		readB.resolve(current);
		await readB.promise;
	});
	expect(hook.result.current).toMatchObject({
		targetKey: TARGET_B,
		loading: false,
		snapshot: current,
	});
});

it("unsubscribes on unmount and ignores a late read", async () => {
	const read = deferred<NativeMutationPersistenceSnapshot>();
	const fake = fakeRuntime();
	fake.reads.mockReturnValueOnce(read.promise);
	const hook = renderHook(() =>
		useNativeMutationRecovery(fake.runtime, TARGET_A),
	);

	expect(fake.listenerCount()).toBe(1);
	hook.unmount();
	expect(fake.listenerCount()).toBe(0);
	expect(fake.unsubscribeCount()).toBe(1);

	await act(async () => {
		read.resolve(snapshot());
		await read.promise;
	});
});

it("refreshes only when storage reports the exact target", async () => {
	const fake = fakeRuntime();
	const initial = snapshot();
	fake.reads.mockResolvedValueOnce(initial);
	const hook = renderHook(() =>
		useNativeMutationRecovery(fake.runtime, TARGET_A),
	);
	await flushAsyncWork();
	expect(fake.reads).toHaveBeenCalledTimes(1);

	fake.emit([TARGET_B]);
	expect(fake.reads).toHaveBeenCalledTimes(1);

	const refreshed = snapshot();
	fake.reads.mockResolvedValueOnce(refreshed);
	fake.emit([TARGET_A]);
	expect(fake.reads).toHaveBeenLastCalledWith(TARGET_A);
	await flushAsyncWork();
	expect(hook.result.current.snapshot).toBe(refreshed);
});
