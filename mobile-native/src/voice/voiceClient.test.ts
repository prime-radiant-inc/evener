import { expect, it } from "vitest";
import { VoiceClient, VoiceError, type NativeVoicePort, type VoiceSnapshot } from "./voiceClient";

const state: VoiceSnapshot = {
	state: "quiet",
	capturing: true,
	cloudMicrophone: false,
	route: "builtInSpeaker",
	speakerConsentRequired: false,
	error: "",
	interpreted: "",
	custody: "",
	canonical: "",
	generated: "",
	bindingId: "binding",
};
const target = {
	hub: { id: "hub", name: "Hub", origin: "http://127.0.0.1:9180" },
	ref: "local:chief",
	instanceId: "instance",
	manager: true,
	voiceAvailable: true,
};
function device() {
	const actions: string[] = [];
	let configuration = "";
	const native: NativeVoicePort = {
		deviceId: async () => "install",
		validateTransport: async () => {},
		authorize: async () => {
			throw new Error("Unconfigured HTTP boundary");
		},
		arm: async (value) => {
			configuration = value;
			actions.push("arm");
		},
		talk: () => {
			actions.push("talk");
		},
		stopSpeaking: () => {
			actions.push("stopSpeaking");
		},
		disarm: () => {
			actions.push("disarm");
		},
		allowSpeaker: () => {
			actions.push("allowSpeaker");
		},
		snapshot: async () => state,
		addListener: () => ({ remove() {} }),
	};
	return { native, actions, configuration: () => JSON.parse(configuration) };
}
function makeClient(
	native: NativeVoicePort,
	token: (id: string) => Promise<string>,
	foreground: () => boolean,
	request: typeof fetch,
) {
	return new VoiceClient(
		{
			...native,
			authorize: async (origin, path, token, body) => {
				const response = await request(`${origin}${path}`, {
					method: "POST",
					headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
					body,
				});
				return { origin, status: response.status, body: await response.text() };
			},
		},
		token,
		foreground,
	);
}
const grant = {
	bindingId: "binding",
	epoch: "opaque",
	grant: "narrow-fixture",
	expiresAt: "2030-01-01T00:00:00Z",
	socketPath: "/api/voice/socket",
	sampleRate: 24000,
	policy: { trailingSilenceMs: 600, maxUtteranceMs: 30000, followupMs: 12000, idleInputMs: 30000 },
};

it("keeps the profile hostname but pins socket and recovery to the issuance address", async () => {
	const hardware = device();
	const origins: string[] = [];
	hardware.native.authorize = async (origin) => {
		origins.push(origin);
		return { origin: "http://100.100.1.2:9180", status: 200, body: JSON.stringify(grant) };
	};
	const named = { ...target, hub: { ...target.hub, origin: "http://magic-kingdom:9180" } };
	const client = new VoiceClient(
		hardware.native,
		async () => "broad-fixture",
		() => true,
	);
	await client.arm(named);
	expect(hardware.configuration().origin).toBe("http://100.100.1.2:9180");
	expect(client.getTarget()?.hub.origin).toBe("http://magic-kingdom:9180");
	await client.disarm();
	expect(origins).toEqual(["http://magic-kingdom:9180", "http://100.100.1.2:9180"]);
});

it("allows a new Arm after native grant expiry", async () => {
	const hardware = device();
	let emit: ((value: VoiceSnapshot) => void) | undefined;
	hardware.native.addListener = (_event, listener) => {
		emit = listener;
		return { remove() {} };
	};
	const client = makeClient(
		hardware.native,
		async () => "broad-fixture",
		() => true,
		async () => Response.json(grant),
	);
	const unlisten = client.listen();
	await client.arm(target);
	emit?.({ ...state, state: "disarmed", bindingId: "", error: "grant_expired" });
	await client.arm(target);
	expect(hardware.actions).toEqual(["arm", "arm"]);
	unlisten();
});

it("does not send broad recovery auth after entering background during token read", async () => {
	const hardware = device();
	let foreground = true;
	let reads = 0;
	let requests = 0;
	const client = makeClient(
		hardware.native,
		async () => {
			if (++reads === 2) foreground = false;
			return "broad-fixture";
		},
		() => foreground,
		async () => {
			requests++;
			return Response.json(grant);
		},
	);
	await client.arm(target);
	await client.disarm();
	expect(hardware.actions).toEqual(["arm", "disarm"]);
	expect(requests).toBe(1);
});

