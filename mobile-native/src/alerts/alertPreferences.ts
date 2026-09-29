// Hub > In-app alerts (spec 12), kept per device: the switches describe how
// this phone interrupts you, whichever hub it is connected to.
import type { SyncStringStorage } from "../syncStringStorage";
import { type AlertPreferences, DEFAULT_ALERT_PREFERENCES } from "./alertCenter";

export const ALERT_PREFERENCES_KEY = "evener.native.alert-preferences";

function read(storage: SyncStringStorage): AlertPreferences {
	let stored: unknown;
	try {
		const raw = storage.getItemSync(ALERT_PREFERENCES_KEY);
		stored = raw ? JSON.parse(raw) : null;
	} catch {
		return DEFAULT_ALERT_PREFERENCES;
	}
	if (typeof stored !== "object" || stored === null || Array.isArray(stored)) return DEFAULT_ALERT_PREFERENCES;
	const preferences: Record<keyof AlertPreferences, boolean> = { ...DEFAULT_ALERT_PREFERENCES };
	for (const key of Object.keys(DEFAULT_ALERT_PREFERENCES) as (keyof AlertPreferences)[]) {
		const value = (stored as Record<string, unknown>)[key];
		if (typeof value === "boolean") preferences[key] = value;
	}
	return preferences;
}

export class AlertPreferenceStore {
	private value: AlertPreferences;
	private listeners = new Set<() => void>();

	constructor(private readonly storage: SyncStringStorage) {
		this.value = read(storage);
	}

	getSnapshot = (): AlertPreferences => this.value;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	set(change: Partial<AlertPreferences>): void {
		this.value = { ...this.value, ...change };
		try {
			this.storage.setItemSync(ALERT_PREFERENCES_KEY, JSON.stringify(this.value));
		} catch {
			// The choice still holds for this launch.
		}
		for (const listener of [...this.listeners]) listener();
	}
}
