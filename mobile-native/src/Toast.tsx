// A short confirmation that echoes what just happened, as spec section 5 asks
// ("Stopped", "Note saved", "Session archived · Undo"). One at a time: a newer
// toast replaces the older one. The screen that owns it decides where it sits;
// it floats on the raised surface, and VoiceOver hears it once.
import { useCallback, useEffect, useRef, useState } from "react";
import {
	AccessibilityInfo,
	Platform,
	Pressable,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import { styles, useColors } from "./ui";

export interface ToastAction {
	label: string;
	run(): void;
}

export interface ToastMessage {
	text: string;
	action?: ToastAction;
}

export type ShownToast = ToastMessage & { id: number };

/** Long enough to read, and longer when the toast offers an action (spec
 * 7.3's Undo stays 8 seconds). */
export const TOAST_MS = 4000;
export const TOAST_ACTION_MS = 8000;

export interface ToastController {
	toast: ShownToast | null;
	show(message: ToastMessage): void;
	dismiss(): void;
}

export function useToast(): ToastController {
	const [toast, setToast] = useState<ShownToast | null>(null);
	const lastId = useRef(0);
	useEffect(() => {
		if (!toast) return;
		const timer = setTimeout(
			() => setToast((current) => (current?.id === toast.id ? null : current)),
			toast.action ? TOAST_ACTION_MS : TOAST_MS,
		);
		return () => clearTimeout(timer);
	}, [toast]);
	const show = useCallback((message: ToastMessage) => {
		lastId.current += 1;
		setToast({ ...message, id: lastId.current });
		AccessibilityInfo.announceForAccessibility(message.text);
	}, []);
	const dismiss = useCallback(() => setToast(null), []);
	return { toast, show, dismiss };
}

export function Toast({ toast, dismiss }: Pick<ToastController, "toast" | "dismiss">) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	if (!toast) return null;
	const action = toast.action;
	return (
		<View
			style={{
				alignSelf: "center",
				flexDirection: "row",
				alignItems: "center",
				gap: 12,
				maxWidth: "92%",
				minHeight: 44,
				paddingHorizontal: 16,
				borderRadius: 22,
				borderWidth: 1,
				borderColor: palette.edgeStrong,
				backgroundColor: palette.surface,
				// A floating element's shadow (spec 16.3), not a hue.
				shadowColor: "#000000",
				shadowOpacity: 0.12,
				shadowRadius: 12,
				shadowOffset: { width: 0, height: 4 },
			}}
		>
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				numberOfLines={2}
				style={{ flexShrink: 1, color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{toast.text}
			</Text>
			{action ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={action.label}
					onPress={() => {
						// dismiss() first: if run() shows a follow-up toast synchronously
						// (an Undo that confirms with its own toast), that toast must
						// survive this batch instead of being cleared by our own dismiss.
						dismiss();
						action.run();
					}}
					style={styles.action}
				>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						style={{ color: palette.accentInk, fontSize: 15 * scale, fontWeight: "600" }}
					>
						{action.label}
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}
