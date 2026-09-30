// What this phone remembers about starting sessions on one hub: the setups
// sessions were started with, so New session opens on the newest one (spec
// 11). It is per hub, because a setup names that hub's hosts and folders.
import { isPlainObject, type LaunchConfigLayer } from "@evener/appwire-client";
import { readJson, removeKeys } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";
import { type LaunchSetup, ownedOverrides, type RememberedSetup } from "./launchSetup";

export const historyKey = (hubId: string) => `evener.native.launch-history.${hubId}`;

/** Starts remembered per hub: one per host and project, enough for every
 * project in use (the spec's fleet has 14) and small enough to rewrite on
 * every start. */
export const HISTORY_LIMIT = 50;

function record(value: unknown): Record<string, unknown> | null {
	return isPlainObject(value) ? value : null;
}

/** A stored setup this build can read, or null. Unknown override fields are
 * dropped: only what the sheet owns comes back. */
function toSetup(value: unknown): LaunchSetup | null {
	const setup = record(value);
	const overrides = record(setup?.overrides);
	const model = setup?.model === null ? null : record(setup?.model);
	if (
		!setup ||
		!overrides ||
		typeof setup.host !== "string" ||
		typeof setup.cwd !== "string" ||
		typeof setup.effort !== "string" ||
		(setup.model !== null && (!model || typeof model.provider !== "string" || typeof model.model !== "string"))
	)
		return null;
	const plugins = overrides.enabledPlugins;
	if (plugins !== undefined && !(Array.isArray(plugins) && plugins.every((name) => typeof name === "string")))
		return null;
	for (const [field, kind] of [
		["sandbox", "string"],
		["sandboxNet", "boolean"],
		["contextStrategy", "string"],
		["maxSubagentDepth", "number"],
		["maxRounds", "number"],
	] as const)
		if (overrides[field] !== undefined && typeof overrides[field] !== kind) return null;
	return {
		host: setup.host,
		cwd: setup.cwd,
		model: model ? { provider: model.provider as string, model: model.model as string } : null,
		effort: setup.effort,
		// Every owned field was type-checked above; ownedOverrides keeps only them.
		overrides: ownedOverrides(overrides as LaunchConfigLayer),
	};
}

function toRemembered(value: unknown): RememberedSetup | null {
	const entry = record(value);
	const setup = toSetup(entry?.setup);
	return entry && setup && typeof entry.at === "number" ? { setup, at: entry.at } : null;
}

/** A stored list's readable entries. A list the phone can't read, or that
 * doesn't parse, reads as empty (deviceStorage's readJson). */
function parseList<T>(value: unknown, item: (value: unknown) => T | null): T[] {
	if (!Array.isArray(value)) return [];
	return value.flatMap((entry) => {
		const parsed = item(entry);
		return parsed ? [parsed] : [];
	});
}

export class LaunchMemory {
	private historyList: RememberedSetup[];

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		this.historyList = parseList(readJson(storage, historyKey(hubId)), toRemembered);
	}

	history(): readonly RememberedSetup[] {
		return this.historyList;
	}

	/** Remembers a start: it replaces older starts for the same
	 * host and project, and the oldest fall off past the limit. */
	recordStart(setup: LaunchSetup, at: number): void {
		const others = this.historyList.filter((entry) => entry.setup.host !== setup.host || entry.setup.cwd !== setup.cwd);
		this.writeHistory([{ setup, at }, ...others].slice(0, HISTORY_LIMIT));
	}

	/** Stores first, so a failed write leaves this memory as it was. */
	private writeHistory(next: RememberedSetup[]): void {
		this.storage.setItemSync(historyKey(this.hubId), JSON.stringify(next));
		this.historyList = next;
	}
}

/** Removes a hub's remembered starts (deviceStorage's removeKeys reports a
 * storage that won't let go of them). */
export function forgetLaunchMemory(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [historyKey(hubId)]);
}
