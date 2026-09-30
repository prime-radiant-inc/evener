// This phone's display choices (spec 12's Display): the appearance, the
// reading font, and whether Board rows show their model. They belong to the phone, not a hub, so they survive switching
// hubs and aren't cleared when a hub is removed.
import type { SyncStringStorage } from "../syncStringStorage";

export type AppearanceChoice = "system" | "light" | "dark";
export type ReadingFont = "serif" | "sans";

export interface DisplayChoices {
	appearance: AppearanceChoice;
	readingFont: ReadingFont;
	/** "Show model on Board rows" (spec 7.2, 12): a row's last line ends with
	 * its model's display name. */
	showModel: boolean;
}

/** The appearances in the order Display offers them. */
export const APPEARANCE_CHOICES: readonly AppearanceChoice[] = ["system", "light", "dark"];

/** Each appearance as Display names it. */
export const APPEARANCE_LABELS: Record<AppearanceChoice, string> = { system: "System", light: "Light", dark: "Dark" };

export const DEFAULT_DISPLAY: DisplayChoices = { appearance: "system", readingFont: "serif", showModel: false };
export const DISPLAY_KEY = "evener.native.display";

export type DisplayStorage = Pick<SyncStringStorage, "getItemSync" | "setItemSync">;

const FONTS: readonly string[] = ["serif", "sans"];

/** A stored value this build doesn't know, or can't parse, reads as the
 * default one field at a time, so one bad field never costs the other. */
function read(storage: DisplayStorage): DisplayChoices {
	let value: unknown;
	try {
		value = JSON.parse(storage.getItemSync(DISPLAY_KEY) ?? "null");
	} catch {
		return DEFAULT_DISPLAY;
	}
	const record = value !== null && typeof value === "object" ? (value as Record<string, unknown>) : {};
	return {
		appearance:
			typeof record.appearance === "string" && (APPEARANCE_CHOICES as readonly string[]).includes(record.appearance)
				? (record.appearance as AppearanceChoice)
				: DEFAULT_DISPLAY.appearance,
		readingFont:
			typeof record.readingFont === "string" && FONTS.includes(record.readingFont)
				? (record.readingFont as ReadingFont)
				: DEFAULT_DISPLAY.readingFont,
		showModel: typeof record.showModel === "boolean" ? record.showModel : DEFAULT_DISPLAY.showModel,
	};
}

export class DisplayPreferences {
	private choices: DisplayChoices;
	/** Whether the choices in memory are the ones stored. */
	private stored = true;
	private readonly listeners = new Set<() => void>();

	constructor(private readonly storage: DisplayStorage) {
		this.choices = read(storage);
	}

	getSnapshot = (): DisplayChoices => this.choices;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	/** Applies a choice at once and stores it. A store that fails throws after
	 * the choice has applied, so the page can say it won't survive a restart. */
	set(change: Partial<DisplayChoices>): void {
		const next = { ...this.choices, ...change };
		const unchanged =
			next.appearance === this.choices.appearance &&
			next.readingFont === this.choices.readingFont &&
			next.showModel === this.choices.showModel;
		// The same choice again still stores it when the last store failed.
		if (unchanged && this.stored) return;
		// Marked unstored before anything that can throw: a listener or the
		// store failing leaves the next set to store it.
		this.stored = false;
		if (!unchanged) {
			this.choices = next;
			for (const listener of this.listeners) listener();
		}
		this.storage.setItemSync(DISPLAY_KEY, JSON.stringify(next));
		this.stored = true;
	}
}

/** React Native's override for the whole app, native chrome included:
 * "unspecified" follows the system again. */
export function colorSchemeFor(appearance: AppearanceChoice): "light" | "dark" | "unspecified" {
	return appearance === "system" ? "unspecified" : appearance;
}
