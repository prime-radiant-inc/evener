import { afterEach, describe, expect, it, vi } from "vitest";
import { type HostRow, WireError } from "@evener/appwire-client";
import { HOST_GATE_TIMEOUT_MS, HOST_POLL_MS, HostsController } from "./hostsController";

const row = (name: string, over: Partial<HostRow> = {}): HostRow => ({
	name,
	origin: "sidecar",
	attached: false,
	midAttach: false,
	removed: false,
	generation: 1,
	incarnationId: "incarnation-1",
	...over,
});

/** A hub that holds every request until the test settles it. */
function hub() {
	const pending: {
		method: string;
		params: unknown;
		opts?: { timeoutMs?: number };
		resolve: (value: unknown) => void;
		reject: (error: unknown) => void;
	}[] = [];
	const client = {
		request: (method: string, params: unknown, opts?: { timeoutMs?: number }) =>
			new Promise((resolve, reject) => pending.push({ method, params, opts, resolve, reject })),
	};
	const take = (method: string) => {
		const index = pending.findIndex((request) => request.method === method);
		if (index < 0) throw new Error(`no pending ${method}`);
		return pending.splice(index, 1)[0] as (typeof pending)[number];
	};
	const count = (method: string) => pending.filter((request) => request.method === method).length;
	return { client: client as never, pending, take, count };
}
const settle = () => new Promise((resolve) => setImmediate(resolve));

afterEach(() => vi.useRealTimers());

describe("the hosts controller", () => {
	it("reads the hub's hosts and leaves removed ones out", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const reading = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park"), row("gone", { removed: true })] });
		await reading;
		expect(hosts.getSnapshot().rows?.map((host) => host.name)).toEqual(["paradise-park"]);
	});

	it("keeps its rows when a read fails, and says why", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const first = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await first;
		const second = hosts.read();
		h.take("evener/host/list").reject(new WireError("hub is busy", -32000));
		await second;
		expect(hosts.getSnapshot()).toMatchObject({ error: "hub is busy" });
		expect(hosts.getSnapshot().rows).toHaveLength(1);
	});

	it("runs a read asked for mid-flight once more after it, and only once", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const first = hosts.read();
		const second = hosts.read();
		const third = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [] });
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await Promise.all([first, second, third]);
		expect(h.count("evener/host/list")).toBe(0);
		expect(hosts.getSnapshot().rows).toHaveLength(1);
	});

	it("polls while started, and stops only with the last stop", async () => {
		vi.useFakeTimers();
		const lists: string[] = [];
		const hosts = new HostsController({
			request: async (method: string) => {
				lists.push(method);
				return { hosts: [] };
			},
		} as never);
		const stopOne = hosts.start();
		const stopTwo = hosts.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(lists).toHaveLength(1);
		await vi.advanceTimersByTimeAsync(HOST_POLL_MS);
		expect(lists).toHaveLength(2);
		stopOne();
		await vi.advanceTimersByTimeAsync(HOST_POLL_MS);
		expect(lists).toHaveLength(3);
		stopTwo();
		await vi.advanceTimersByTimeAsync(HOST_POLL_MS * 5);
		expect(lists).toHaveLength(3);
		expect(vi.getTimerCount()).toBe(0);
	});

	it("marks a host connecting until the hub answers, then re-reads", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const connecting = hosts.connect("paradise-park");
		expect([...hosts.getSnapshot().connecting]).toEqual(["paradise-park"]);
		const attach = h.take("evener/host/attach");
		expect(attach.params).toEqual({ host: "paradise-park" });
		expect(attach.opts).toEqual({ timeoutMs: HOST_GATE_TIMEOUT_MS });
		attach.resolve({ attached: true, host: "paradise-park" });
		await settle();
		expect(hosts.getSnapshot().connecting.size).toBe(0);
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park", { attached: true })] });
		await connecting;
		expect(hosts.getSnapshot().rows?.[0]?.attached).toBe(true);
	});

	it("keeps a refused Connect's message for that host until its next Connect", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const refused = hosts.connect("paradise-park");
		h.take("evener/host/attach").reject(
			new WireError("ssh: connect to host paradise-park port 22: Connection refused", -32000),
		);
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await refused;
		expect(hosts.getSnapshot().connectErrors.get("paradise-park")).toBe(
			"ssh: connect to host paradise-park port 22: Connection refused",
		);
		void hosts.connect("paradise-park");
		expect(hosts.getSnapshot().connectErrors.has("paradise-park")).toBe(false);
	});
});
