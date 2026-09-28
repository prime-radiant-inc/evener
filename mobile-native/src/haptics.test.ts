import { beforeEach, expect, it, vi } from "vitest";
import { AlertPreferenceStore } from "./alerts/alertPreferences";

const played = vi.hoisted(() => [] as string[]);
vi.mock("expo-haptics", () => ({
	ImpactFeedbackStyle: { Light: "Light", Rigid: "Rigid" },
	NotificationFeedbackType: { Success: "Success", Warning: "Warning" },
	selectionAsync: async () => void played.push("selection"),
	impactAsync: async (style: string) => void played.push(`impact:${style}`),
	notificationAsync: async (type: string) => void played.push(`notification:${type}`),
}));
const store = vi.hoisted(() => ({ current: null as AlertPreferenceStore | null }));
vi.mock("./alerts/nativeAlertPreferences", () => ({ alertPreferences: () => store.current }));

import { haptic } from "./haptics";

beforeEach(() => {
	played.length = 0;
	const values = new Map<string, string>();
	store.current = new AlertPreferenceStore({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	});
});

it("plays spec 16.6's feedback for each kind", () => {
	for (const kind of ["selection", "light", "success", "warning", "rigid"] as const) haptic(kind);
	expect(played).toEqual(["selection", "impact:Light", "notification:Success", "notification:Warning", "impact:Rigid"]);
});

it("plays nothing when Hub > In-app alerts turns haptics off", () => {
	store.current?.set({ haptics: false });
	haptic("warning");
	expect(played).toEqual([]);
});
