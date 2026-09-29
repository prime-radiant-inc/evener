import type { AuthStatusResponse, NavigationSessionSummary, PluginEntry, Source } from "@evener/appwire-client";
import { plural } from "./attention";

/** A hub-level problem the Board shows under its chips (spec 7.1). The
 * subject's id routes the action; the text is only for reading. */
export type Notice = { key: string; text: string } & (
	| { kind: "signIn"; action: "Sign in"; providerId: string }
	| { kind: "host"; action: "Details"; sourceId: string }
	| { kind: "plugin"; action: "Plugins"; pluginId: string; marketplace: string }
);

/** Whether `row` failed because `provider` needs a sign-in: the hub's
 * signInRequired cause, or the provider refusing it with a 401. A rate limit
 * or any other failure from the same provider isn't one. */
function signInFailure(row: NavigationSessionSummary, provider: string): boolean {
	const failure = row.failure;
	return failure?.provider === provider && (failure.cause_kind === "signInRequired" || failure.status === 401);
}

/** The Board's notices: every provider whose sign-in expired, every offline
 * host and every broken plugin, in that order.
 *
 * Each names the sessions it affects that the Board has loaded, each once
 * however many sections loaded it: for an expired sign-in, the rows that
 * failed on that provider's sign-in (signInFailure); for an offline host,
 * the rows running on it. The hub sends no such counts, so a session on a page not yet loaded
 * goes uncounted: a count can fall short but never runs over, until a hub
 * rollup (S11) replaces it. */
export function notices(input: {
	auth: AuthStatusResponse[];
	sources: Source[];
	plugins: PluginEntry[];
	loadedRows: readonly NavigationSessionSummary[];
}): Notice[] {
	const result: Notice[] = [];
	/** "<sentence> · N sessions" for the loaded rows `affects` picks, counted
	 * once each; the sentence alone when it picks none. */
	const withCount = (sentence: string, affects: (row: NavigationSessionSummary) => boolean) => {
		const refs = new Set(input.loadedRows.filter(affects).map((row) => row.ref));
		return refs.size ? `${sentence} · ${plural(refs.size, "session")}` : sentence;
	};
	for (const { provider, needsLogin } of input.auth)
		if (needsLogin)
			result.push({
				key: `signIn:${provider}`,
				kind: "signIn",
				text: withCount(`${provider} sign-in expired`, (row) => signInFailure(row, provider)),
				action: "Sign in",
				providerId: provider,
			});
	for (const source of input.sources) {
		if (source.online) continue;
		result.push({
			key: `host:${source.id}`,
			kind: "host",
			text: withCount(`${source.label} is offline`, (row) => row.host_id === source.id),
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
			marketplace,
		});
	}
	return result;
}