it("arms only an advertised manager and sends no RPC or PCM through foreground auth", async () => {
	const hardware = device();
	const requests: Request[] = [];
	const request: typeof fetch = async (url, init) => {
		requests.push(new Request(url, init));
		return Response.json(grant);
	};
	const client = makeClient(
		hardware.native,
		async () => "broad-fixture",
		() => true,
		request,
	);
	await expect(client.arm({ ...target, voiceAvailable: false })).rejects.toMatchObject({ code: "voice_unavailable" });
	await expect(client.arm({ ...target, manager: false })).rejects.toMatchObject({ code: "manager_required" });
	expect(requests).toHaveLength(0);
	await client.arm(target);
	expect(requests).toHaveLength(1);
	expect(await requests[0].json()).toEqual({
		ref: "local:chief",
		expectedInstanceId: "instance",
		deviceId: "install",
		provider: "openai",
		model: "gpt-realtime-2.1",
		voice: "marin",
		takeover: false,
	});
	expect(hardware.configuration()).toMatchObject({ grant: "narrow-fixture", origin: target.hub.origin });
	expect(hardware.configuration()).not.toHaveProperty("token");
	expect(client.getSnapshot()).not.toHaveProperty("grant");
	client.talk();
	client.stopSpeaking();
	expect(requests).toHaveLength(1);
	expect(hardware.actions).toEqual(["arm", "talk", "stopSpeaking"]);
	await client.disarm();
	expect(new URL(requests[1].url).pathname).toBe("/api/voice/disarm");
	expect(await requests[1].json()).toEqual({ bindingId: "binding", epoch: "opaque" });
});

it("keeps busy takeover explicit and refuses background broad credential reads", async () => {
	const hardware = device();
	let foreground = false;
	let reads = 0;
	let requests = 0;
	const client = makeClient(
		hardware.native,
		async () => {
			reads++;
			return "broad-fixture";
		},
		() => foreground,
		async () => {
			requests++;
			return Response.json({ code: "binding_owned" }, { status: 409 });
		},
	);
	await expect(client.arm(target)).rejects.toMatchObject({ code: "foreground_required" });
	expect(reads).toBe(0);
	foreground = true;
	await expect(client.arm(target)).rejects.toMatchObject({ code: "binding_owned" });
	expect(requests).toBe(1);
	expect(hardware.actions).toEqual([]);
});

it.each([
	{ status: 502, body: "<html>Bad Gateway</html>" },
	{ status: 401, body: "" },
])("maps an invalid JSON $status arm response to VoiceError and allows retry", async ({ status, body }) => {
	const hardware = device();
	let requests = 0;
	const client = makeClient(
		hardware.native,
		async () => "broad-fixture",
		() => true,
		async () => (++requests === 1 ? new Response(body, { status }) : Response.json(grant)),
	);
	const pending = client.arm(target);
	await expect(pending).rejects.toBeInstanceOf(VoiceError);
	await expect(pending).rejects.toMatchObject({ code: "arm_failed" });
	expect(requests).toBe(1);
	expect(hardware.actions).toEqual([]);
	expect(client.getTarget()).toBeNull();
	await client.arm(target);
	expect(requests).toBe(2);
	expect(hardware.actions).toEqual(["arm"]);
	expect(hardware.configuration()).toEqual({ ...grant, origin: target.hub.origin, deviceId: "install" });
	expect(client.getTarget()).toEqual(target);
});

it("revokes a late arm response after logout instead of starting microphone", async () => {
	const hardware = device();
	let resolve: ((value: Response) => void) | undefined;
	const waiting = new Promise<Response>((done) => {
		resolve = done;
	});
	let requested: (() => void) | undefined;
	const started = new Promise<void>((done) => {
		requested = done;
	});
	const requests: string[] = [];
	const client = makeClient(
		hardware.native,
		async () => "broad-fixture",
		() => true,
		async (url) => {
			requests.push(String(url));
			if (String(url).endsWith("/arm")) {
				requested?.();
				return waiting;
			}
			return Response.json({});
		},
	);
	const pending = client.arm(target);
	await started;
	client.retainHub(null);
	resolve?.(Response.json(grant));
	await expect(pending).rejects.toMatchObject({ code: "arm_cancelled" });
	expect(hardware.actions).toEqual(["disarm"]);
	expect(requests.map((url) => new URL(url).pathname)).toEqual(["/api/voice/arm", "/api/voice/disarm"]);
});

it("cancels pending arm when the foreground changes to a different hub", async () => {
	const hardware = device();
	let resolve: ((value: Response) => void) | undefined;
	const waiting = new Promise<Response>((done) => {
		resolve = done;
	});
	let requested: (() => void) | undefined;
	const started = new Promise<void>((done) => {
		requested = done;
	});
	const client = makeClient(
		hardware.native,
		async () => "broad-fixture",
		() => true,
		async (url) => {
			if (String(url).endsWith("/arm")) {
				requested?.();
				return waiting;
			}
			return Response.json({});
		},
	);
	const pending = client.arm(target);
	await started;
	client.retainHub("another-hub");
	resolve?.(Response.json(grant));
	await expect(pending).rejects.toMatchObject({ code: "arm_cancelled" });
	expect(hardware.actions).toEqual(["disarm"]);
});
