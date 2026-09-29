import { isPlainObject } from "@evener/appwire-client";
import { decodeForkTarget, type ForkTarget } from "./forkCheckpointRepository";
import { localSessionId } from "./sessionDeletionResult";
import { isSheetRoute } from "./sheet/sheetRoutes";
import type { SyncStringStorage } from "./syncStringStorage";

export interface SavedLocation {
	hubId: string;
	conversation?: { ref: string; title: string };
	reader?: { sessionRef: string; path: string; updatedAt?: string };
	pinAssignment?: true;
	deleteSession?: true;
	fork?: ForkTarget;
	pinned?: { section?: { id: string; title: string }; manage?: true };
	projects?: {
		archived: boolean;
		project?: {
			key: string;
			title: string;
			tier: "current" | "recent" | "archived";
		};
	};
}
const key = "evener.last-location";
function conversation(value: unknown): value is { ref: string; title: string } {
	return (
		isPlainObject(value) && typeof value.ref === "string" && value.ref.length > 0 && typeof value.title === "string"
	);
}
function reader(value: unknown): value is NonNullable<SavedLocation["reader"]> {
	return (
		isPlainObject(value) &&
		typeof value.sessionRef === "string" &&
		value.sessionRef.length > 0 &&
		typeof value.path === "string" &&
		value.path.length > 0 &&
		(value.updatedAt === undefined || typeof value.updatedAt === "string")
	);
}
function pinned(value: unknown): value is NonNullable<SavedLocation["pinned"]> {
	if (!isPlainObject(value)) return false;
	if (
		value.section !== undefined &&
		(!isPlainObject(value.section) ||
			typeof value.section.id !== "string" ||
			!value.section.id.trim() ||
			typeof value.section.title !== "string")
	)
		return false;
	return value.manage === undefined || (value.manage === true && value.section !== undefined);
}
function fork(value: unknown): ForkTarget | null {
	try {
		return decodeForkTarget(value);
	} catch {
		return null;
	}
}
function projects(value: unknown): value is NonNullable<SavedLocation["projects"]> {
	if (!isPlainObject(value) || typeof value.archived !== "boolean") return false;
	const project = value.project;
	return (
		project === undefined ||
		(isPlainObject(project) &&
			typeof project.key === "string" &&
			!!project.key.trim() &&
			typeof project.title === "string" &&
			["current", "recent", "archived"].includes(String(project.tier)))
	);
}
export class LocationRepository {
	constructor(private readonly storage: SyncStringStorage) {}
	read(savedHubIds: readonly string[]): SavedLocation | null {
		const raw = this.storage.getItemSync(key);
		if (!raw) return null;
		let value: unknown;
		try {
			value = JSON.parse(raw);
		} catch {
			return null;
		}
		if (!isPlainObject(value) || typeof value.hubId !== "string" || !savedHubIds.includes(value.hubId)) return null;
		if (value.conversation !== undefined && !conversation(value.conversation)) return null;
		if (
			value.projects !== undefined &&
			(!projects(value.projects) ||
				["conversation", "pinned", "pinAssignment", "fork", "deleteSession"].some((key) => value[key] !== undefined))
		)
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
			(!target || !conversation(value.conversation) || value.pinned !== undefined || value.pinAssignment !== undefined)
		)
			return null;
		if (
			value.pinned !== undefined &&
			(!pinned(value.pinned) || value.conversation !== undefined || value.pinAssignment !== undefined)
		)
			return null;
		if (value.pinAssignment !== undefined && (value.pinAssignment !== true || !conversation(value.conversation)))
			return null;
		// A reader needs its session and stands alone.
		if (
			value.reader !== undefined &&
			(!reader(value.reader) ||
				!conversation(value.conversation) ||
				["pinAssignment", "fork", "deleteSession", "pinned", "projects"].some(
					(key) => value[key] !== undefined,
				))
		)
			return null;
		return {
			hubId: value.hubId,
			...(projects(value.projects) ? { projects: value.projects } : {}),
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
			...(reader(value.reader)
				? {
						reader: {
							sessionRef: value.reader.sessionRef,
							path: value.reader.path,
							...(value.reader.updatedAt === undefined ? {} : { updatedAt: value.reader.updatedAt }),
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
/** The route a relaunch reopens: the frontmost one that isn't a sheet. A
 * sheet is a moment over its screen, never a place to come back to. */
export function routeToSave<Route extends { name: string }>(state: {
	index: number;
	routes: readonly Route[];
}): Route | undefined {
	return state.routes.slice(0, state.index + 1).findLast((route) => !isSheetRoute(route.name));
}
export function locationForRoute(
	route: { name: string; params?: unknown },
	hubId: string | null,
): SavedLocation | null {
	if (!hubId || route.name === "Hubs") return null;
	if (route.name === "Projects" || route.name === "Project") {
		if (!isPlainObject(route.params) || route.params.hubId !== hubId) return null;
		const destination = {
			archived: route.params.archived ?? false,
			...(route.name === "Project"
				? {
						project: {
							key: route.params.projectKey,
							title: route.params.title,
							tier: route.params.tier ?? "current",
						},
					}
				: {}),
		};
		return projects(destination) ? { hubId, projects: destination } : null;
	}
	if (route.name === "Fork") {
		if (!isPlainObject(route.params) || route.params.hubId !== hubId) return null;
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
	if (route.name === "PinSections" || route.name === "PinnedSection" || route.name === "PinSectionEditor") {
		if (!isPlainObject(route.params) || route.params.hubId !== hubId) return null;
		if (route.name === "PinSections") return { hubId, pinned: {} };
		const destination = {
			section: { id: route.params.sectionId, title: route.params.title },
			...(route.name === "PinSectionEditor" ? { manage: true as const } : {}),
		};
		return pinned(destination) ? { hubId, pinned: destination } : null;
	}
	// A subagent's session reopens as its coordinator's (ruling 21).
	if (route.name === "Subagent") {
		if (!isPlainObject(route.params) || route.params.hubId !== hubId || !isPlainObject(route.params.coordinator))
			return null;
		const session = { ref: route.params.coordinator.ref, title: route.params.coordinator.title };
		return conversation(session) ? { hubId, conversation: { ref: session.ref, title: session.title } } : null;
	}
	if (route.name === "Reader") {
		if (!isPlainObject(route.params) || route.params.hubId !== hubId) return null;
		const { sessionRef, path, sessionTitle, updatedAt } = route.params;
		const destination = { sessionRef, path, ...(typeof updatedAt === "string" ? { updatedAt } : {}) };
		const session = { ref: sessionRef, title: sessionTitle };
		return reader(destination) && conversation(session)
			? { hubId, conversation: { ref: session.ref, title: session.title }, reader: destination }
			: null;
	}
	// A coordinator's Subagents list reopens as its session (ruling 21).
	if (
		route.name === "Conversation" ||
		route.name === "Subagents" ||
		route.name === "PinAssignment" ||
		route.name === "SessionDeletion"
	) {
		if (!isPlainObject(route.params) || route.params.hubId !== hubId || !conversation(route.params)) return null;
		if (route.name === "SessionDeletion" && !localSessionId(route.params.ref)) return null;
		return {
			hubId,
			conversation: { ref: route.params.ref, title: route.params.title },
			...(route.name === "PinAssignment" ? { pinAssignment: true as const } : {}),
			...(route.name === "SessionDeletion" ? { deleteSession: true as const } : {}),
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
			projectKey?: string;
			archived?: boolean;
			tier?: "current" | "recent" | "archived";
			sessionRef?: string;
			path?: string;
			sessionTitle?: string;
			updatedAt?: string;
		};
	}[] = [{ name: "Hubs" }];
	if (location) routes.push({ name: "Sessions" });
	if (location?.projects) {
		const params = {
			hubId: location.hubId,
			archived: location.projects.archived,
		};
		routes.push({ name: "Projects", params });
		if (location.projects.project) {
			const { key, title, tier } = location.projects.project;
			routes.push({
				name: "Project",
				params: { ...params, projectKey: key, title, tier },
			});
		}
	}
	if (location?.pinned) {
		routes.push({ name: "PinSections", params: { hubId: location.hubId } });
		if (location.pinned.section) {
			const params = {
				hubId: location.hubId,
				sectionId: location.pinned.section.id,
				title: location.pinned.section.title,
			};
			routes.push({ name: "PinnedSection", params });
			if (location.pinned.manage) routes.push({ name: "PinSectionEditor", params });
		}
	}
	if (location?.conversation)
		routes.push({
			name: "Conversation",
			params: { hubId: location.hubId, ...location.conversation },
		});
	if (location?.reader && location.conversation)
		routes.push({
			name: "Reader",
			params: {
				hubId: location.hubId,
				sessionRef: location.reader.sessionRef,
				path: location.reader.path,
				sessionTitle: location.conversation.title,
				...(location.reader.updatedAt === undefined ? {} : { updatedAt: location.reader.updatedAt }),
			},
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
