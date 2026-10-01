import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
// The scripted hub the Board's navigation reads meet (boardData.ts), shared
// by the suites that drive a board controller: each request waits until the
// test answers it, by reader, in the order asked.
import { WireError } from "@evener/appwire-client";
import type {
	AnyNotification,
	HubNotice,
	NavigationInvalidationTarget,
	NavigationReadParams,
	NavigationReadResponse,
} from "@evener/appwire-client";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export function boundary() {
	const requests: Array<{
		method: string;
		params: NavigationReadParams;
		resolve: (value: NavigationReadResponse) => void;
		reject: (error: Error) => void;
		answered: boolean;
	}> = [];
	const listeners = new Set<(event: AnyNotification) => void>();
	const client: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		request: (method, params) =>
			new Promise((resolve, reject) => {
				requests.push({
					method,
					params: params as NavigationReadParams,
					resolve,
					reject,
					answered: false,
				});
			}),
		onNotification: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
	return { client, requests, listeners };
}
export type Hub = ReturnType<typeof boundary>;
export function response(params: NavigationReadParams, data: unknown, revision = 1) {
	return wireSnapshot(
		{
			...params,
			representationVersion: 3,
			offset: params.offset ?? 0,
			limit: params.limit ?? 50,
		},
		data,
		`etag-${params.offset ?? 0}-${revision}`,
		revision,
		"generation-test",
	);
}
export const session = (ref: string) => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "p",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
});
export const sessions = (prefix: string, count: number, from = 0) =>
	Array.from({ length: count }, (_, index) => session(`${prefix}${from + index}`));
export const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

/** Each category's reads are a reader of their own, named by its id. */
export type Reader = "live" | "needs_you" | "pin_catalog" | "manifest" | `pin_section:${string}` | "notices";
export const readerOf = ({ method, params }: Hub["requests"][number]): Reader =>
	method === "evener/notices/list"
		? "notices"
		: params.resource === "section"
			? (params.section as Reader)
			: params.resource === "pin_section"
				? `pin_section:${params.sectionId}`
				: (params.resource as Reader);
/** The oldest unanswered request for one reader. */
export function next(hub: Hub, reader: Reader) {
	const request = hub.requests.find((candidate) => !candidate.answered && readerOf(candidate) === reader);
	if (!request) throw new Error(`no pending ${reader} request`);
	request.answered = true;
	return request;
}
export function answer(hub: Hub, reader: Reader, data: unknown, revision = 1) {
	const request = next(hub, reader);
	request.resolve(response(request.params, data, revision));
	return request;
}
export function fail(hub: Hub, reader: Reader, message: string) {
	next(hub, reader).reject(new Error(message));
}
export const requestsFor = (hub: Hub, reader: Reader) => hub.requests.filter((request) => readerOf(request) === reader);
export function invalidate(
	hub: Hub,
	sequence: number,
	targets: NavigationInvalidationTarget[],
	generationId = "generation-test",
) {
	for (const listener of hub.listeners)
		listener({
			method: "evener/navigation/invalidated",
			params: { generationId, sequence, targets },
		});
}
/** A hub notice (S11) as evener/notices/list and evener/notices/changed
 * carry it (cmd/evener-hub/app_notices.go): "<kind>:<subject>", and
 * "<kind>:<plugin>@<marketplace>" with its marketplace for a broken plugin;
 * its count only when the hub has one. */
export const hubNotice = (kind: string, subject: string, affectedSessions?: number): HubNotice =>
	kind === "pluginBroken"
		? { id: `${kind}:${subject}@evener`, kind, subject, marketplace: "evener" }
		: { id: `${kind}:${subject}`, kind, subject, ...(affectedSessions ? { affectedSessions } : {}) };
export function answerNotices(hub: Hub, notices: HubNotice[]) {
	next(hub, "notices").resolve({ notices } as never);
}
/** An older hub, which has no evener/notices/list. */
export function noticesMethodNotFound(hub: Hub) {
	next(hub, "notices").reject(new WireError("method not found: evener/notices/list", -32601));
}
export function noticesChanged(hub: Hub, notices: HubNotice[]) {
	for (const listener of hub.listeners)
		listener({ method: "evener/notices/changed", params: { notices } } as AnyNotification);
}
