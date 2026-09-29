// The hub's hosts as evener/host/list reports them, kept current while a page
// shows them. No host lifecycle notification exists, so a started controller
// reads every 2 seconds, as the web's hosts section does (HOST_POLL_MS,
// cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx), and keeps its
// last rows when a read fails.
import { friendlyErrorMessage, type HostRow } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export const HOST_POLL_MS = 2_000;
/** Connect waits on that host's gate, which the hub's
 * supervisor may hold for a whole reconnect cycle, well past the client's
 * 30-second default. The web allows them 35 minutes (HOST_GATE_TIMEOUT_MS,
 * cmd/evener-hub/frontend/src/stores/hosts.ts), so a slow attach isn't
 * reported as a failure while the hub is still working on it. */
export const HOST_GATE_TIMEOUT_MS = 35 * 60_000;

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

	constructor(
		private readonly client: Pick<ConversationClientLike, "request">,
		private readonly pollMs = HOST_POLL_MS,
	) {}

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
		this.inFlight = (async () => {
			do {
				this.again = false;
				try {
					const { hosts } = await this.client.request("evener/host/list", {});
					this.publish({ rows: hosts.filter((row) => !row.removed), error: null });
				} catch (error) {
					this.publish({ error: friendlyErrorMessage(error) });
				}
			} while (this.again && !this.disposed);
		})().finally(() => {
			this.inFlight = null;
		});
		return this.inFlight;
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

	dispose(): void {
		this.disposed = true;
		this.loop += 1;
		if (this.timer) clearTimeout(this.timer);
		this.timer = null;
		this.listeners.clear();
	}
}
