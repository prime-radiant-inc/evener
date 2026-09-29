import type { HubNotice, Source } from "@evener/appwire-client";
import { plural } from "./attention";

/** A hub-level problem the Board shows under its chips (spec 7.1). The
 * subject's id routes the action; the text is only for reading. */
export type Notice = { key: string; text: string } & (
	| { kind: "signIn"; action: "Sign in"; providerId: string }
	| { kind: "host"; action: "Details"; sourceId: string }
	| { kind: "plugin"; action: "Plugins"; pluginId: string; marketplace: string }
);

/** The Board's notices, as the hub derives them (S11: evener/notices/list and
 * evener/notices/changed), in the hub's order: sign-ins, then hosts, then
 * plugins. Each keeps the hub's id as its key, so an alert fires once for a
 * new notice and not again when its count moves (spec 13.3).
 *
 * A notice names the sessions it blocks when the hub counts them, as
 * " · N sessions". A host goes by its label from the manifest's sources,
 * or by its id before the manifest lists it; two broken plugins sharing a
 * name each name their marketplace. A kind this phone doesn't know is left
 * out. */
export function notices(input: { hubNotices: readonly HubNotice[]; sources: readonly Source[] }): Notice[] {
	const withCount = (sentence: string, notice: HubNotice) =>
		notice.affectedSessions ? `${sentence} · ${plural(notice.affectedSessions, "session")}` : sentence;
	const brokenPlugins = input.hubNotices.filter((notice) => notice.kind === "pluginBroken");
	return input.hubNotices.flatMap((notice): Notice[] => {
		const key = notice.id;
		switch (notice.kind) {
			case "signInRequired":
				return [
					{
						key,
						kind: "signIn",
						text: withCount(`${notice.subject} sign-in expired`, notice),
						action: "Sign in",
						providerId: notice.subject,
					},
				];
			case "hostOffline": {
				const label = input.sources.find((source) => source.id === notice.subject)?.label ?? notice.subject;
				return [
					{
						key,
						kind: "host",
						text: withCount(`${label} is offline`, notice),
						action: "Details",
						sourceId: notice.subject,
					},
				];
			}
			case "pluginBroken": {
				const marketplace = notice.marketplace ?? "";
				const shared = brokenPlugins.filter((other) => other.subject === notice.subject).length > 1;
				return [
					{
						key,
						kind: "plugin",
						text: `${shared ? `${notice.subject} from ${marketplace}` : notice.subject} is broken`,
						action: "Plugins",
						pluginId: notice.subject,
						marketplace,
					},
				];
			}
			default:
				return [];
		}
	});
}
