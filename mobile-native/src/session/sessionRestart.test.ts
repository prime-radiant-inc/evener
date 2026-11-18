// How one step of a restart went, read off a real SessionControls whose
// service is a fake: whether the request went out at all is what decides
// whether a resume is owed.
import { act } from "react-test-renderer";
import { describe, expect, it } from "vitest";
import { renderHook, settle } from "../renderNative.testkit";
import { SessionControls } from "../sessionControls";
import { attemptControl, useSessionRestart } from "./sessionRestart";

function controls({
	current = true,
	forceStop = async () => {},
	resume = async () => {},
}: {
	current?: boolean;
	forceStop?: () => Promise<void>;
	resume?: () => Promise<void>;
} = {}) {
	const state = { current };
	const service = { forceStop, resume } as unknown as ConstructorParameters<typeof SessionControls>[0];
	const made = new SessionControls(
		service,
		async () => {},
		() => {},
		() => state.current,
		() => null,
		() => true,
	);
	return { made, state };
}

describe("one step of a restart", () => {
	it("is done when the step goes through", async () => {
		expect(await attemptControl(controls().made, "forceStop")).toBe("done");
		expect(await attemptControl(controls().made, "resume")).toBe("done");
	});

	it("is refused when the controls would not send it", async () => {
		expect(await attemptControl(controls({ current: false }).made, "resume")).toBe("refused");
		const busy = controls({ forceStop: () => new Promise(() => {}) }).made;
		void busy.forceStop();
		expect(await attemptControl(busy, "resume")).toBe("refused");
	});

	it("failed when the hub refused it", async () => {
		const failing = controls({
			resume: async () => {
				throw new Error("refused");
			},
		});
		expect(await attemptControl(failing.made, "resume")).toBe("failed");
	});

	it("is done when it went out and the controls were replaced before the answer", async () => {
		let answer!: () => void;
		const swapped = controls({
			forceStop: () =>
				new Promise<void>((resolve) => {
					answer = resolve;
				}),
		});
		const step = attemptControl(swapped.made, "forceStop");
		swapped.made.dispose();
		answer();
		expect(await step).toBe("done");
	});

	// The stop was acknowledged, so the resume is owed. Another screen can cover
	// the pane between the two steps; refusing the resume there leaves the
	// daemon stopped with the restart unfinished, and nothing else sends it.
	it("sends an owed resume while another screen covers the pane", async () => {
		let sent = 0;
		let inFront = true;
		const made = new SessionControls(
			{
				forceStop: async () => {},
				resume: async () => {
					sent += 1;
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			// Another screen is in front; the session, its transport and its
			// open thread are live, which is what the owed resume needs to send on.
			(scope) => (scope === undefined || scope === "route" ? inFront : true),
			() => null,
			() => true,
		);
		expect(await attemptControl(made, "forceStop")).toBe("done");
		inFront = false;
		expect(await attemptControl(made, "resume")).toBe("done");
		expect(sent).toBe(1);
	});
	// The stop's own read starts before the resume is tried, and that read has
	// already cleared the service's thread binding while the store still says
	// the session is open. Asking the store would send the resume into a gap
	// where the service throws, and the restart would keep a failed attempt
	// instead of a sent one. The resume waits for the read, then goes.
	it("sends the owed resume once the stop's read has restored the binding", async () => {
		let sent = 0;
		// The stop's read is already out when the resume is first tried, so the
		// service has no thread open even though the session is the same one.
		let canSend = false;
		let settled = "open:reading";
		const made = new SessionControls(
			{
				forceStop: async () => {},
				resume: async () => {
					sent += 1;
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			// The binding answers about the session; sending asks whether the
			// service has a thread open, which the stop's read takes away.
			(scope) => (scope === "send" ? canSend : true),
			() => null,
			() => true,
		);
		const hook = renderHook(() => useSessionRestart(made, settled));
		await act(async () => {
			await hook.result.current.restart();
		});
		// No thread is open on the service, so the resume is refused before it
		// is marked pending rather than failing: the restart still owes it.
		expect(sent).toBe(0);
		expect(made.getSnapshot().pending).toBeNull();
		// The read lands and the binding is back: the resume the stop owed goes
		// out, without anything else having to happen.
		canSend = true;
		settled = "open:ready";
		hook.rerender();
		await settle();
		expect(sent).toBe(1);
	});

	it("sends the resume the covering screen took the controls away from", async () => {
		let sent = 0;
		let forceStops = 0;
		const made = new SessionControls(
			{
				forceStop: async () => {
					forceStops += 1;
				},
				resume: async () => {
					sent += 1;
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		// Another conversation opens over the pane: the screen drops the controls
		// it had, which is what a covering route does.
		let live: SessionControls | null = made;
		const hook = renderHook(() => useSessionRestart(live, "open"));
		await act(async () => {
			await hook.result.current.restart();
		});
		await settle();
		expect(forceStops).toBe(1);
		// Covered before the resume could go out. Coming back brings fresh
		// controls; the resume is still owed, and the daemon is stopped until it
		// goes.
		live = null;
		hook.rerender();
		await settle();
		live = made;
		hook.rerender();
		await settle();
		expect(sent).toBe(1);
	});
});
