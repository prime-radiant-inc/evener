import type { AuthStatusResponse, NavigationSessionSummary, PluginEntry, Source } from "@evener/appwire-client";
import { plural } from "./attention";

/** A hub-level problem the Board shows under its chips (spec 7.1). The
 * subject's id routes the action; the text is only for reading. */
export type Notice = { key: string; text: string } & (
	| { kind: "signIn"; action: "Sign in"; providerId: string }
	| { kind: "host"; action: "Details"; sourceId: string }
	| { kind: "plugin"; action: "Plugins"; pluginId: string }
);

/** The Board's notices: every provider whose sign-in expired, every offline
 * host and every broken plugin, in that order.
 *
 * An offline host counts the sessions the Board has loaded that run on it,
 * each once however many sections loaded it. The hub sends no per-host
 * count, so a session on a page not yet loaded goes uncounted: the count can
 * fall short but never runs over, until a hub rollup (S11) replaces it. */
export function notices(input: {
	auth: AuthStatusResponse[];
	sources: Source[];
	plugins: PluginEntry[];
	loadedRows: readonly NavigationSessionSummary[];
}): Notice[] {
	const result: Notice[] = [];
	for (const { provider, needsLogin } of input.auth)
		if (needsLogin)
			result.push({
				key: `signIn:${provider}`,
				kind: "signIn",
				text: `${provider} sign-in expired`,
				action: "Sign in",
				providerId: provider,
			});
	for (const source of input.sources) {
		if (source.online) continue;
		const refs = new Set(input.loadedRows.filter((row) => row.host_id === source.id).map((row) => row.ref));
		result.push({
			key: `host:${source.id}`,
			kind: "host",
			text: refs.size ? `${source.label} is offline · ${plural(refs.size, "session")}` : `${source.label} is offline`,
			action: "Details",
			sourceId: source.id,
		});
	}
	// Two marketplaces can each ship a plugin of the same name: such plugins
	// name their marketplace too.
	const broken = input.plugins.filter((entry) => entry.broken);
	for (const { plugin, marketplace } of broken) {
		const shared = broken.filter((other) => other.plugin === plugin).length > 1;
		result.push({
			key: `plugin:${plugin}@${marketplace}`,
			kind: "plugin",
			text: `${shared ? `${plugin} from ${marketplace}` : plugin} is broken`,
			action: "Plugins",
			pluginId: plugin,
		});
	}
	return result;
}
