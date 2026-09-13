import type {
	SettingsOverviewResponse,
	UpgradeResponse,
} from "../../appwire-client/typescript/types.gen";

const requiredResponse = [
	"release",
	"channel",
	"url",
	"archive",
	"prefix",
	"binDir",
	"shareBinDir",
	"restartMessage",
] as const;
const nonblank = (value: unknown): value is string =>
	typeof value === "string" && value.trim() !== "";
export function isValidUpgradeResponse(
	value: unknown,
): value is UpgradeResponse {
	if (!value || typeof value !== "object") return false;
	const candidate = value as Record<string, unknown>;
	return (
		requiredResponse.every((key) => nonblank(candidate[key])) &&
		Array.isArray(candidate.installed) &&
		candidate.installed.length > 0 &&
		candidate.installed.every(nonblank)
	);
}
export function isValidRunningOverview(
	value: unknown,
): value is SettingsOverviewResponse {
	if (!value || typeof value !== "object") return false;
	const hub = (value as Record<string, unknown>).hub;
	if (!hub || typeof hub !== "object") return false;
	const identity = hub as Record<string, unknown>;
	return (
		nonblank(identity.version) &&
		(identity.commit === undefined || nonblank(identity.commit))
	);
}
