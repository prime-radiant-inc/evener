// How one step of a restart went, read off a real SessionControls whose
// service is a fake: whether the request went out at all is what decides
// whether a resume is owed.
import { act } from "react-test-renderer";
import { describe, expect, it } from "vitest";
import { renderHook, settle } from "../renderNative.testkit";
import { SessionControls } from "../sessionControls";
import { ResumeNotDispatched } from "../connection";
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
	// A resume that never reached the wire is a refusal, not the hub's answer:
	// nothing was sent, so the step is still owed. The connection says so with
	// its own error, and a step that rethrows it is a step the hook can never
	// settle - it stays in "resuming" and never tries again when the phone
	// reconnects, leaving a stopped daemon with the resume still owed.
	it("keeps the resume owed when the connection never sent it", async () => {
		const neverSent = controls({
			resume: async () => {
				throw new ResumeNotDispatched("The connection closed");
			},
		});
		expect(await attemptControl(neverSent.made, "resume")).toBe("refused");
	});

	// The same refusal through the hook: the resume goes out on the binding that
	// comes back rather than the restart hanging on the one that could not send.
	it("sends the owed resume after a refusal the connection reported", async () => {
		let sent = 0;
		let refuse = true;
		const made = new SessionControls(
			{
				forceStop: async () => {},
				resume: async () => {
					if (refuse) throw new ResumeNotDispatched("The connection closed");
					sent += 1;
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		let settled = "open:closed";
		const hook = renderHook(() => useSessionRestart("hub-a", "ref-a", made, settled));
		await act(async () => {
			await hook.result.current.restart();
		});
		await settle();
		expect(sent).toBe(0);
		// The hook is still waiting to send it, not settled with a failure.
		expect(hook.result.current.busy).toBe(true);
		expect(hook.result.current.error).toBeNull();
		// The phone reconnects: the resume the stop owed goes out.
		refuse = false;
		settled = "open:ready";
		hook.rerender();
		await settle();
		expect(sent).toBe(1);
		expect(hook.result.current.busy).toBe(false);
	});

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

	// The stop travels on its own recovery connection, so the primary one can
	// reconnect and dispose these controls before the hub answers. A refusal
	// that lands then used to read as an acknowledgement, and the restart went
	// on to resume a runtime the hub had just refused to stop.
	it("fails a stop the hub refused after its controls were gone", async () => {
		let refuse!: (cause: Error) => void;
		const swapped = controls({
			forceStop: () =>
				new Promise<void>((_, reject) => {
					refuse = reject;
				}),
		});
		const step = attemptControl(swapped.made, "forceStop");
		swapped.made.dispose();
		refuse(new Error("refused"));
		expect(await step).toBe("failed");
	});

	it("sends no resume for a stop the hub refused after its controls were gone", async () => {
		let sent = 0;
		let refuse!: (cause: Error) => void;
		const made = new SessionControls(
			{
				forceStop: () =>
					new Promise<void>((_, reject) => {
						refuse = reject;
					}),
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
		const hook = renderHook(() => useSessionRestart("hub-a", "ref-a", made, "open"));
		await act(async () => {
			const restarting = hook.result.current.restart();
			await settle();
			// The primary connection reconnects while the stop is still out on
			// the recovery one, which disposes the controls that sent it.
			made.dispose();
			refuse(new Error("refused"));
			await restarting;
		});
		await settle();
		expect(sent).toBe(0);
		expect(hook.result.current.error).not.toBeNull();
	});

	// The other side of the same line: a resume that went out and whose answer
	// never came may already have been taken, so it is reported rather than
	// retried. Resending it could resume a session twice.
	it("does not retry a resume whose delivery is uncertain", async () => {
		let sent = 0;
		let attempts = 0;
		const made = new SessionControls(
			{
				forceStop: async () => {},
				resume: async () => {
					attempts += 1;
					sent += 1;
					throw new Error("the answer never came");
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		const hook = renderHook(() => useSessionRestart("hub-a", "ref-a", made, "open"));
		await act(async () => {
			await hook.result.current.restart();
		});
		await settle();
		expect(attempts).toBe(1);
		expect(sent).toBe(1);
		// Reported, and the restart is not left waiting to send it again.
		expect(hook.result.current.error).not.toBeNull();
		expect(hook.result.current.busy).toBe(false);
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
		const hook = renderHook(() => useSessionRestart("hub-a", "ref-a", made, settled));
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
		let acknowledge!: () => void;
		const made = new SessionControls(
			{
				// Held open, so the controls go away while the stop is still on
				// its way: an acknowledgement that resolves at once would let the
				// resume go out before the covering route ever removed them, and
				// the count of one would pass without exercising the retry.
				forceStop: () => {
					forceStops += 1;
					return new Promise<void>((resolve) => {
						acknowledge = resolve;
					});
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
		const hook = renderHook(() => useSessionRestart("hub-a", "ref-a", live, "open"));
		let restarting!: Promise<void>;
		act(() => {
			restarting = hook.result.current.restart();
		});
		await settle();
		expect(forceStops).toBe(1);
		// Covered while the stop is still out, so no resume can have gone yet.
		live = null;
		hook.rerender();
		await settle();
		expect(sent).toBe(0);
		// The hub now takes the stop. With the controls gone there is nothing to
		// send the resume with, so it stays owed rather than going out against a
		// binding this screen no longer holds. (The retry that carries it once
		// the same controls are refused again is the read test's subject: what
		// this one fixes is the ordering, that no resume can precede the
		// acknowledgement.)
		act(() => acknowledge());
		await restarting;
		await settle();
		expect(sent).toBe(0);
		// Coming back brings fresh controls; the resume is still owed, and the
		// daemon is stopped until it goes.
		live = made;
		hook.rerender();
		await settle();
		expect(sent).toBe(1);
	});

	// The stop is the hub's, and it can be acknowledged after the screen that
	// asked for it is gone: closing a conversation does not cancel a stop the
	// hub is already taking. The owed resume lived in the unmounted hook's own
	// state, so that acknowledgement had nowhere to land - reopening the session
	// mounted a fresh hook in "idle" and the daemon stayed stopped with the
	// restart unfinished. The obligation belongs to the session, so a hook
	// mounting for a session that still owes a resume starts by sending it.
	it("sends a resume its stop owed after the screen closed", async () => {
		let sent = 0;
		let acknowledge = () => {};
		const stopping = new Promise<void>((resolve) => {
			acknowledge = resolve;
		});
		const made = new SessionControls(
			{
				forceStop: async () => {
					await stopping;
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
		const first = renderHook(() => useSessionRestart("hub-b", "ref-b", made, "open"));
		await act(async () => {
			void first.result.current.restart();
			await settle();
		});
		// The reader closes the conversation while the stop is still out.
		first.unmount();
		act(() => acknowledge());
		await settle();
		expect(sent).toBe(0);

		// Reopening the session mounts a fresh hook with no memory of the stop.
		// The resume is still owed, and the daemon is stopped until it goes.
		renderHook(() => useSessionRestart("hub-b", "ref-b", made, "open"));
		await settle();
		expect(sent).toBe(1);
	});

	// The stop is the hub's, and the reader can close and reopen the conversation
	// around it. Both sides of that race are one question: who sends the resume.
	// A reopen before the acknowledgement must pick the obligation up when it
	// lands, and a reopen during dispatch must join that attempt rather than
	// start a second resume on the same socket.
	it("sends the resume a reopen is still waiting for when the stop is acknowledged", async () => {
		let sent = 0;
		let acknowledge = () => {};
		const stopping = new Promise<void>((resolve) => {
			acknowledge = resolve;
		});
		const made = new SessionControls(
			{
				forceStop: async () => {
					await stopping;
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
		const first = renderHook(() => useSessionRestart("hub-c", "ref-c", made, "open"));
		await act(async () => {
			void first.result.current.restart();
			await settle();
		});
		// The reader closes the conversation while the stop is still out, and
		// reopens it before the hub answers.
		first.unmount();
		const reopened = renderHook(() => useSessionRestart("hub-c", "ref-c", made, "open"));
		await settle();
		expect(sent).toBe(0);

		// The acknowledgement arrives at a session whose screen is already open
		// again. It must be picked up there, not lost with the view that asked.
		await act(async () => {
			acknowledge();
		});
		await settle();
		expect(sent).toBe(1);
		reopened.unmount();
	});

	it("joins an owed resume already in flight instead of sending a second", async () => {
		let sent = 0;
		let answer!: () => void;
		const answered = new Promise<void>((resolve) => {
			answer = resolve;
		});
		const made = new SessionControls(
			{
				forceStop: async () => {},
				resume: async () => {
					sent += 1;
					await answered;
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		const first = renderHook(() => useSessionRestart("hub-d", "ref-d", made, "open"));
		await act(async () => {
			void first.result.current.restart();
		});
		await settle();
		// The resume is out and unanswered when the reader closes and reopens,
		// and the controls that sent it stop reporting it pending on the way out.
		// Nothing about the in-flight attempt itself changes.
		expect(sent).toBe(1);
		first.unmount();
		// A reopen builds fresh controls for the same session, which can send.
		// Only the claim that the in-flight attempt holds stands between them and
		// a second resume on the same socket.
		const replacement = new SessionControls(
			{
				forceStop: async () => {},
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
		const reopened = renderHook(() => useSessionRestart("hub-d", "ref-d", replacement, "open"));
		await settle();
		// The second hook joins the attempt rather than recycling the socket
		// again for a resume that is already on its way.
		expect(sent).toBe(1);
		await act(async () => {
			answer();
		});
		await settle();
		expect(sent).toBe(1);
		reopened.unmount();
	});
});

// A reopen during another hook's dispatch joins that attempt, and the join has
// to end when the attempt does. The hook that waited has no pending state of
// its own to clear and no record change it caused, so the obligation settling
// is the only signal it gets - and a hook still reading "resuming" there keeps
// its Restart disabled after a refusal the reader is being told about, which
// leaves a stopped daemon and no way to ask again.
describe("a hook that joined another's attempt", () => {
	it("leaves its busy state when the attempt it joined settles", async () => {
		let sent = 0;
		let answer!: () => void;
		const answered = new Promise<void>((resolve) => {
			answer = resolve;
		});
		const made = new SessionControls(
			{
				forceStop: async () => {},
				resume: async () => {
					sent += 1;
					await answered;
				},
			} as unknown as ConstructorParameters<typeof SessionControls>[0],
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		const first = renderHook(() => useSessionRestart("hub-e", "ref-e", made, "open"));
		await act(async () => {
			void first.result.current.restart();
		});
		await settle();
		expect(sent).toBe(1);
		// The reader reopens while the resume is still out. This hook joins the
		// attempt instead of sending a second resume, and reports itself busy
		// because the restore is unfinished.
		first.unmount();
		const reopened = renderHook(() => useSessionRestart("hub-e", "ref-e", made, "open"));
		await settle();
		expect(sent).toBe(1);
		expect(reopened.result.current.busy).toBe(true);
		// The hub refuses. The attempt is settled and nothing is owed, so the
		// hook that was waiting for it has nothing left to wait for.
		await act(async () => {
			answer();
		});
		await settle();
		expect(reopened.result.current.busy).toBe(false);
		reopened.unmount();
	});
});
