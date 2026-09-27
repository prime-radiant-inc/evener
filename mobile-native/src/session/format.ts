// The Session's compact numbers (spec 5): durations as 40s, 12m, 3h, 2d and
// counts as 1.2K, 39.8K, 412K, 46M. The package's formatTokenCount stops at
// "k" and relativeAge says "now" under a minute, so neither reads the way the
// spec's copy does.

export function compactDuration(ms: number): string {
	const seconds = Math.max(0, Math.floor(ms / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours}h`;
	return `${Math.floor(hours / 24)}d`;
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
