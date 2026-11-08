import type { AnyNotification } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render } from "../renderNative.testkit";
import { launchSchemaOf } from "./newSessionTestUtils";
import { useSheetLaunchSchema } from "./useLaunchSchema";

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
	const seen: unknown[] = [];
	let isReady = ready;
	function Probe() {
		seen.push(useSheetLaunchSchema(store, client as never, isReady));
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

it("reads the hub's schema for the host once, and knows it when it answers", async () => {
	const probe = mount("paradise-park", "/Users/jesse/git/evener");
	expect(probe.latest()).toBeNull();
	expect(probe.calls.map((call) => call.params)).toEqual([
		{ host: "paradise-park", method: "evener/launch/schema", params: {} },
	]);
	const schema = launchSchemaOf(["contextStrategy", "memoryEnabled"]);
	await act(async () => probe.calls[0]?.answer.resolve(schema));
	expect(probe.latest()).toEqual(schema);
});

it("keeps the known schema through a project pick: the schema is about the host", async () => {
	const probe = mount("paradise-park", "/Users/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.resolve(launchSchemaOf(["memoryEnabled"])));
	probe.move({ host: "paradise-park", cwd: "/Users/jesse/git/docs" });
	expect(probe.calls).toHaveLength(1);
	expect(probe.latest()).toEqual(launchSchemaOf(["memoryEnabled"]));
});

it("doesn't re-read when the hub's launch settings change: the schema rides the hub build", async () => {
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.resolve(launchSchemaOf(["memoryEnabled"])));
	probe.notify({ method: "evener/launch/updated", params: {} } as never);
	expect(probe.calls).toHaveLength(1);
	expect(probe.latest()).toEqual(launchSchemaOf(["memoryEnabled"]));
});

it("reads again when the host changes, dropping an answer for a host left behind", async () => {
	const probe = mount("paradise-park", "/Users/jesse/git/evener");
	probe.move({ host: "local", cwd: "/home/jesse/git/evener" });
	expect(probe.calls[1]?.method).toBe("evener/launch/schema");
	await act(async () => probe.calls[0]?.answer.resolve(launchSchemaOf(["memoryEnabled"])));
	expect(probe.latest()).toBeNull();
	await act(async () => probe.calls[1]?.answer.resolve(launchSchemaOf(["contextStrategy"])));
	expect(probe.latest()).toEqual(launchSchemaOf(["contextStrategy"]));
});

it("knows nothing after a failed read, and tries again when the host changes", async () => {
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.reject(new Error("hub away")));
	expect(probe.latest()).toBeNull();
	probe.move({ host: "paradise-park", cwd: "/Users/jesse/git/evener" });
	await act(async () => probe.calls[1]?.answer.resolve(launchSchemaOf(["memoryEnabled"])));
	expect(probe.latest()).toEqual(launchSchemaOf(["memoryEnabled"]));
});

it("asks nothing while the connection is down, and keeps what it knew", async () => {
	expect(mount("local", "/home/jesse/git/evener", false).calls).toEqual([]);
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.resolve(launchSchemaOf(["memoryEnabled"])));
	probe.setReady(false);
	expect(probe.latest()).toEqual(launchSchemaOf(["memoryEnabled"]));
});
