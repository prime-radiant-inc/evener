// How one step of a restart went, read off a real SessionControls whose
// service is a fake: whether the request went out at all is what decides
// whether a resume is owed.
import { describe, expect, it } from "vitest";
import { SessionControls } from "../sessionControls";
import { attemptControl } from "./sessionRestart";

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
});
