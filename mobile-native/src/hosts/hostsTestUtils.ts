// A scripted hub for the Hosts pages' tests: it answers evener/host/list from
// rows the test holds, evener/host/attach as the test says, and the Live
// section from sessions the test holds, through the real package codec.
import type { HostRow, NavigationReadParams } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

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
	client: ConversationClientLike;
	hosts: HostRow[];
	sessions: ReturnType<typeof liveSession>[];
	calls: { method: string; params: unknown }[];
	/** How the next evener/host/attach answers: attach the host, or refuse. */
	attach: { refuse?: string; hold?: boolean };
	/** Settles an attach the test held. */
	releaseAttach(): void;
}

export function scriptedFleet(hosts: HostRow[], sessions: ReturnType<typeof liveSession>[] = []): ScriptedFleet {
	let heldAttach: (() => void) | null = null;
	const fleet: ScriptedFleet = {
		hosts,
		sessions,
		calls: [],
		attach: {},
		releaseAttach: () => heldAttach?.(),
		client: {
			request: async (method: string, params: unknown) => {
				fleet.calls.push({ method, params });
				if (method === "evener/host/list") return { hosts: fleet.hosts };
				if (method === "evener/host/attach") {
					const { host } = params as { host: string };
					if (fleet.attach.refuse) throw new WireError(fleet.attach.refuse, -32000);
					if (fleet.attach.hold) await new Promise<void>((resolve) => (heldAttach = resolve));
					fleet.hosts = fleet.hosts.map((row) => (row.name === host ? { ...row, attached: true } : row));
					return { attached: true, host };
				}
				if (method === "evener/navigation/read") {
					const read = params as NavigationReadParams;
					return wireV2(
						{ ...read, representationVersion: 2, offset: read.offset ?? 0, limit: read.limit ?? 50 },
						{ sessions: fleet.sessions, remaining: 0 },
						"etag-live",
						1,
						"generation-test",
					);
				}
				throw new Error(`hostsTestUtils: no answer for ${method}`);
			},
			onNotification: () => () => {},
		} as unknown as ConversationClientLike,
	};
	return fleet;
}
