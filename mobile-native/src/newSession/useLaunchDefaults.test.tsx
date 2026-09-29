import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render } from "../renderNative.testkit";
import { NewSessionProvider } from "./newSessionContext";
import { sheetContext } from "./newSessionTestUtils";
import { type LaunchDefaults, useLaunchDefaults } from "./useLaunchDefaults";

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
	const client = {
		request: (method: string, params: unknown) => {
			const answer = deferred();
			calls.push({ method, params, answer });
			return answer.promise;
		},
		onNotification: () => () => {},
	};
	const seen: (LaunchDefaults | null)[] = [];
	function Probe({ host, cwd }: { host: string; cwd: string }) {
		seen.push(useLaunchDefaults(host, cwd));
		return null;
	}
	let context = sheetContext(createNewSessionStore("hub-1"), { client: client as never, ready });
	let place = { host, cwd };
	const page = () => (
		<NewSessionProvider value={context}>
			<Probe host={place.host} cwd={place.cwd} />
		</NewSessionProvider>
	);
	const tree = render(page());
	const move = (next: { host: string; cwd: string }) =>
		act(() => {
			place = next;
			tree.update(page());
		});
	const setReady = (next: boolean) =>
		act(() => {
			context = { ...context, ready: next };
			tree.update(page());
		});
	return { calls, latest: () => seen.at(-1), move, setReady };
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

it("knows nothing after a failed read", async () => {
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.reject(new Error("hub away")));
	expect(probe.latest()).toBeNull();
});

it("asks nothing without a project or while the connection is down, and keeps what it knew", async () => {
	expect(mount("local", "").calls).toEqual([]);
	expect(mount("local", "/home/jesse/git/evener", false).calls).toEqual([]);
	const probe = mount("local", "/home/jesse/git/evener");
	await act(async () => probe.calls[0]?.answer.resolve(resolved({ sandbox: "read-only" })));
	probe.setReady(false);
	expect(probe.latest()).toEqual({ sandbox: "read-only" });
});
