// The pulse meter's geometry (spec 16.4): seven one-minute bars, newest on
// the right, on one fixed fleet-wide log scale so two meters compare and a
// trickle never looks like a flood.

/** Events per minute at which a bar reaches full height. */
export const PULSE_FULL_SCALE = 64;
export const PULSE_BARS = 7;

/** Until the hub reports per-minute activity (S5), a working session shows a
 * single bar in its newest minute: the spec's fallback. */
export const WORKING_WITHOUT_ACTIVITY: readonly number[] = [0, 0, 0, 0, 0, 0, 8];

export interface PulseBar {
	/** 0 to 1 of the meter's height; the meter always draws a 1pt baseline. */
	height: number;
	opacity: number;
}

export function pulseBars(perMinute: readonly number[] = WORKING_WITHOUT_ACTIVITY): PulseBar[] {
	const recent = perMinute.slice(-PULSE_BARS);
	const minutes = [...Array<number>(PULSE_BARS - recent.length).fill(0), ...recent];
	return minutes.map((events, index) => ({
		height: events <= 0 ? 0 : Math.min(1, Math.log2(1 + events) / Math.log2(1 + PULSE_FULL_SCALE)),
		opacity: 0.35 + (0.65 * index) / (PULSE_BARS - 1),
	}));
}
