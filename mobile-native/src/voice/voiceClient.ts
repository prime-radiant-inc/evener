import type { HubProfile } from "../connection";
import type { WakeSnapshot, WakeTrialLabel } from "./wakeTrials";

export interface VoiceSnapshot {
	state: "disarmed" | "recovering" | "quiet" | "conversing" | "speaking";
	capturing: boolean;
	cloudMicrophone: boolean;
	route: string;
	speakerConsentRequired: boolean;
	error: string;
	interpreted: string;
	custody: string;
	canonical: string;
	generated: string;
	bindingId: string;
	/** The hub's voice provider instance chosen for this binding; empty until armed. */
	provider: string;
	wake?: WakeSnapshot;
}
export interface NativeVoicePort {
	deviceId(): Promise<string>;
	validateTransport(origin: string): Promise<void>;
	authorize(
		origin: string,
		path: string,
		token: string,
		body: string,
	): Promise<{ origin: string; status: number; body: string }>;
	arm(configuration: string): Promise<void>;
	talk(): void;
	stopSpeaking(): void;
	disarm(): void;
	allowSpeaker(): void;
	startWakeTrial(): Promise<void>;
	finishWakeTrial(label: WakeTrialLabel): Promise<void>;
	snapshot(): Promise<VoiceSnapshot>;
	addListener(event: "state", listener: (state: VoiceSnapshot) => void): { remove(): void };
}
export interface VoiceTarget {
	hub: HubProfile;
	ref: string;
	instanceId: string;
	manager: boolean;
	voiceAvailable: boolean;
}
export interface VoiceOptions {
	provider: string;
	model: string;
	voice: string;
}
export interface VoiceSupport {
	supported: boolean;
	reason: string;
}
export const defaultVoiceOptions: VoiceOptions = { provider: "", model: "gpt-realtime-2.1", voice: "marin" };
export class VoiceError extends Error {
	constructor(readonly code: string) {
		super(code);
	}
}
interface Grant {
	bindingId: string;
	epoch: string;
	grant: string;
	expiresAt: string;
	socketPath: string;
	sampleRate: number;
	policy: Record<string, number>;
	provider?: string;
}
const disarmed: VoiceSnapshot = {
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

/** Foreground authorization only. PCM and grants never enter this owner's observable state or an outbox. */
export class VoiceClient {
	private value = disarmed;
	private listeners = new Set<() => void>();
	private selection: VoiceTarget | null = null;
	private binding: { hub: HubProfile; origin: string; bindingId: string; epoch: string } | null = null;
	private providerName = "";
	private operation = 0;
	private pending = false;
	private pendingHub: string | null = null;
	constructor(
		private native: NativeVoicePort,
		private token: (hubId: string) => Promise<string>,
		private foreground: () => boolean,
	) {}
	getSnapshot = () => this.value;
	getTarget = () => this.selection;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private publish(value: VoiceSnapshot) {
		if (value.state === "disarmed") {
			this.binding = null;
			this.selection = null;
			this.providerName = "";
		}
		this.value = { ...value, provider: this.providerName };
		for (const listener of this.listeners) listener();
	}
	listen() {
		let live = true;
		let changed = false;
		const listener = this.native.addListener("state", (state) => {
			changed = true;
			this.publish(state);
		});
		void this.native.snapshot().then((state) => {
			if (live && !changed) this.publish(state);
		});
		return () => {
			live = false;
			listener.remove();
		};
	}
	async arm(target: VoiceTarget, options: VoiceOptions = defaultVoiceOptions, takeover = false) {
		if (!this.foreground()) throw new VoiceError("foreground_required");
		if (!target.voiceAvailable) throw new VoiceError("voice_unavailable");
		if (!target.manager || !target.instanceId) throw new VoiceError("manager_required");
		if (this.binding || this.pending) throw new VoiceError("already_armed");
		const operation = ++this.operation;
		this.pending = true;
		this.pendingHub = target.hub.id;
		let granted: Grant | null = null;
		let pinnedOrigin = target.hub.origin;
		try {
			await this.native.validateTransport(target.hub.origin);
			const deviceId = await this.native.deviceId();
			const token = await this.token(target.hub.id);
			if (!this.foreground() || operation !== this.operation) throw new VoiceError("arm_cancelled");
			const response = await this.native.authorize(
				target.hub.origin,
				"/api/voice/arm",
				token,
				JSON.stringify({ ref: target.ref, expectedInstanceId: target.instanceId, deviceId, ...options, takeover }),
			);
			pinnedOrigin = response.origin;
			let result;
			try {
				result = JSON.parse(response.body);
			} catch {
				throw new VoiceError("arm_failed");
			}
			if (response.status < 200 || response.status >= 300)
				throw new VoiceError(result.code ?? result.error?.code ?? "arm_failed");
			granted = result as Grant;
			this.providerName = typeof granted.provider === "string" ? granted.provider : "";
			if (operation !== this.operation || !this.foreground()) throw new VoiceError("arm_cancelled");
			await this.native.arm(JSON.stringify({ ...granted, origin: pinnedOrigin, deviceId }));
			if (operation !== this.operation || !this.foreground()) {
				this.native.disarm();
				throw new VoiceError("arm_cancelled");
			}
			this.binding = { hub: target.hub, origin: pinnedOrigin, bindingId: granted.bindingId, epoch: granted.epoch };
			this.selection = target;
			const snapshot = await this.native.snapshot();
			if (operation === this.operation) this.publish(snapshot);
		} catch (error) {
			if (granted)
				await this.revoke({
					hub: target.hub,
					origin: pinnedOrigin,
					bindingId: granted.bindingId,
					epoch: granted.epoch,
				});
			throw error;
		} finally {
			if (operation === this.operation) {
				this.pending = false;
				this.pendingHub = null;
			}
		}
	}
	/** Whether this session can run voice right now. The hub answers from the
	 * admission evidence Arm uses, so a session that would refuse keeps the
	 * control hidden. A hub that predates the preflight keeps the old behavior. */
	async status(target: VoiceTarget): Promise<VoiceSupport> {
		if (!target.manager || !target.instanceId) return { supported: false, reason: "manager_required" };
		const token = await this.token(target.hub.id);
		const response = await this.native.authorize(
			target.hub.origin,
			"/api/voice/status",
			token,
			JSON.stringify({ ref: target.ref, expectedInstanceId: target.instanceId }),
		);
		if (response.status === 404) return { supported: true, reason: "" };
		let result: { supported?: unknown; reason?: unknown; code?: unknown } = {};
		try {
			result = JSON.parse(response.body);
		} catch {
			// An unreadable body answers the same way a missing reason does.
		}
		if (response.status < 200 || response.status >= 300)
			return { supported: false, reason: typeof result.code === "string" ? result.code : "status_unavailable" };
		return { supported: result.supported === true, reason: typeof result.reason === "string" ? result.reason : "" };
	}
	private async revoke(binding: { hub: HubProfile; origin: string; bindingId: string; epoch: string }) {
		if (!this.foreground()) return;
		const token = await this.token(binding.hub.id);
		if (!this.foreground()) return;
		const response = await this.native.authorize(
			binding.origin,
			"/api/voice/disarm",
			token,
			JSON.stringify({ bindingId: binding.bindingId, epoch: binding.epoch }),
		);
		if (response.status < 200 || response.status >= 300) throw new VoiceError("disarm_unconfirmed");
	}
	talk() {
		this.native.talk();
	}
	stopSpeaking() {
		this.native.stopSpeaking();
	}
	allowSpeaker() {
		this.native.allowSpeaker();
	}
	async startWakeTrial() {
		await this.native.startWakeTrial();
	}
	async finishWakeTrial(label: WakeTrialLabel) {
		await this.native.finishWakeTrial(label);
	}
	async disarm() {
		++this.operation;
		this.pending = false;
		this.pendingHub = null;
		this.native.disarm();
		const binding = this.binding;
		this.binding = null;
		this.selection = null;
		this.publish({ ...disarmed, wake: this.value.wake });
		if (binding) await this.revoke(binding);
	}
	/** Hub removal/logout retires pending arm as well as an existing binding. Background alone does not. */
	retainHub(hubId: string | null) {
		if ((this.selection && this.selection.hub.id !== hubId) || (this.pending && this.pendingHub !== hubId))
			void this.disarm().catch(() => {});
	}
}
