import type { AnyNotification } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render } from "../renderNative.testkit";
import { type LaunchDefaults, useSheetLaunchDefaults } from "./useLaunchDefaults";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

function deferred() {
	let resolve!: (value: unknown) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<unknown>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}

const resolved = (effective: Record<string, unknown>) => ({ effective, layers: {}, provenance: {} });

function mount(host: string, cwd: string, ready = true) {
	const calls: { method: string; params: unknown; answer: ReturnType<typeof deferred> }[] = [];
	let notify: (notification: AnyNotification) => void = () => {};
	const client = {
		request: (method: string, params: unknown) => {
			const answer = deferred();
			calls.push({ method, params, answer });
			return answer.promise;
		},
		onNotification: (handler: (notification: AnyNotification) => void) => {
			notify = handler;
			return () => {};
		},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({ source: host, cwd });
	const seen: (LaunchDefaults | null)[] = [];
	let isReady = ready;
	function Probe() {
		seen.push(useSheetLaunchDefaults(store, client as never, isReady));
		return null;
	}
	const tree = render(<Probe />);
	const move = (next: { host: string; cwd: string }) => act(() => store.setState({ source: next.host, cwd: next.cwd }));
	const setReady = (next: boolean) =>
		act(() => {
			isReady = next;
			tree.update(<Probe />);
		});
	return {
		calls,
		latest: () => seen.at(-1),
		move,
		setReady,
		notify: (n: AnyNotification) => act(() => notify(n)),
	};
}

it("reads the hub's defaults for the host and project, leaving the sheet's overrides out", async () => {
	const probe = mount("paradise-park", "/Users/jesse/git/evener");
	expect(probe.latest()).toBeNull();
	expect(probe.calls.map((call) => call.params)).toEqual([
		{ host: "paradise-park", method: "evener/launch/resolve", params: { cwd: "/Users/jesse/git/evener" } },
	]);
	await act(async () =>
		probe.calls[0]?.answer.resolve(
			resolved({ sandbox: "workspace-write", sandboxNet: false, maxRounds: -1, model: "lunaroute/glm-5.3-vision" }),
		),
	);
	expect(probe.latest()).toEqual({ sandbox: "workspace-write", sandboxNet: false, maxRounds: -1 });
});

it("drops an answer for a place the form has left", async () => {
	const probe = mount("paradise-park", "/Users/jesse/git/evener");
	probe.move({ host: "local", cwd: "/home/jesse/git/docs" });
	expect(probe.calls[1]?.method).toBe("evener/launch/resolve");
	await act(async () => probe.calls[0]?.answer.resolve(resolved({ sandbox: "off" })));
	expect(probe.latest()).toBeNull();
	await act(async () => probe.calls[1]?.answer.resolve(resolved({ sandbox: "read-only" })));
	expect(probe.latest()).toEqual({ sandbox: "read-only" });
});

it("reads again when the hub's launch settings change, keeping the old answer until the new one", async () => {
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.resolve(resolved({ sandbox: "read-only" })));
	probe.notify({ method: "evener/launch/updated", params: {} } as never);
	expect(probe.calls).toHaveLength(2);
	expect(probe.latest()).toEqual({ sandbox: "read-only" });
	await act(async () => probe.calls[1]?.answer.resolve(resolved({ sandbox: "restricted" })));
	expect(probe.latest()).toEqual({ sandbox: "restricted" });
	probe.notify({ method: "evener/plugin/updated", params: {} } as never);
	expect(probe.calls).toHaveLength(2);
});

it("knows nothing after a failed read, and tries again on the next change", async () => {
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.reject(new Error("hub away")));
	expect(probe.latest()).toBeNull();
	probe.notify({ method: "evener/launch/updated", params: {} } as never);
	await act(async () => probe.calls[1]?.answer.resolve(resolved({ sandbox: "read-only" })));
	expect(probe.latest()).toEqual({ sandbox: "read-only" });
});

it("asks nothing without a project or while the connection is down, and keeps what it knew", async () => {
	expect(mount("local", "").calls).toEqual([]);
	expect(mount("local", "/home/jesse/git/evener", false).calls).toEqual([]);
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.resolve(resolved({ sandbox: "read-only" })));
	probe.setReady(false);
	expect(probe.latest()).toEqual({ sandbox: "read-only" });
});
