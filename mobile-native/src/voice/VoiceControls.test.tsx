import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { VoiceControls } from "./VoiceControls";
import { VoiceContext } from "./voiceContext";
import { defaultVoiceOptions, VoiceClient, type NativeVoicePort, type VoiceSnapshot } from "./voiceClient";
import activation from "../../modules/evener-voice/runtime-tests/fixtures/wake-activation.json";
import recordedTrials from "../../modules/evener-voice/runtime-tests/fixtures/wake-trials.json";
import type { WakeSnapshot, WakeTrialAttempt } from "./wakeTrials";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

it("arms in one tap with a visible disclosure, keeps takeover explicit, and sends Talk only to native", async () => {
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
		provider: "",
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
		startWakeTrial: async () => {},
		finishWakeTrial: async () => {},
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
		// voiceService.arm in cmd/evener-hub/voice_http.go returns this wire code.
		if (requests.length === 1) {
			expect(_path).toBe("/api/voice/arm");
			return { origin, status: 409, body: JSON.stringify({ code: "binding_owned" }) };
		}
		return {
			origin,
			status: 200,
			body: JSON.stringify({
				bindingId: "binding",
				epoch: "opaque",
				grant: "fixture",
				expiresAt: "2030-01-01T00:00:00Z",
				provider: "openai",
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
		button("Voice").props.onPress();
	});
	await act(async () => {
		button("Turn on").props.onPress();
	});
	// The bar's Turn on opens the panel; nothing arms until the panel's control.
	expect(requests).toHaveLength(0);
	// Arm is explained before it happens, not after.
	expect(button("I understand · Arm voice")).toBeUndefined();
	expect(tree.root.findByProps({ testID: "voice-arm-disclosure" })).toBeDefined();
	expect(JSON.stringify(tree.toJSON())).toContain("Anyone nearby, or audio playing nearby, can talk to Evener");
	await act(async () => {
		button("Turn on voice").props.onPress();
	});
	expect(requests).toHaveLength(1);
	expect(requests[0].takeover).toBe(false);
	expect(actions).toEqual([]);
	// One primary action: Talk appears once the binding is ours.
	expect(button("Talk")).toBeUndefined();
	expect(button("Use this iPhone instead")).toBeDefined();
	await act(async () => {
		button("Use this iPhone instead").props.onPress();
	});
	expect(requests[1].takeover).toBe(true);
	expect(button("Talk").props.disabled).toBe(false);
	await act(async () => {
		button("Voice & privacy").props.onPress();
	});
	expect(JSON.stringify(tree.toJSON())).toContain("openai");
	await act(async () => {
		button("Session & audio").props.onPress();
	});
	expect(JSON.stringify(tree.toJSON())).toContain("iPhone speaker");
	await act(async () => {
		button("Talk").props.onPress();
		button("Stop speaking").props.onPress();
	});
	expect(actions).toEqual(["arm", "talk", "stop"]);
	expect(requests).toHaveLength(2);
	await act(async () => {
		button("Turn voice off").props.onPress();
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

it("shows measured wake receipts and sends trial labels to the native owner", async () => {
	const actions: string[] = [];
	let emit: (state: VoiceSnapshot) => void = () => {};
	const state: VoiceSnapshot = {
		state: "quiet",
		capturing: true,
		cloudMicrophone: false,
		route: "iPhone",
		speakerConsentRequired: false,
		error: "",
		interpreted: "",
		custody: "",
		canonical: "",
		generated: "",
		bindingId: "binding",
		provider: "",
		wake: { ...activation.wake, state: "listening", trials: recordedTrials as WakeTrialAttempt[] } as WakeSnapshot,
	};
	const native: NativeVoicePort = {
		deviceId: async () => "test",
		validateTransport: async () => {},
		authorize: async () => {
			throw new Error("no HTTP in local trials");
		},
		arm: async () => {},
		talk: () => {
			throw new Error("trials must not Talk");
		},
		stopSpeaking: () => {},
		disarm: () => {},
		allowSpeaker: () => {},
		snapshot: async () => state,
		addListener: (_event, listener) => {
			emit = listener;
			return { remove() {} };
		},
		startWakeTrial: async () => {
			actions.push("start");
			emit({
				...state,
				wake: {
					...state.wake!,
					trials: [
						...(recordedTrials as WakeTrialAttempt[]),
						{ ...recordedTrials[0], running: true, label: undefined } as WakeTrialAttempt,
					],
				},
			});
		},
		finishWakeTrial: async (label) => {
			actions.push(label);
			emit(state);
		},
	};
	const client = new VoiceClient(
		native,
		async () => "",
		() => true,
	);
	const unlisten = client.listen();
	const tree = render(
		<VoiceContext.Provider value={client}>
			<VoiceControls
				target={{
					hub: { id: "hub", name: "Hub", origin: "https://example.com" },
					ref: "chief",
					instanceId: "instance",
					manager: true,
					voiceAvailable: true,
				}}
			/>
		</VoiceContext.Provider>,
	);
	const button = (label: string) =>
		tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)[0];
	await act(async () => {
		button("Voice").props.onPress();
	});
	await act(async () => {
		button("Test wake word").props.onPress();
	});
	expect(JSON.stringify(tree.toJSON())).toContain("Heard wake");
	expect(JSON.stringify(tree.toJSON())).toContain("Alexa");
	expect(
		tree.root
			.findAll((node) => String(node.type) === "Text")
			.map((node) => node.children.join(""))
			.join("\n"),
	).toContain(`Last score: ${recordedTrials.at(-1)!.lastScore.toFixed(4)}`);
	expect(button("Start test").props.disabled).toBe(false);
	for (const label of ["Intended", "Background", "False alarm"]) {
		await act(async () => {
			button("Start test").props.onPress();
		});
		expect(button("Talk").props.disabled).toBe(true);
		expect(button("Start test").props.disabled).toBe(true);
		await act(async () => {
			button(label).props.onPress();
		});
	}
	expect(actions).toEqual(["start", "positive", "start", "negative", "start", "falseActivation"]);
	unlisten();
});

it("keeps Stop live while Evener speaks", async () => {
	const actions: string[] = [];
	let emit: (state: VoiceSnapshot) => void = () => {};
	const armed: VoiceSnapshot = {
		state: "quiet",
		capturing: true,
		cloudMicrophone: true,
		route: "iPhone speaker",
		speakerConsentRequired: false,
		error: "",
		interpreted: "",
		custody: "",
		canonical: "",
		generated: "",
		bindingId: "binding",
		provider: "openai",
	};
	const native: NativeVoicePort = {
		deviceId: async () => "install",
		validateTransport: async () => {},
		authorize: async (origin) => ({
			origin,
			status: 200,
			body: JSON.stringify({
				bindingId: "binding",
				epoch: "opaque",
				grant: "fixture",
				expiresAt: "2030-01-01T00:00:00Z",
				provider: "openai",
			}),
		}),
		arm: async () => {},
		snapshot: async () => armed,
		addListener: (_event, listener) => {
			emit = listener;
			return { remove() {} };
		},
		talk: () => actions.push("talk"),
		stopSpeaking: () => actions.push("stop"),
		disarm: () => {},
		allowSpeaker: () => {},
		startWakeTrial: async () => {},
		finishWakeTrial: async () => {},
	};
	const client = new VoiceClient(
		native,
		async () => "",
		() => true,
	);
	client.listen();
	const target = {
		hub: { id: "hub", name: "Hub", origin: "http://127.0.0.1:9180" },
		ref: "chief",
		instanceId: "instance",
		manager: true,
		voiceAvailable: true,
	};
	const tree = render(
		<VoiceContext.Provider value={client}>
			<VoiceControls target={target} />
		</VoiceContext.Provider>,
	);
	const button = (label: string) =>
		tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)[0];
	await act(async () => {
		await client.arm(target, defaultVoiceOptions, false);
	});
	await act(async () => {
		emit({ ...armed, state: "speaking" });
	});
	expect(button("Stop").props.disabled).toBe(false);
	await act(async () => {
		button("Stop").props.onPress();
	});
	expect(actions).toEqual(["stop"]);
	// Stop leaves native conversing with no live turn; Talk must be the way out.
	await act(async () => {
		emit({ ...armed, state: "conversing", cloudMicrophone: false });
	});
	expect(button("Talk").props.disabled).toBe(false);
	// Nothing is streaming, so the bar must not claim to be listening.
	expect(JSON.stringify(tree.toJSON())).toContain("Tap Talk to reply");
	await act(async () => {
		emit({ ...armed, state: "conversing", cloudMicrophone: true });
	});
	expect(JSON.stringify(tree.toJSON())).toContain("Listening to you");
});

it("calls a routine abort a finished turn, not a stopped voice", async () => {
	const quietWithAbort: VoiceSnapshot = {
		state: "quiet",
		capturing: true,
		cloudMicrophone: false,
		route: "iPhone speaker",
		speakerConsentRequired: false,
		error: "input_idle",
		interpreted: "",
		custody: "",
		canonical: "",
		generated: "",
		bindingId: "binding",
		provider: "openai",
	};
	const native: NativeVoicePort = {
		deviceId: async () => "install",
		validateTransport: async () => {},
		authorize: async () => {
			throw new Error("unused");
		},
		arm: async () => {},
		snapshot: async () => quietWithAbort,
		addListener: () => ({ remove() {} }),
		talk: () => {},
		stopSpeaking: () => {},
		disarm: () => {},
		allowSpeaker: () => {},
		startWakeTrial: async () => {},
		finishWakeTrial: async () => {},
	};
	const client = new VoiceClient(
		native,
		async () => "",
		() => true,
	);
	client.listen();
	const tree = render(
		<VoiceContext.Provider value={client}>
			<VoiceControls
				target={{
					hub: { id: "hub", name: "Hub", origin: "http://127.0.0.1:9180" },
					ref: "chief",
					instanceId: "instance",
					manager: true,
					voiceAvailable: true,
				}}
			/>
		</VoiceContext.Provider>,
	);
	const button = (label: string) =>
		tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)[0];
	await act(async () => {
		button("Voice").props.onPress();
	});
	const text = tree.root
		.findAll((node) => String(node.type) === "Text")
		.map((node) => node.children.join(""))
		.join("\n");
	expect(text).toContain("Last turn ended (input_idle)");
	expect(text).not.toContain("Voice stopped");
	// The footnote describes the state, not a state that no longer holds.
	expect(text).toContain("until you talk");
});

it("names the session voice is on for when the bar belongs to another one", async () => {
	const armedElsewhere: VoiceSnapshot = {
		state: "quiet",
		capturing: true,
		cloudMicrophone: true,
		route: "iPhone speaker",
		speakerConsentRequired: false,
		error: "",
		interpreted: "",
		custody: "",
		canonical: "",
		generated: "",
		bindingId: "binding",
		provider: "openai",
	};
	const native: NativeVoicePort = {
		deviceId: async () => "install",
		validateTransport: async () => {},
		authorize: async (origin) => ({
			origin,
			status: 200,
			body: JSON.stringify({
				bindingId: "binding",
				epoch: "opaque",
				grant: "fixture",
				expiresAt: "2030-01-01T00:00:00Z",
				provider: "openai",
			}),
		}),
		arm: async () => {},
		snapshot: async () => armedElsewhere,
		addListener: () => ({ remove() {} }),
		talk: () => {},
		stopSpeaking: () => {},
		disarm: () => {},
		allowSpeaker: () => {},
		startWakeTrial: async () => {},
		finishWakeTrial: async () => {},
	};
	const client = new VoiceClient(
		native,
		async () => "",
		() => true,
	);
	const first = {
		hub: { id: "hub", name: "Hub", origin: "http://127.0.0.1:9180" },
		ref: "chief",
		instanceId: "instance",
		manager: true,
		voiceAvailable: true,
	};
	await client.arm(first, defaultVoiceOptions, false);
	const tree = render(
		<VoiceContext.Provider value={client}>
			<VoiceControls target={{ ...first, ref: "other" }} />
		</VoiceContext.Provider>,
	);
	const button = (label: string) =>
		tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)[0];
	expect(JSON.stringify(tree.toJSON())).toContain("Voice is on for chief");
	expect(button("Talk").props.disabled).toBe(true);
});
