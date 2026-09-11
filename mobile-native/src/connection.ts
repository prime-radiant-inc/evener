import { AppwireClient } from "../../cmd/evener-hub/frontend/src/protocol/client";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";

export interface HubProfile {
	id: string;
	name: string;
	origin: string;
}
export interface HubInput {
	name: string;
	origin: string;
	token: string;
}
export interface HubUpdate {
	name: string;
	token?: string;
}

const hasPairingControl = (value: string) =>
	[...value].some((character) => {
		const code = character.codePointAt(0) ?? 0;
		return code <= 0x1f || code === 0x7f;
	});
const invalidPairingURL = () => new Error("Invalid pairing URL.");

/** Parses the hub's /auth/<PathEscape(token)> QR/paste format without I/O. */
export function parsePairingURL(input: string): {
	origin: string;
	token: string;
} {
	if (
		typeof input !== "string" ||
		input.length === 0 ||
		hasPairingControl(input) ||
		input.includes("\\")
	)
		throw invalidPairingURL();
	const match = /^(https?):\/\/([^/?#]+)(\/[^?#]*)$/.exec(input);
	if (!match || match[2].includes("@")) throw invalidPairingURL();
	let url: URL;
	try {
		url = new URL(input);
	} catch {
		throw invalidPairingURL();
	}
	if (!url.hostname || url.username || url.password || url.search || url.hash)
		throw invalidPairingURL();
	const path = match[3];
	if (url.pathname !== path) throw invalidPairingURL();
	if (
		!path.startsWith("/auth/") ||
		path.slice(6).length === 0 ||
		path.slice(6).includes("/")
	)
		throw invalidPairingURL();
	let token: string;
	try {
		token = decodeURIComponent(path.slice(6));
	} catch {
		throw invalidPairingURL();
	}
	if (!token || hasPairingControl(token)) throw invalidPairingURL();
	return { origin: url.origin, token };
}
export interface SecureStorage {
	getItemAsync(key: string): Promise<string | null>;
	setItemAsync(key: string, value: string): Promise<void>;
	deleteItemAsync(key: string): Promise<void>;
}

export function connectionTarget(input: string): string {
	let url: URL;
	try {
		url = new URL(input.trim());
	} catch {
		throw new Error("Enter a complete http:// or https:// hub address.");
	}
	if (
		!["http:", "https:"].includes(url.protocol) ||
		!url.hostname ||
		url.username ||
		url.password ||
		url.search ||
		url.hash ||
		url.pathname !== "/"
	)
		throw new Error(
			"Use the hub origin only, with the token in its separate field.",
		);
	url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
	url.pathname = "/rpc";
	return url.toString();
}

const INDEX = "evener.hubs";
function key(id: string): string {
	if (!/^[a-zA-Z0-9-]+$/.test(id)) throw new Error("Invalid hub identifier.");
	return `evener.hub.${id}`;
}

/** Each credential is a separate secure item; the index contains only IDs. */
export class HubProfiles {
	constructor(private storage: SecureStorage) {}
	private writes: Promise<unknown> = Promise.resolve();
	private write<T>(operation: () => Promise<T>): Promise<T> {
		const result = this.writes.then(operation);
		this.writes = result.catch(() => undefined);
		return result;
	}
	private async ids(): Promise<string[]> {
		const raw = await this.storage.getItemAsync(INDEX);
		if (!raw) return [];
		// An index we cannot parse names no hubs we could open, so it reads as
		// empty: list() shows an empty roster and the user can save a hub again,
		// where propagating the parse error would leave the app with no way in.
		// A parsed index of the wrong shape still throws below — that one is a
		// readable record making a claim we refuse, not an unreadable one.
		let ids: unknown;
		try {
			ids = JSON.parse(raw);
		} catch {
			return [];
		}
		if (
			!Array.isArray(ids) ||
			!ids.every((id) => typeof id === "string" && /^[a-zA-Z0-9-]+$/.test(id))
		)
			throw new Error("Saved hub index could not be read.");
		return ids;
	}
	private async read(
		id: string,
	): Promise<(HubProfile & { token: string }) | null> {
		const raw = await this.storage.getItemAsync(key(id));
		if (!raw) return null;
		// A record is a single hub the caller asked for by id, so an unreadable
		// one is reported rather than silently skipped — token() must not hand
		// back an empty token for a hub that is actually there but corrupt.
		// list() catches this per record and drops it, so one bad record still
		// does not take the roster down.
		let value: Partial<HubProfile & { token: string }> | null;
		try {
			value = JSON.parse(raw);
		} catch {
			throw new Error("Saved hub could not be read.");
		}
		if (
			value === null ||
			typeof value !== "object" ||
			typeof value.id !== "string" ||
			value.id !== id ||
			typeof value.name !== "string" ||
			typeof value.origin !== "string" ||
			typeof value.token !== "string"
		)
			throw new Error("Saved hub could not be read.");
		connectionTarget(value.origin);
		return {
			id: value.id,
			name: value.name,
			origin: value.origin,
			token: value.token,
		};
	}
	async list(): Promise<HubProfile[]> {
		const values = await Promise.all(
			(await this.ids()).map(async (id) => {
				try {
					return await this.read(id);
				} catch {
					return null;
				}
			}),
		);
		return values.flatMap((value) =>
			value ? [{ id: value.id, name: value.name, origin: value.origin }] : [],
		);
	}
	async token(id: string): Promise<string> {
		return (await this.read(id))?.token ?? "";
	}
	update(id: string, input: HubUpdate): Promise<HubProfile> {
		return this.write(async () => {
			if (!(await this.ids()).includes(id))
				throw new Error("This hub is no longer saved.");
			const current = await this.read(id);
			if (!current) throw new Error("This hub is no longer saved.");
			return this.saveProfile({
				id,
				origin: current.origin,
				name: input.name,
				token: input.token ?? current.token,
			});
		});
	}
	save(input: HubInput & { id: string }): Promise<HubProfile> {
		return this.write(() => this.saveProfile(input));
	}
	private async saveProfile(
		input: HubInput & { id: string },
	): Promise<HubProfile> {
		connectionTarget(input.origin);
		if (!input.name.trim()) throw new Error("Give this hub a name.");
		if (/[\r\n]/.test(input.token))
			throw new Error("The token must be a single line.");
		const profile = {
			id: input.id,
			name: input.name.trim(),
			origin: new URL(input.origin.trim()).origin,
		};
		await this.storage.setItemAsync(
			key(input.id),
			JSON.stringify({ ...profile, token: input.token.trim() }),
		);
		const ids = await this.ids();
		if (!ids.includes(input.id))
			await this.storage.setItemAsync(
				INDEX,
				JSON.stringify([...ids, input.id]),
			);
		return profile;
	}
	remove(id: string): Promise<void> {
		return this.write(() => this.removeProfile(id));
	}
	private async removeProfile(id: string): Promise<void> {
		await this.storage.setItemAsync(
			INDEX,
			JSON.stringify((await this.ids()).filter((value) => value !== id)),
		);
		await this.storage.deleteItemAsync(key(id));
	}
}

export type NativeSocketFactory = (
	url: string,
	options: { headers: Record<string, string> },
) => WebSocketLike;
export function createHubClient(
	origin: string,
	token: string,
	socketFactory: NativeSocketFactory,
): AppwireClient {
	return new AppwireClient({
		url: connectionTarget(origin),
		clientInfo: { name: "evener-native", version: "0.1.0" },
		socketFactory: (url) =>
			socketFactory(url, {
				headers: token ? { Authorization: `Bearer ${token}` } : {},
			}),
	});
}
