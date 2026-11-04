import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { VoiceControls } from "./VoiceControls";
import { VoiceContext } from "./voiceContext";
import { VoiceClient, type NativeVoicePort, type VoiceSnapshot } from "./voiceClient";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

it("requires disclosure consent, keeps takeover explicit, and sends Talk only to native", async () => {
	const actions: string[] = [];
	const requests: Record<string, unknown>[] = [];
	let state: VoiceSnapshot = {
		state: "disarmed",
		capturing: false,
		cloudMicrophone: false,
		route: "",
		speakerConsentRequired: false,
		error: "",
		interpreted: "",
		custody: "",
		canonical: "",
		generated: "",
		bindingId: "",
	};
	const native: NativeVoicePort = {
		deviceId: async () => "install",
		validateTransport: async () => {},
		authorize: async () => {
			throw new Error("Unconfigured HTTP boundary");
		},
		arm: async () => {
			actions.push("arm");
			state = { ...state, state: "quiet", capturing: true, bindingId: "binding", route: "iPhone speaker" };
		},
		snapshot: async () => state,
		addListener: () => ({ remove() {} }),
		talk: () => {
			actions.push("talk");
		},
		stopSpeaking: () => {
			actions.push("stop");
		},
		disarm: () => {
			actions.push("disarm");
		},
		allowSpeaker: () => {},
	};
	const target = {
		hub: { id: "hub", name: "Hub", origin: "http://127.0.0.1:9180" },
		ref: "local:chief",
		instanceId: "instance",
		manager: true,
		voiceAvailable: true,
	};
	native.authorize = async (origin, _path, _token, body) => {
		requests.push(JSON.parse(body));
		if (requests.length === 1) return { origin, status: 409, body: JSON.stringify({ code: "binding_busy" }) };
		return {
			origin,
			status: 200,
			body: JSON.stringify({
				bindingId: "binding",
				epoch: "opaque",
				grant: "fixture",
				expiresAt: "2030-01-01T00:00:00Z",
			}),
		};
	};
	const client = new VoiceClient(
		native,
		async () => "fixture",
		() => true,
	);
	const tree = render(
		<VoiceContext.Provider value={client}>
			<VoiceControls target={target} />
		</VoiceContext.Provider>,
	);
	const button = (label: string) =>
		tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)[0];
	await act(async () => {
		button("Voice · Talk preview").props.onPress();
	});
	await act(async () => {
		button("Arm").props.onPress();
	});
	expect(requests).toHaveLength(0);
	expect(JSON.stringify(tree.toJSON())).toContain("nearby people or recordings");
	expect(JSON.stringify(tree.toJSON())).toContain("Wake activation is unavailable");
	await act(async () => {
		button("I understand · Arm voice").props.onPress();
	});
	expect(requests).toHaveLength(1);
	expect(requests[0].takeover).toBe(false);
	await act(async () => {
		button("Take over the other armed device").props.onPress();
	});
	expect(requests[1].takeover).toBe(true);
	expect(button("Talk").props.disabled).toBe(false);
	await act(async () => {
		button("Talk").props.onPress();
		button("Stop speaking").props.onPress();
	});
	expect(actions).toEqual(["arm", "talk", "stop"]);
	expect(requests).toHaveLength(2);
	await act(async () => {
		button("Disarm").props.onPress();
	});
	expect(actions).toEqual(["arm", "talk", "stop", "disarm"]);
});

it("adds no controls when the hub does not advertise voice", () => {
	const tree = render(
		<VoiceControls
			target={{
				hub: { id: "hub", name: "Hub", origin: "https://example.com" },
				ref: "local:chief",
				instanceId: "instance",
				manager: true,
				voiceAvailable: false,
			}}
		/>,
	);
	expect(tree.toJSON()).toBeNull();
});
