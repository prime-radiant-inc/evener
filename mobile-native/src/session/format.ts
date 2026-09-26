// The Session's compact durations (spec 5): 40s, 12m, 3h, 2d. The package's
// formatElapsed reads "12m03s" and relativeAge says "now" under a minute, so
// neither reads the way the spec's copy does.

export function compactDuration(ms: number): string {
	const seconds = Math.max(0, Math.floor(ms / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours}h`;
	return `${Math.floor(hours / 24)}d`;
}
