import { AppwireClient, WireError } from "@evener/appwire-client";
import type { WebSocketLike } from "@evener/appwire-client";
import { hubRPCURL, parseHubOrigin } from "@evener/appwire-client/hubOrigin";

export interface HubProfile {
	id: string;
	name: string;
	origin: string;
}

/** Whether `hubId` is still one of the phone's saved hubs. */
export function hasHub(profiles: readonly Pick<HubProfile, "id">[], hubId: string): boolean {
	return profiles.some((profile) => profile.id === hubId);
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
	if (typeof input !== "string" || input.length === 0 || hasPairingControl(input) || input.includes("\\"))
		throw invalidPairingURL();
	const match = /^(https?):\/\/([^/?#]+)(\/[^?#]*)$/.exec(input);
	if (!match || match[2].includes("@")) throw invalidPairingURL();
	let url: URL;
	try {
		url = new URL(input);
	} catch {
		throw invalidPairingURL();
	}
	if (!url.hostname || url.username || url.password || url.search || url.hash) throw invalidPairingURL();
	const path = match[3];
	if (url.pathname !== path) throw invalidPairingURL();
	if (!path.startsWith("/auth/") || path.slice(6).length === 0 || path.slice(6).includes("/"))
		throw invalidPairingURL();
	let token: string;
	try {
		token = decodeURIComponent(path.slice(6));
	} catch {
		throw invalidPairingURL();
	}
	if (!token || hasPairingControl(token)) throw invalidPairingURL();
	try {
		return { origin: parseHubOrigin(`${match[1]}://${match[2]}`), token };
	} catch {
		throw invalidPairingURL();
	}
}
export interface SecureStorage {
	getItemAsync(key: string): Promise<string | null>;
	setItemAsync(key: string, value: string): Promise<void>;
	deleteItemAsync(key: string): Promise<void>;
}

export function connectionTarget(input: string): string {
	return hubRPCURL(input);
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
		// An index that will not parse, and one that parses to anything but a
		// list of hub ids, are the same thing: neither names a hub anyone could
		// open. Both read as empty, so list() shows an empty roster and the user
		// can save a hub again; failing instead would leave an app with a
		// damaged index no way back in.
		let ids: unknown;
		try {
			ids = JSON.parse(raw);
		} catch {
			return [];
		}
		if (!Array.isArray(ids) || !ids.every((id) => typeof id === "string" && /^[a-zA-Z0-9-]+$/.test(id))) return [];
		return ids;
	}
	private async read(id: string): Promise<(HubProfile & { token: string }) | null> {
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
		return values.flatMap((value) => (value ? [{ id: value.id, name: value.name, origin: value.origin }] : []));
	}
	async token(id: string): Promise<string> {
		return (await this.read(id))?.token ?? "";
	}
	update(id: string, input: HubUpdate): Promise<HubProfile> {
		return this.write(async () => {
			if (!(await this.ids()).includes(id)) throw new Error("This hub is no longer saved.");
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
	private async saveProfile(input: HubInput & { id: string }): Promise<HubProfile> {
		const origin = parseHubOrigin(input.origin);
		if (!input.name.trim()) throw new Error("Give this hub a name.");
		if (/[\r\n]/.test(input.token)) throw new Error("The token must be a single line.");
		const profile = {
			id: input.id,
			name: input.name.trim(),
			origin,
		};
		await this.storage.setItemAsync(key(input.id), JSON.stringify({ ...profile, token: input.token.trim() }));
		const ids = await this.ids();
		if (!ids.includes(input.id)) await this.storage.setItemAsync(INDEX, JSON.stringify([...ids, input.id]));
		return profile;
	}
	remove(id: string): Promise<void> {
		return this.write(() => this.removeProfile(id));
	}
	private async removeProfile(id: string): Promise<void> {
		await this.storage.setItemAsync(INDEX, JSON.stringify((await this.ids()).filter((value) => value !== id)));
		await this.storage.deleteItemAsync(key(id));
	}
}

export type NativeSocketFactory = (url: string, options: { headers: Record<string, string> }) => WebSocketLike;

/** A resume that provably did not reach the hub. Nothing was sent, so the
 * caller still owes it and may try again on a usable binding; a rejection
 * from the request itself is not this, because the hub may have taken it and
 * replaying it could resume the same session twice. */
export class ResumeNotDispatched extends Error {
	constructor(cause: string) {
		super(cause);
		this.name = "ResumeNotDispatched";
	}
}

/** A resume that reached the connection and then turned out not to be wanted.
 * Nothing was sent: the transport is left alone and the answer says whether
 * to ask again ("retry"), wait for a newer read ("stale"), or rest ("blocked"). */
export class ResumeAbandoned extends Error {
	constructor(readonly verdict: "blocked" | "retry" | "stale") {
		super("The reason to resume no longer holds");
		this.name = "ResumeAbandoned";
	}
}

/** What a caller may say about a resume it is asking for. `beforeRecycle` runs
 * once the connection's slot is held and the transport can send, but before
 * the resume retires that transport — the last point at which abandoning the
 * call costs nothing. `beforeRequest` runs after the fresh handshake, before
 * the RPC, for anything that can still change while the handshake is out. */
export interface ResumeAdmission {
	beforeRecycle?: () => void | Promise<void>;
	beforeRequest?: () => void | Promise<void>;
}

/** One explicit resume at a time per connection. Every caller — a restart and
 * the outbox's recovery alike — goes through the client's own `resumeThread`,
 * so the slot is enforced there rather than beside any one of them. A queued
 * resume waits for the transport to be usable: the slot freeing because a
 * socket dropped is not evidence that the next one can be sent, and dispatching
 * into the reconnect is how a restart ends with the daemon stopped. */
const resumeSlots = new WeakMap<object, Promise<void>>();
function admitResume<T>(
	client: AppwireClient,
	run: () => Promise<T>,
	beforeRecycle?: () => void | Promise<void>,
): Promise<T> {
	const previous = resumeSlots.get(client) ?? Promise.resolve();
	let release!: () => void;
	const held = new Promise<void>((resolve) => {
		release = resolve;
	});
	resumeSlots.set(
		client,
		previous.then(() => held),
	);
	return (
		previous
			.then(() => waitForReady(client))
			// Abandoning here is free: the transport has not been retired, so nothing
			// it is carrying is lost and no other caller sees a recycle for nothing.
			.then(() => beforeRecycle?.())
			.then(() => run())
			.finally(() => release())
	);
}

/** Resolves once the client can send, or rejects with a resume that was never
 * dispatched. A closed client will never send, so that is not a wait. */
function waitForReady(client: AppwireClient): Promise<void> {
	if (client.state === "ready") return Promise.resolve();
	if (client.state === "closed") return Promise.reject(new ResumeNotDispatched("The connection is closed"));
	return new Promise<void>((resolve, reject) => {
		const timer = setTimeout(() => {
			stop();
			reject(new ResumeNotDispatched("The connection did not come back"));
		}, DEFAULT_RESUME_WAIT_MS);
		const stopReady = client.onReady(() => {
			stop();
			resolve();
		});
		const stopState = client.onStateChange((state) => {
			if (state !== "closed") return;
			stop();
			reject(new ResumeNotDispatched("The connection closed"));
		});
		function stop() {
			clearTimeout(timer);
			stopReady();
			stopState();
		}
	});
}

const DEFAULT_RESUME_WAIT_MS = 30_000;

/** Which attempt's frame a socket write belongs to. The slot guarantees at most
 * one attempt is sending, so the socket names the holder rather than the client
 * guessing from call order: a resume that queues behind another must not inherit
 * an answer owed to it. */
interface ResumeFrames {
	holding: { sent: boolean } | null;
}

/** Watches one socket for the resume frame. The client writes every request as
 * JSON with its method, so the frame is identified by that alone; a socket
 * that fails to send records nothing, which is the point. */
function watchResumeFrames(socket: WebSocketLike, frames: ResumeFrames): WebSocketLike {
	return {
		get onopen() {
			return socket.onopen;
		},
		set onopen(value) {
			socket.onopen = value;
		},
		get onmessage() {
			return socket.onmessage;
		},
		set onmessage(value) {
			socket.onmessage = value;
		},
		get onerror() {
			return socket.onerror;
		},
		set onerror(value) {
			socket.onerror = value;
		},
		get onclose() {
			return socket.onclose;
		},
		set onclose(value) {
			socket.onclose = value;
		},
		send(data: string) {
			// Recorded after the socket takes it: a send that throws left nothing.
			socket.send(data);
			if (frames.holding !== null && data.includes('"method":"thread/resume"')) frames.holding.sent = true;
		},
		close(code?: number) {
			socket.close(code);
		},
	};
}

export function createHubClient(origin: string, token: string, socketFactory: NativeSocketFactory): AppwireClient {
	// Whether the hub was actually asked is a fact about one frame, and the
	// socket's own send is the only place that fact exists. Everything earlier
	// is intent: the client retires the transport, re-handshakes, runs
	// `beforeRequest`, and only then passes its readiness admission, which can
	// still refuse without a byte leaving. Marking dispatch anywhere before the
	// frame is what let a never-sent resume be reported as though the hub had
	// answered it.
	const frames: ResumeFrames = { holding: null };
	const client = new AppwireClient({
		url: connectionTarget(origin),
		clientInfo: { name: "evener-native", version: "0.3.0" },
		socketFactory: (url) =>
			watchResumeFrames(
				socketFactory(url, {
					headers: token ? { Authorization: `Bearer ${token}` } : {},
				}),
				frames,
			),
	});
	const resumeThread = client.resumeThread.bind(client);
	client.resumeThread = (ref: string, options?: Parameters<typeof resumeThread>[1]) => {
		const admission = options as ResumeAdmission | undefined;
		// This attempt's own dispatch record. It is installed only once the slot
		// is held, so a resume still queued behind another cannot see — or clear —
		// the answer owed to the attempt that is actually on the wire.
		const frame = { sent: false };
		return admitResume(
			client,
			async () => {
				frames.holding = frame;
				try {
					return await resumeThread(ref, {
						...options,
						beforeRequest: async () => {
							await options?.beforeRequest?.();
						},
					});
				} finally {
					if (frames.holding === frame) frames.holding = null;
				}
			},
			admission?.beforeRecycle,
		).catch((cause: unknown) => {
			// The caller decided the resume was not wanted: nothing was sent, and
			// its own verdict says how to ask again.
			if (cause instanceof ResumeAbandoned || cause instanceof ResumeNotDispatched) throw cause;
			// The hub answered. That is its own refusal, not a transport accident,
			// and it must stay visible.
			if (cause instanceof WireError) throw cause;
			// No resume frame left the socket, so there is nothing the hub could
			// have taken: the step is still owed and may be sent again.
			if (!frame.sent)
				throw new ResumeNotDispatched(cause instanceof Error ? cause.message : "The connection did not come back");
			// The frame went out. Whether the hub took it is unknowable here,
			// so it is reported and never replayed.
			throw cause;
		});
	};
	return client;
}
