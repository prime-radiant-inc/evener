import { afterEach, describe, expect, it, vi } from "vitest";
import { type HostRow, WireError } from "@evener/appwire-client";
import { HOST_GATE_TIMEOUT_MS } from "@evener/appwire-client";
import { HOST_POLL_MS, HostsController } from "./hostsController";

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

	it("marks a host connecting until the re-read after the hub's answer lands, so Connect never flashes back", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const connecting = hosts.connect("paradise-park");
		expect([...hosts.getSnapshot().connecting]).toEqual(["paradise-park"]);
		const attach = h.take("evener/host/attach");
		expect(attach.params).toEqual({ host: "paradise-park" });
		expect(attach.opts).toEqual({ timeoutMs: HOST_GATE_TIMEOUT_MS });
		attach.resolve({ attached: true, host: "paradise-park" });
		await settle();
		expect([...hosts.getSnapshot().connecting]).toEqual(["paradise-park"]);
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park", { attached: true })] });
		await connecting;
		expect(hosts.getSnapshot().connecting.size).toBe(0);
		expect(hosts.getSnapshot().rows?.[0]?.attached).toBe(true);
	});

	it("asks the hub again for a read requested at any moment after an answer lands", async () => {
		for (let ticks = 0; ticks < 8; ticks++) {
			const h = hub();
			const hosts = new HostsController(h.client);
			void hosts.read();
			h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
			for (let tick = 0; tick < ticks; tick++) await Promise.resolve();
			void hosts.read();
			await settle();
			expect(h.count("evener/host/list"), `a read asked for ${ticks} microtasks after the answer`).toBe(1);
		}
	});

	it("reads again after a client that refuses before it answers", async () => {
		let asked = 0;
		const hosts = new HostsController({
			request: () => {
				asked += 1;
				throw new Error("not connected");
			},
			onNotification: () => () => {},
		} as never);
		await hosts.read();
		await hosts.read();
		expect(asked).toBe(2);
	});

	it("forgets a refused Connect's message once the hub lists the host attached", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const refused = hosts.connect("paradise-park");
		h.take("evener/host/attach").reject(new WireError("host key mismatch", -32000));
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await refused;
		expect(hosts.getSnapshot().connectErrors.has("paradise-park")).toBe(true);
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park", { attached: true })] });
		await read;
		expect(hosts.getSnapshot().connectErrors.has("paradise-park")).toBe(false);
	});

	it("asks the hub nothing once disposed", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		hosts.dispose();
		await hosts.read();
		expect(h.count("evener/host/list")).toBe(0);
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

describe("editing and removing a host (spec 12)", () => {
	const ids = () => {
		let next = 0;
		return () => `m${++next}`;
	};

	it("edits a host with the pair its form opened on, then reads the hub's hosts again", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic", { generation: 4 })] });
		await read;
		const edit = hosts.update("attic", { address: "attic.lan" }, { generation: 4, incarnationId: "incarnation-1" });
		await settle();
		const sent = h.take("evener/host/update");
		expect(sent.params).toEqual({
			name: "attic",
			entry: { address: "attic.lan" },
			mutationId: "m1",
			expectedGeneration: 4,
			expectedIncarnationId: "incarnation-1",
		});
		expect(sent.opts).toEqual({ timeoutMs: HOST_GATE_TIMEOUT_MS });
		sent.resolve({ outcome: "committed", host: row("attic", { address: "attic.lan", generation: 5 }) });
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("attic", { address: "attic.lan", generation: 5 })] });
		await edit;
		expect(hosts.getSnapshot().rows?.[0]?.address).toBe("attic.lan");
	});

	it("hands an edit the hub found stale back to its form, never retried", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic", { generation: 2 })] });
		await read;
		const edit = hosts.update("attic", { address: "attic.lan" }, { generation: 1, incarnationId: "incarnation-1" });
		await settle();
		const sent = h.take("evener/host/update");
		expect(sent.params).toMatchObject({ expectedGeneration: 1 });
		sent.reject(new WireError("the entry moved", -32013, { evenerErrorInfo: "stale-entry" }));
		await expect(edit).rejects.toThrow("the entry moved");
		expect(h.count("evener/host/update")).toBe(0);
		expect(h.count("evener/host/list")).toBe(0);
	});

	it("hands a refused edit back to the page that asked", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic")] });
		await read;
		const edit = hosts.update("attic", { address: "" }, { generation: 1, incarnationId: "incarnation-1" });
		await settle();
		h.take("evener/host/update").reject(
			new WireError("missing ssh destination", -32602, { evenerErrorInfo: "invalidHostField", field: "address" }),
		);
		await expect(edit).rejects.toThrow("missing ssh destination");
	});

	it("drops a removed host from its rows even when the read after it fails", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic"), row("studio")] });
		await read;
		const removal = hosts.remove("attic");
		await settle();
		h.take("evener/host/remove").resolve({
			outcome: "committed",
			host: { name: "attic", generation: 1, incarnationId: "incarnation-1" },
		});
		await settle();
		h.take("evener/host/list").reject(new Error("connection lost"));
		await removal;
		expect(hosts.getSnapshot().rows?.map((candidate) => candidate.name)).toEqual(["studio"]);
	});

	it("removes the host it holds, then reads the hub's hosts again", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic")] });
		await read;
		const removal = hosts.remove("attic");
		await settle();
		const sent = h.take("evener/host/remove");
		expect(sent.params).toMatchObject({ name: "attic", expectedGeneration: 1, expectedIncarnationId: "incarnation-1" });
		sent.resolve({ outcome: "committed", host: { name: "attic", generation: 1, incarnationId: "incarnation-1" } });
		await settle();
		h.take("evener/host/list").resolve({ hosts: [] });
		await removal;
		expect(hosts.getSnapshot().rows).toEqual([]);
	});

	it("re-reads after an edit's non-commit arm, so a committed teardown failure appears", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic")] });
		await read;

		const edit = hosts.update("attic", { address: "attic.lan" }, { generation: 1, incarnationId: "incarnation-1" });
		await settle();
		h.take("evener/host/update").resolve({
			outcome: "committed-with-teardown-failure",
			seam: "rebind",
			remnantId: "r1",
			host: { ...row("attic"), address: "attic.lan" },
		});
		await settle();
		// The non-commit arm rejects, but the controller still re-reads the rows.
		expect(h.count("evener/host/list")).toBe(1);
		h.take("evener/host/list").resolve({ hosts: [{ ...row("attic"), address: "attic.lan", openRemnantId: "r1" }] });
		await expect(edit).rejects.toThrow(/remnantId r1/);
		expect(hosts.getSnapshot().rows?.[0]?.openRemnantId).toBe("r1");
	});

	it("re-reads after a removal's non-commit arm, so a committed removal appears", async () => {
		const h = hub();
		const hosts = new HostsController(h.client, ids());
		const read = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("attic"), row("studio")] });
		await read;

		const removal = hosts.remove("attic");
		await settle();
		h.take("evener/host/remove").resolve({
			outcome: "committed-with-teardown-failure",
			seam: "rebind",
			remnantId: "r1",
			host: { ...row("attic"), removed: true },
		});
		await settle();
		expect(h.count("evener/host/list")).toBe(1);
		h.take("evener/host/list").resolve({ hosts: [row("studio")] });
		await expect(removal).rejects.toThrow(/remnantId r1/);
		expect(hosts.getSnapshot().rows?.map((candidate) => candidate.name)).toEqual(["studio"]);
	});
});
