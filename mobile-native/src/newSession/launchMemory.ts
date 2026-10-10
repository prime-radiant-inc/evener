// What this phone remembers about starting sessions on one hub: the setup the
// newest session was started with, so New session opens on it (spec 11). It is
// per hub, because a setup names that hub's hosts and folders.
import { isPlainObject, type LaunchConfigLayer } from "@evener/appwire-client";
import { readJson, removeKeys } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";
import { type LaunchSetup, ownedOverrides } from "./launchSetup";

export const lastSetupKey = (hubId: string) => `evener.native.launch-setup.${hubId}`;

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

export class LaunchMemory {
	private last: LaunchSetup | null;

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		this.last = toSetup(readJson(storage, lastSetupKey(hubId)));
	}

	/** The setup the newest start used, or null when nothing was started here. */
	lastSetup(): LaunchSetup | null {
		return this.last;
	}

	/** Remembers a start. Stores first, so a failed write leaves this memory
	 * as it was. */
	recordStart(setup: LaunchSetup): void {
		this.storage.setItemSync(lastSetupKey(this.hubId), JSON.stringify(setup));
		this.last = setup;
	}
}

/** Removes a hub's remembered start (deviceStorage's removeKeys reports a
 * storage that won't let go of it). */
export function forgetLaunchMemory(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [lastSetupKey(hubId)]);
}
