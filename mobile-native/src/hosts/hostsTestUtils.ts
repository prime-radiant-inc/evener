// A scripted hub for the Hosts pages' tests: it answers evener/host/list from
// rows the test holds, evener/host/attach, update and remove as the test says
// (a committed update bumps the row's generation, as the registry does), and
// the Live section from sessions the test holds, through the real package
// codec.
import type { HostRow, NavigationReadParams } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";

export const hostRow = (name: string, over: Partial<HostRow> = {}): HostRow => ({
	name,
	origin: "hub.toml",
	attached: true,
	midAttach: false,
	removed: false,
	generation: 1,
	incarnationId: `${name}-1`,
	...over,
});

export const liveSession = (ref: string, host_id: string) => ({
	ref,
	host_id,
	session_id: ref,
	title: ref,
	project: "evener",
	state: "active",
	kind: "session",
	live: true,
	children: [],
});

export interface ScriptedFleet {
	client: FakeClient;
	hosts: HostRow[];
	sessions: ReturnType<typeof liveSession>[];
	calls: { method: string; params: unknown }[];
	/** How the next evener/host/attach answers: attach the host, or refuse. */
	attach: { refuse?: string; hold?: boolean };
	/** Settles an attach the test held. */
	releaseAttach(): void;
	/** A refusal the next evener/host/update or remove answers with. */
	refuse: { update?: Error; remove?: Error };
	/** Mutation ids for a HostsController over this fleet. */
	newMutationId(): string;
}

export function scriptedFleet(hosts: HostRow[], sessions: ReturnType<typeof liveSession>[] = []): ScriptedFleet {
	let heldAttach: (() => void) | null = null;
	let mutationIds = 0;
	const client = new FakeClient("ready");
	const fleet: ScriptedFleet = {
		hosts,
		sessions,
		calls: client.calls,
		attach: {},
		releaseAttach: () => heldAttach?.(),
		refuse: {},
		newMutationId: () => `mutation-${++mutationIds}`,
		client,
	};
	client.on("evener/host/list", () => ({ hosts: fleet.hosts }));
	client.on("evener/host/attach", async (params) => {
		const { host } = params as { host: string };
		if (fleet.attach.refuse) throw new WireError(fleet.attach.refuse, -32000);
		if (fleet.attach.hold) await new Promise<void>((resolve) => (heldAttach = resolve));
		fleet.hosts = fleet.hosts.map((row) => (row.name === host ? { ...row, attached: true } : row));
		return { attached: true, host } as never;
	});
	client.on("evener/host/update", (params) => {
		const refusal = fleet.refuse.update;
		fleet.refuse.update = undefined;
		if (refusal) throw refusal;
		const { name, entry, expectedGeneration, expectedIncarnationId } = params as unknown as {
			name: string;
			entry: Partial<HostRow>;
			expectedGeneration: number;
			expectedIncarnationId: string;
		};
		const current = fleet.hosts.find((row) => row.name === name);
		if (!current) throw new WireError(`host "${name}" is not listed`, -32602);
		// The registry's guard: only the current pair changes the entry.
		if (current.generation !== expectedGeneration || current.incarnationId !== expectedIncarnationId)
			throw new WireError(`host "${name}": the entry moved`, -32013, { evenerErrorInfo: "stale-entry" });
		const updated = { ...current, ...entry, generation: current.generation + 1 };
		fleet.hosts = fleet.hosts.map((row) => (row.name === name ? updated : row));
		return { outcome: "committed", host: updated } as never;
	});
	client.on("evener/host/remove", (params) => {
		const refusal = fleet.refuse.remove;
		fleet.refuse.remove = undefined;
		if (refusal) throw refusal;
		const { name } = params as { name: string };
		const current = fleet.hosts.find((row) => row.name === name);
		fleet.hosts = fleet.hosts.filter((row) => row.name !== name);
		return {
			outcome: "committed",
			host: { name, generation: current?.generation, incarnationId: current?.incarnationId },
		} as never;
	});
	client.on("evener/navigation/read", (params) => {
		const read = params as NavigationReadParams;
		return wireSnapshot(
			{ ...read, representationVersion: 3, offset: read.offset ?? 0, limit: read.limit ?? 50 },
			{ sessions: fleet.sessions, remaining: 0 },
			"etag-live",
			1,
			"generation-test",
		) as never;
	});
	return fleet;
}
