import { decodeForkTarget, type ForkTarget } from "./forkCheckpointRepository";
import { localSessionId } from "./sessionDeletionResult";

export interface SavedLocation {
	hubId: string;
	conversation?: { ref: string; title: string };
	pinAssignment?: true;
	deleteSession?: true;
	fork?: ForkTarget;
	pinned?: { section?: { id: string; title: string }; manage?: true };
}
interface Storage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}
const key = "evener.last-location";
function object(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}
function conversation(value: unknown): value is { ref: string; title: string } {
	return (
		object(value) &&
		typeof value.ref === "string" &&
		value.ref.length > 0 &&
		typeof value.title === "string"
	);
}
function pinned(value: unknown): value is NonNullable<SavedLocation["pinned"]> {
	if (!object(value)) return false;
	if (
		value.section !== undefined &&
		(!object(value.section) ||
			typeof value.section.id !== "string" ||
			!value.section.id.trim() ||
			typeof value.section.title !== "string")
	)
		return false;
	return (
		value.manage === undefined ||
		(value.manage === true && value.section !== undefined)
	);
}
function fork(value: unknown): ForkTarget | null {
	try {
		return decodeForkTarget(value);
	} catch {
		return null;
	}
}
export class LocationRepository {
	constructor(private readonly storage: Storage) {}
	read(savedHubIds: readonly string[]): SavedLocation | null {
		const raw = this.storage.getItemSync(key);
		if (!raw) return null;
		let value: unknown;
		try {
			value = JSON.parse(raw);
		} catch {
			return null;
		}
		if (
			!object(value) ||
			typeof value.hubId !== "string" ||
			!savedHubIds.includes(value.hubId)
		)
			return null;
		if (value.conversation !== undefined && !conversation(value.conversation))
			return null;
		const target = fork(value.fork);
		if (
			value.deleteSession !== undefined &&
			(value.deleteSession !== true ||
				!conversation(value.conversation) ||
				!localSessionId(value.conversation.ref) ||
				value.fork !== undefined ||
				value.pinAssignment !== undefined ||
				value.pinned !== undefined)
		)
			return null;
		if (
			value.fork !== undefined &&
			(!target ||
				!conversation(value.conversation) ||
				value.pinned !== undefined ||
				value.pinAssignment !== undefined)
		)
			return null;
		if (
			value.pinned !== undefined &&
			(!pinned(value.pinned) ||
				value.conversation !== undefined ||
				value.pinAssignment !== undefined)
		)
			return null;
		if (
			value.pinAssignment !== undefined &&
			(value.pinAssignment !== true || !conversation(value.conversation))
		)
			return null;
		return {
			hubId: value.hubId,
			...(target ? { fork: target } : {}),
			...(pinned(value.pinned) ? { pinned: value.pinned } : {}),
			...(value.pinAssignment === true ? { pinAssignment: true as const } : {}),
			...(value.deleteSession === true ? { deleteSession: true as const } : {}),
			...(conversation(value.conversation)
				? {
						conversation: {
							ref: value.conversation.ref,
							title: value.conversation.title,
						},
					}
				: {}),
		};
	}
	save(location: SavedLocation | null) {
		if (location) this.storage.setItemSync(key, JSON.stringify(location));
		else this.storage.removeItemSync(key);
	}
}
export function locationForRoute(
	route: { name: string; params?: unknown },
	hubId: string | null,
): SavedLocation | null {
	if (!hubId || route.name === "Hubs") return null;
	if (route.name === "Fork") {
		if (!object(route.params) || route.params.hubId !== hubId) return null;
		const target = fork({
			instanceId: route.params.instanceId,
			entryIndex: route.params.entryIndex,
			preview: route.params.preview,
		});
		if (!conversation(route.params)) return null;
		return target
			? {
					hubId,
					conversation: { ref: route.params.ref, title: route.params.title },
					fork: target,
				}
			: null;
	}
	if (
		route.name === "PinSections" ||
		route.name === "PinnedSection" ||
		route.name === "PinSectionEditor"
	) {
		if (!object(route.params) || route.params.hubId !== hubId) return null;
		if (route.name === "PinSections") return { hubId, pinned: {} };
		const destination = {
			section: { id: route.params.sectionId, title: route.params.title },
			...(route.name === "PinSectionEditor" ? { manage: true as const } : {}),
		};
		return pinned(destination) ? { hubId, pinned: destination } : null;
	}
	if (
		route.name === "Conversation" ||
		route.name === "PinAssignment" ||
		route.name === "SessionDeletion"
	) {
		if (
			!object(route.params) ||
			route.params.hubId !== hubId ||
			!conversation(route.params)
		)
			return null;
		if (route.name === "SessionDeletion" && !localSessionId(route.params.ref))
			return null;
		return {
			hubId,
			conversation: { ref: route.params.ref, title: route.params.title },
			...(route.name === "PinAssignment"
				? { pinAssignment: true as const }
				: {}),
			...(route.name === "SessionDeletion"
				? { deleteSession: true as const }
				: {}),
		};
	}
	return { hubId };
}
export function restoredStack(location: SavedLocation | null) {
	const routes: {
		name: string;
		params?: {
			hubId: string;
			ref?: string;
			sectionId?: string;
			title?: string;
			instanceId?: string;
			entryIndex?: number;
			preview?: string;
		};
	}[] = [{ name: "Hubs" }];
	if (location) routes.push({ name: "Sessions" });
	if (location?.pinned) {
		routes.push({ name: "PinSections", params: { hubId: location.hubId } });
		if (location.pinned.section) {
			const params = {
				hubId: location.hubId,
				sectionId: location.pinned.section.id,
				title: location.pinned.section.title,
			};
			routes.push({ name: "PinnedSection", params });
			if (location.pinned.manage)
				routes.push({ name: "PinSectionEditor", params });
		}
	}
	if (location?.conversation)
		routes.push({
			name: "Conversation",
			params: { hubId: location.hubId, ...location.conversation },
		});
	if (location?.pinAssignment && location.conversation)
		routes.push({
			name: "PinAssignment",
			params: { hubId: location.hubId, ...location.conversation },
		});
	if (location?.fork && location.conversation)
		routes.push({
			name: "Fork",
			params: {
				hubId: location.hubId,
				...location.conversation,
				...location.fork,
			},
		});
	if (location?.deleteSession && location.conversation)
		routes.push({
			name: "SessionDeletion",
			params: { hubId: location.hubId, ...location.conversation },
		});
	return { index: routes.length - 1, routes };
}
