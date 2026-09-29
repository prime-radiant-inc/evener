// The Session's compact durations (spec 5): 40s, 12m, 3h, 2d. relativeAge
// says "now" under a minute, so it doesn't read the way the spec's copy does.
// Token counts use the package's formatTokenCount. Paths and URLs wrap only at
// their slashes.

export type DurationUnit = "second" | "minute" | "hour" | "day";

export const DURATION_UNIT_MS: Record<DurationUnit, number> = {
	second: 1_000,
	minute: 60_000,
	hour: 3_600_000,
	day: 86_400_000,
};

/** A duration in its one largest whole unit, rounded down. */
export function durationIn(ms: number): { count: number; unit: DurationUnit } {
	const seconds = Number.isFinite(ms) ? Math.max(0, Math.floor(ms / 1000)) : 0;
	if (seconds < 60) return { count: seconds, unit: "second" };
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return { count: minutes, unit: "minute" };
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return { count: hours, unit: "hour" };
	return { count: Math.floor(hours / 24), unit: "day" };
}

export function compactDuration(ms: number): string {
	const { count, unit } = durationIn(ms);
	return `${count}${unit[0]}`;
}

/** The same duration as VoiceOver should read it: "3 minutes", "1 day". */
export function spokenDuration(ms: number): string {
	const { count, unit } = durationIn(ms);
	return `${count} ${unit}${count === 1 ? "" : "s"}`;
}

/** A path or URL may wrap only after a slash: a zero-width space follows each
 * one. */
export const wrapAfterSlashes = (path: string) => path.replace(/\//g, "/​");
