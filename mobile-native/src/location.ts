export interface SavedLocation {
	hubId: string;
	conversation?: { ref: string; title: string };
	pinAssignment?: true;
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
		if (
			value.pinAssignment !== undefined &&
			(value.pinAssignment !== true || !conversation(value.conversation))
		)
			return null;
		return {
			hubId: value.hubId,
			...(value.pinAssignment === true ? { pinAssignment: true as const } : {}),
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
	if (route.name === "Conversation" || route.name === "PinAssignment") {
		if (
			!object(route.params) ||
			route.params.hubId !== hubId ||
			!conversation(route.params)
		)
			return null;
		return {
			hubId,
			conversation: { ref: route.params.ref, title: route.params.title },
			...(route.name === "PinAssignment"
				? { pinAssignment: true as const }
				: {}),
		};
	}
	return { hubId };
}
export function restoredStack(location: SavedLocation | null) {
	const routes: {
		name: string;
		params?: { hubId: string; ref: string; title: string };
	}[] = [{ name: "Hubs" }];
	if (location) routes.push({ name: "Sessions" });
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
	return { index: routes.length - 1, routes };
}
