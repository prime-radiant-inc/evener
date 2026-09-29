// The hub's hosts as evener/host/list reports them, kept current while a page
// shows them. No host lifecycle notification exists, so a started controller
// reads every 2 seconds, as the web's hosts section does (HOST_POLL_MS,
// cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx), and keeps its
// last rows when a read fails. Edit and Remove go through the package's guarded
// host mutations (hostMutations.ts), the core the web's hosts store wraps.
import {
	createHostMutations,
	friendlyErrorMessage,
	HOST_GATE_TIMEOUT_MS,
	HostMutationOutcomeError,
	type HostEntry,
	type HostMutationPair,
	type HostMutations,
	type HostRow,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export const HOST_POLL_MS = 2_000;

const noMutationIds = (): string => {
	throw new Error("HostsController: editing or removing a host needs a mutation id source");
};

export interface HostsState {
	/** null until the first read lands; removed hosts are left out. */
	rows: HostRow[] | null;
	/** The last failed read's message; the rows stay as they were. */
	error: string | null;
	/** Hosts this phone asked to connect, until the hub answers. */
	connecting: ReadonlySet<string>;
	/** A refused Connect's message, per host, until its next Connect. */
	connectErrors: ReadonlyMap<string, string>;
}

export class HostsController {
	private state: HostsState = { rows: null, error: null, connecting: new Set(), connectErrors: new Map() };
	private readonly listeners = new Set<() => void>();
	private inFlight: Promise<void> | null = null;
	private again = false;
	private starts = 0;
	private loop = 0;
	private timer: ReturnType<typeof setTimeout> | null = null;
	private disposed = false;

	private readonly mutations: HostMutations;

	/** newMutationId is the random id each edit or removal attempt carries
	 * (expo-crypto's randomUUID in the app). */
	constructor(
		private readonly client: Pick<ConversationClientLike, "request">,
		newMutationId: () => string = noMutationIds,
		private readonly pollMs = HOST_POLL_MS,
	) {
		this.mutations = createHostMutations({
			client: () => this.client,
			heldPair: (name) => {
				const row = this.state.rows?.find((candidate) => candidate.name === name);
				return row && { generation: row.generation, incarnationId: row.incarnationId };
			},
			reRead: () => this.read(),
			newMutationId,
		});
	}

	getSnapshot = (): HostsState => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	private publish(change: Partial<HostsState>) {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}

	/** Reads now, then every pollMs, until every start's stop has run. Pages
	 * start it when they come into view and stop it when they leave. */
	start(): () => void {
		this.starts += 1;
		if (this.starts === 1) void this.poll(++this.loop);
		let stopped = false;
		return () => {
			if (stopped) return;
			stopped = true;
			this.starts -= 1;
			if (this.starts > 0) return;
			this.loop += 1;
			if (this.timer) clearTimeout(this.timer);
			this.timer = null;
		};
	}

	private async poll(loop: number): Promise<void> {
		await this.read();
		if (loop !== this.loop || this.disposed) return;
		this.timer = setTimeout(() => void this.poll(loop), this.pollMs);
	}

	/** One read. A read asked for while another is in flight runs once more
	 * after it, so no answer is older than the request that asked for it. */
	read(): Promise<void> {
		if (this.disposed) return Promise.resolve();
		if (this.inFlight) {
			this.again = true;
			return this.inFlight;
		}
		let ended = false;
		const loop = (async () => {
			do {
				this.again = false;
				try {
					const { hosts } = await this.client.request("evener/host/list", {});
					// A refusal is moot once the host is attached, however it got there.
					const connectErrors = new Map(this.state.connectErrors);
					for (const row of hosts) if (row.attached) connectErrors.delete(row.name);
					this.publish({ rows: hosts.filter((row) => !row.removed), error: null, connectErrors });
				} catch (error) {
					this.publish({ error: friendlyErrorMessage(error) });
				}
			} while (this.again && !this.disposed);
			// Cleared in the same step as the loop's last check: a read asked
			// for after it starts a new loop instead of joining this one.
			ended = true;
			this.inFlight = null;
		})();
		// A client that refuses without awaiting ends the loop before this line.
		if (!ended) this.inFlight = loop;
		return loop;
	}

	/** Asks the hub to attach a host (evener/host/attach, the web's Connect),
	 * then re-reads the rows. */
	async connect(name: string): Promise<void> {
		if (this.state.connecting.has(name)) return;
		const connectErrors = new Map(this.state.connectErrors);
		connectErrors.delete(name);
		this.publish({ connecting: new Set([...this.state.connecting, name]), connectErrors });
		try {
			await this.client.request("evener/host/attach", { host: name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
		} catch (error) {
			this.publish({ connectErrors: new Map([...this.state.connectErrors, [name, friendlyErrorMessage(error)]]) });
		}
		// The host stays connecting until the rows say what the attach did;
		// clearing it first would show Offline and Connect for a moment.
		await this.read();
		this.publish({ connecting: new Set([...this.state.connecting].filter((host) => host !== name)) });
	}

	/** Edits a host (evener/host/update) against `expected`, the pair of the
	 * row the edit form opened on, then re-reads the rows. A refusal rejects,
	 * so the edit page can put it under the field it names, or say the host
	 * changed since it opened. */
	async update(name: string, entry: HostEntry, expected: HostMutationPair): Promise<void> {
		let outcomeError: unknown;
		try {
			await this.mutations.update({ name, entry, expected });
		} catch (error) {
			if (!(error instanceof HostMutationOutcomeError)) throw error;
			// The union's non-commit arm may still have committed (a teardown
			// failure), so the read below runs before the arm's message surfaces.
			outcomeError = error;
		}
		await this.read();
		if (outcomeError !== undefined) throw outcomeError;
	}

	/** Removes a host (evener/host/remove), then re-reads the rows. */
	async remove(name: string): Promise<void> {
		let outcomeError: unknown;
		try {
			await this.mutations.remove(name);
		} catch (error) {
			if (!(error instanceof HostMutationOutcomeError)) throw error;
			// The union's non-commit arm may still have committed (a teardown
			// failure); the read below reflects it.
			outcomeError = error;
		}
		if (outcomeError === undefined) {
			// A committed removal: the rows drop it now, so its page leaves even
			// when the read after this fails. A non-commit arm instead re-reads,
			// so a collision that dropped nothing is not optimistically dropped.
			this.publish({ rows: this.state.rows?.filter((row) => row.name !== name) ?? null });
		}
		await this.read();
		if (outcomeError !== undefined) throw outcomeError;
	}

	dispose(): void {
		this.disposed = true;
		this.loop += 1;
		if (this.timer) clearTimeout(this.timer);
		this.timer = null;
		this.listeners.clear();
	}
}
