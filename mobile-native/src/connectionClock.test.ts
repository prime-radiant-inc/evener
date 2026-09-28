import { describe, expect, it } from "vitest";
import { ConnectionClock } from "./connectionClock";

const at = (seconds: number) => 1_000_000 + seconds * 1000;
const hub = (live: boolean, foreground = true, hubId: string | null = "hub-a") => ({ hubId, live, foreground });

describe("the connection clock (spec 14)", () => {
	it("starts down at launch, with nothing live yet", () => {
		const clock = new ConnectionClock();
		expect(clock.observe(hub(false), at(0))).toEqual({ downSince: at(0), lastLiveAt: null });
	});

	it("is not down while live, and remembers when the connection dropped", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(false), at(0));
		expect(clock.observe(hub(true), at(1))).toEqual({ downSince: null, lastLiveAt: null });
		expect(clock.observe(hub(false), at(90))).toEqual({ downSince: at(90), lastLiveAt: at(90) });
	});

	it("keeps counting from the moment it went down", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		clock.observe(hub(false), at(10));
		expect(clock.observe(hub(false), at(40))).toEqual({ downSince: at(10), lastLiveAt: at(10) });
	});

	it("never counts time in the background, but the data's age does", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		expect(clock.observe(hub(false, false), at(5))).toEqual({ downSince: null, lastLiveAt: at(5) });
		expect(clock.observe(hub(false, true), at(3600))).toEqual({ downSince: at(3600), lastLiveAt: at(5) });
	});

	it("dates the data from leaving the front, even if the connection closes later", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		expect(clock.observe(hub(true, false), at(5))).toEqual({ downSince: null, lastLiveAt: at(5) });
		expect(clock.observe(hub(false, true), at(3600))).toEqual({ downSince: at(3600), lastLiveAt: at(5) });
	});

	it("is never down with no hub chosen, and starts fresh once one is", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		expect(clock.observe(hub(false, true, null), at(10))).toEqual({ downSince: null, lastLiveAt: null });
		expect(clock.observe(hub(false, true, null), at(50))).toEqual({ downSince: null, lastLiveAt: null });
		expect(clock.observe(hub(false, true, "hub-b"), at(60))).toEqual({ downSince: at(60), lastLiveAt: null });
	});

	it("starts over for a different hub", () => {
		const clock = new ConnectionClock();
		clock.observe(hub(true), at(0));
		clock.observe(hub(false), at(10));
		expect(clock.observe(hub(false, true, "hub-b"), at(20))).toEqual({ downSince: at(20), lastLiveAt: null });
	});
});
