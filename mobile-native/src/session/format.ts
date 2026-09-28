// The Session's compact numbers (spec 5): durations as 40s, 12m, 3h, 2d and
// counts as 1.2K, 39.8K, 412K, 46M. The package's formatTokenCount stops at
// "k" and relativeAge says "now" under a minute, so neither reads the way the
// spec's copy does.

type DurationUnit = "second" | "minute" | "hour" | "day";

/** A duration in its one largest whole unit, rounded down. */
function durationIn(ms: number): { count: number; unit: DurationUnit } {
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

const UNITS = [
	["K", 1_000],
	["M", 1_000_000],
] as const;

export function compactCount(n: number): string {
	const value = Number.isFinite(n) && n > 0 ? Math.round(n) : 0;
	if (value < 1000) return String(value);
	for (const [unit, size] of UNITS) {
		const scaled = value / size;
		// One decimal below 100 of the unit, none from 100 up: 1.2K, 39.8K, 412K.
		const text = (scaled < 100 ? scaled.toFixed(1) : scaled.toFixed(0)).replace(/\.0$/, "");
		// 999,950 rounds to "1000K"; it reads better as the next unit.
		if (Number(text) < 1000 || unit === "M") return `${text}${unit}`;
	}
	return String(value);
}
