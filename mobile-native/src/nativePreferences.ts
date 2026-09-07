import type {
	AnyNotification,
	FeatureSet,
	KeybindingsOverrides,
	KeybindingsRule,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { TranscriptDisplayConfigV1 } from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import {
	fromWireConfig,
	normalizeConfig,
	toWireConfig,
} from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

type NativeFeatures = Pick<
	FeatureSet,
	"keybindingsSettings" | "transcriptDisplaySettings"
>;
type Support = "unknown" | "supported" | "unsupported";

export interface PreferenceState<T> {
	support: Support;
	loading: boolean;
	saving: boolean;
	confirmed: T | null;
	draft: T | null;
	error: string | null;
	conflict: boolean;
	writeUncertain: boolean;
}

export interface NativePreferencesSnapshot {
	keybindings: PreferenceState<KeybindingsOverrides>;
	transcriptMobile: PreferenceState<{
		revision: number;
		config: TranscriptDisplayConfigV1;
	}>;
}

const initialDomain = <T>(): PreferenceState<T> => ({
	support: "unknown",
	loading: false,
	saving: false,
	confirmed: null,
	draft: null,
	error: null,
	conflict: false,
	writeUncertain: false,
});

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
	return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function isRule(value: unknown): value is KeybindingsRule {
	return (
		isRecord(value) &&
		typeof value.action === "string" &&
		(value.chord === null || typeof value.chord === "string")
	);
}

function decodeKeybindings(value: unknown): KeybindingsOverrides {
	if (
		!isRecord(value) ||
		value.version !== 1 ||
		!isRevision(value.revision) ||
		!Array.isArray(value.rules) ||
		!value.rules.every(isRule)
	)
		throw new Error("Hub returned invalid keybinding settings.");
	if (value.loadError !== undefined && typeof value.loadError !== "string")
		throw new Error("Hub returned invalid keybinding settings.");
	return {
		version: 1,
		revision: value.revision,
		rules: value.rules,
		...(value.loadError === undefined ? {} : { loadError: value.loadError }),
	};
}

function decodeTranscript(value: unknown) {
	if (!isRecord(value) || !isRecord(value.mobile) || !isRecord(value.desktop))
		throw new Error("Hub returned invalid transcript display settings.");
	const decode = (entry: unknown) => {
		if (!isRecord(entry) || !isRevision(entry.revision))
			throw new Error("Hub returned invalid transcript display settings.");
		const config = fromWireConfig(entry.config);
		if (config === undefined)
			throw new Error("Hub returned invalid transcript display settings.");
		return { revision: entry.revision, config };
	};
	return { desktop: decode(value.desktop), mobile: decode(value.mobile) };
}

function decodeTranscriptPatch(value: unknown): {
	revision: number;
	config: TranscriptDisplayConfigV1;
} {
	if (!isRecord(value) || !isRevision(value.revision))
		throw new Error("Hub returned invalid transcript display settings.");
	const config = fromWireConfig(value.config);
	if (config === undefined)
		throw new Error("Hub returned invalid transcript display settings.");
	return { revision: value.revision, config };
}

function errorText(error: unknown): string {
	return error instanceof Error
		? error.message
		: "The hub request could not be confirmed.";
}

export class NativePreferences {
	private state: NativePreferencesSnapshot = {
		keybindings: initialDomain<KeybindingsOverrides>(),
		transcriptMobile: initialDomain<{
			revision: number;
			config: TranscriptDisplayConfigV1;
		}>(),
	};
	private readonly listeners = new Set<() => void>();
	private readonly client: ConversationClientLike;
	private readonly unsubscribe: () => void;
	private generation = 0;
	private disposed = false;

	constructor(client: ConversationClientLike, features: NativeFeatures) {
		this.client = client;
		this.state = {
			keybindings: {
				...this.state.keybindings,
				support:
					features.keybindingsSettings === true ? "supported" : "unsupported",
			},
			transcriptMobile: {
				...this.state.transcriptMobile,
				support:
					features.transcriptDisplaySettings === true
						? "supported"
						: "unsupported",
			},
		};
		this.unsubscribe = client.onNotification((notification) =>
			this.onNotification(notification),
		);
	}

	getSnapshot = (): NativePreferencesSnapshot => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	private publish(change: Partial<NativePreferencesSnapshot>): void {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}

	private onNotification(notification: AnyNotification): void {
		if (this.disposed) return;
		if (
			notification.method === "evener/settings/keybindings/changed" &&
			this.state.keybindings.support === "supported"
		) {
			try {
				const value = decodeKeybindings(notification.params);
				const current = this.state.keybindings.confirmed;
				if (current && value.revision < current.revision) return;
				const pending =
					this.state.keybindings.saving ||
					this.state.keybindings.writeUncertain;
				this.publish({
					keybindings: {
						...this.state.keybindings,
						confirmed: value,
						draft: pending ? this.state.keybindings.draft : null,
						error: pending ? this.state.keybindings.error : null,
						conflict: pending,
						writeUncertain: this.state.keybindings.writeUncertain,
					},
				});
			} catch {
				this.publish({
					keybindings: {
						...this.state.keybindings,
						error:
							"Keybinding settings changed. Refresh to inspect the current value.",
					},
				});
			}
		}
		if (
			notification.method === "evener/settings/transcriptDisplay/changed" &&
			this.state.transcriptMobile.support === "supported"
		) {
			const params = notification.params;
			if (!isRecord(params) || params.layout !== "mobile") return;
			try {
				const value = decodeTranscriptPatch(params);
				const current = this.state.transcriptMobile.confirmed;
				if (current && value.revision < current.revision) return;
				const pending =
					this.state.transcriptMobile.saving ||
					this.state.transcriptMobile.writeUncertain;
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						confirmed: value,
						draft: pending ? this.state.transcriptMobile.draft : null,
						error: pending ? this.state.transcriptMobile.error : null,
						conflict: pending,
						writeUncertain: this.state.transcriptMobile.writeUncertain,
					},
				});
			} catch {
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						error:
							"Transcript display settings changed. Refresh to inspect the current value.",
					},
				});
			}
		}
	}

	async refresh(): Promise<void> {
		if (this.disposed) return;
		const generation = ++this.generation;
		const reads: Promise<void>[] = [];
		if (this.state.keybindings.support === "supported")
			reads.push(this.refreshKeybindings(generation));
		if (this.state.transcriptMobile.support === "supported")
			reads.push(this.refreshTranscript(generation));
		await Promise.all(reads);
	}

	private async refreshKeybindings(generation: number): Promise<void> {
		this.publish({
			keybindings: { ...this.state.keybindings, loading: true, error: null },
		});
		try {
			const value = decodeKeybindings(
				await this.client.request("evener/settings/keybindings/get", {}),
			);
			if (generation !== this.generation || this.disposed) return;
			if (
				this.state.keybindings.saving ||
				(this.state.keybindings.confirmed?.revision ?? -1) > value.revision
			) {
				this.publish({
					keybindings: { ...this.state.keybindings, loading: false },
				});
				return;
			}
			this.publish({
				keybindings: {
					...this.state.keybindings,
					loading: false,
					confirmed: value,
					draft: null,
					error: null,
					conflict: false,
					writeUncertain: false,
				},
			});
		} catch (error) {
			if (generation === this.generation && !this.disposed)
				this.publish({
					keybindings: {
						...this.state.keybindings,
						loading: false,
						error: errorText(error),
					},
				});
		}
	}

	private async refreshTranscript(generation: number): Promise<void> {
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				loading: true,
				error: null,
			},
		});
		try {
			const value = decodeTranscript(
				await this.client.request("evener/settings/transcriptDisplay/get", {}),
			);
			if (generation !== this.generation || this.disposed) return;
			if (
				this.state.transcriptMobile.saving ||
				(this.state.transcriptMobile.confirmed?.revision ?? -1) >
					value.mobile.revision
			) {
				this.publish({
					transcriptMobile: { ...this.state.transcriptMobile, loading: false },
				});
				return;
			}
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					loading: false,
					confirmed: value.mobile,
					draft: null,
					error: null,
					conflict: false,
					writeUncertain: false,
				},
			});
		} catch (error) {
			if (generation === this.generation && !this.disposed)
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						loading: false,
						error: errorText(error),
					},
				});
		}
	}

	async saveKeybindings(
		rules: readonly KeybindingsRule[],
	): Promise<KeybindingsOverrides> {
		const current = this.state.keybindings.confirmed;
		if (
			this.disposed ||
			this.state.keybindings.saving ||
			this.state.keybindings.writeUncertain ||
			current?.loadError ||
			this.state.keybindings.support !== "supported" ||
			current === null
		)
			throw new Error("Hub keybinding settings are unavailable.");
		const draft = { version: 1, revision: current.revision, rules: [...rules] };
		this.publish({
			keybindings: {
				...this.state.keybindings,
				saving: true,
				draft,
				error: null,
				conflict: false,
			},
		});
		try {
			const value = decodeKeybindings(
				await this.client.request("evener/settings/keybindings/patch", {
					expectedRevision: current.revision,
					config: { version: 1, rules: [...rules] },
				}),
			);
			if (this.disposed) return value;
			const latest = this.state.keybindings.confirmed;
			if (latest && value.revision < latest.revision) {
				this.publish({
					keybindings: {
						...this.state.keybindings,
						saving: false,
						conflict: true,
						writeUncertain: true,
					},
				});
				return value;
			}
			this.publish({
				keybindings: {
					...this.state.keybindings,
					saving: false,
					confirmed: value,
					draft: null,
					error: null,
					conflict: false,
					writeUncertain: false,
				},
			});
			return value;
		} catch (error) {
			this.publish({
				keybindings: {
					...this.state.keybindings,
					saving: false,
					error: errorText(error),
					conflict: true,
					writeUncertain: true,
				},
			});
			throw error;
		}
	}

	async saveTranscript(
		config: TranscriptDisplayConfigV1,
	): Promise<{ revision: number; config: TranscriptDisplayConfigV1 }> {
		const current = this.state.transcriptMobile.confirmed;
		if (
			this.disposed ||
			this.state.transcriptMobile.saving ||
			this.state.transcriptMobile.writeUncertain ||
			this.state.transcriptMobile.support !== "supported" ||
			current === null
		)
			throw new Error("Hub transcript display settings are unavailable.");
		const normalized = normalizeConfig(config);
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				saving: true,
				draft: { revision: current.revision, config: normalized },
				error: null,
				conflict: false,
			},
		});
		try {
			const value = decodeTranscriptPatch(
				await this.client.request("evener/settings/transcriptDisplay/patch", {
					layout: "mobile",
					expectedRevision: current.revision,
					config: toWireConfig(normalized),
				}),
			);
			if (this.disposed) return value;
			const latest = this.state.transcriptMobile.confirmed;
			if (latest && value.revision < latest.revision) {
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						saving: false,
						conflict: true,
						writeUncertain: true,
					},
				});
				return value;
			}
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					saving: false,
					confirmed: value,
					draft: null,
					error: null,
					conflict: false,
					writeUncertain: false,
				},
			});
			return value;
		} catch (error) {
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					saving: false,
					error: errorText(error),
					conflict: true,
					writeUncertain: true,
				},
			});
			throw error;
		}
	}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		this.generation += 1;
		this.unsubscribe();
		this.listeners.clear();
	}
}
