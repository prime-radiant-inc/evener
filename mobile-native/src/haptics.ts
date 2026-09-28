// Spec 16.6's haptics, all behind Hub > In-app alerts' one Haptics switch
// (phase 3's ruling 5). A haptic is feedback, never information, so a
// failure to play one is ignored.
import * as Haptics from "expo-haptics";
import { alertPreferences } from "./alerts/nativeAlertPreferences";

export type HapticKind = "selection" | "light" | "success" | "warning" | "rigid";

export function haptic(kind: HapticKind): void {
	if (!alertPreferences().getSnapshot().haptics) return;
	const playing =
		kind === "selection"
			? Haptics.selectionAsync()
			: kind === "light"
				? Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light)
				: kind === "rigid"
					? Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Rigid)
					: Haptics.notificationAsync(
							kind === "success" ? Haptics.NotificationFeedbackType.Success : Haptics.NotificationFeedbackType.Warning,
						);
	void playing.catch(() => undefined);
}
