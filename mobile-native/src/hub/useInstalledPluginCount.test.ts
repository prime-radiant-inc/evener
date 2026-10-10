// The Hub home's installed-plugin count: evener/plugin/list, read on a client
// and on each evener/plugin/updated, keeping the last count through a failure.
import type { AnyNotification, PluginEntry } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";
import { PLUGIN_COUNT_RETRY_MS, useInstalledPluginCount } from "./useInstalledPluginCount";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

afterEach(() => {
	unmountMountedTrees();
	vi.useRealTimers();
});

const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));
const plugins = (count: number) => Array.from({ length: count }, (_, i) => ({ plugin: `p${i}` }) as PluginEntry);

/** A hub whose evener/plugin/list reads the test answers by hand, in any order. */
function hub() {
	const pending: { resolve(value: unknown): void; reject(error: Error): void }[] = [];
	const handlers = new Set<(n: AnyNotification) => void>();
	const client = {
		request: () =>
			new Promise((resolve, reject) => {
				pending.push({ resolve, reject });
			}),
		onNotification: (handler: (n: AnyNotification) => void) => {
			handlers.add(handler);
			return () => {
				handlers.delete(handler);
			};
		},
	};
	return {
		client: client as unknown as Parameters<typeof useInstalledPluginCount>[0],
		pending,
		handlers,
		updated: () => {
			for (const handler of handlers) handler({ method: "evener/plugin/updated", params: {} } as AnyNotification);
		},
	};
}

it("lands only the newest read when an older answer arrives late", async () => {
	const fake = hub();
	const { result } = renderHook(() => useInstalledPluginCount(fake.client));
	fake.updated();
	expect(fake.pending).toHaveLength(2);
	await act(async () => {
		fake.pending[1]?.resolve({ plugins: plugins(3) });
		await settle();
	});
	await act(async () => {
		fake.pending[0]?.resolve({ plugins: plugins(1) });
		await settle();
	});
	expect(result.current).toBe(3);
});

it("keeps the last count through a failed read, and reads again on its own until one lands", async () => {
	vi.useFakeTimers();
	const fake = hub();
	const { result } = renderHook(() => useInstalledPluginCount(fake.client));
	await act(async () => {
		fake.pending[0]?.resolve({ plugins: plugins(2) });
		await vi.advanceTimersByTimeAsync(0);
	});
	fake.updated();
	await act(async () => {
		fake.pending[1]?.reject(new Error("hub busy"));
		await vi.advanceTimersByTimeAsync(0);
	});
	expect(result.current).toBe(2);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(PLUGIN_COUNT_RETRY_MS);
	});
	expect(fake.pending).toHaveLength(3);
	await act(async () => {
		fake.pending[2]?.resolve({ plugins: plugins(4) });
		await vi.advanceTimersByTimeAsync(0);
	});
	expect(result.current).toBe(4);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(PLUGIN_COUNT_RETRY_MS * 3);
	});
	expect(fake.pending).toHaveLength(3);
});

it("stops listening, and reading, when it unmounts", async () => {
	vi.useFakeTimers();
	const fake = hub();
	const { unmount } = renderHook(() => useInstalledPluginCount(fake.client));
	await act(async () => {
		fake.pending[0]?.reject(new Error("hub busy"));
		await vi.advanceTimersByTimeAsync(0);
	});
	unmount();
	expect(fake.handlers.size).toBe(0);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(PLUGIN_COUNT_RETRY_MS);
	});
	expect(fake.pending).toHaveLength(1);
});
