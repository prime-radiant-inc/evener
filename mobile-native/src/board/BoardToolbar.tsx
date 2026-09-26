import type { ConnectionState } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { useEffect, useState } from "react";
import { Platform, Pressable, Text, useWindowDimensions, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useColors } from "../ui";
import { connectionStatus, OFFLINE_AFTER_MS, RECONNECTING_AFTER_MS } from "./connectionStatus";

const MINUTE = 60_000;

interface Down {
	since: number;
	/** When the toolbar last saw the connection live, or null when it never
	 * has this launch. */
	lastLiveAt: number | null;
}

/** The Board's bottom toolbar (spec 7.1): the connection status in the
 * middle, and New session on the trailing side. Select arrives in PR 4. */
export function BoardToolbar({
	state,
	fatal,
	newSessionDisabled,
	onNewSession,
}: {
	state: ConnectionState;
	fatal: boolean;
	newSessionDisabled: boolean;
	onNewSession: () => void;
}) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	const { bottom } = useSafeAreaInsets();
	const live = state === "ready";
	const [down, setDown] = useState<Down | null>(() => (live ? null : { since: Date.now(), lastLiveAt: null }));
	const [now, setNow] = useState(Date.now);
	// The status depends on elapsed time, so while the connection is down the
	// toolbar re-renders at the 2-second and 30-second marks and then once a
	// minute for the age. A live Board runs no clock.
	useEffect(() => {
		if (live) {
			setDown(null);
			return () => {
				const at = Date.now();
				setDown({ since: at, lastLiveAt: at });
				setNow(at);
			};
		}
		const tick = () => setNow(Date.now());
		let minutes: ReturnType<typeof setInterval> | undefined;
		const timers = [
			setTimeout(tick, RECONNECTING_AFTER_MS),
			setTimeout(() => {
				tick();
				minutes = setInterval(tick, MINUTE);
			}, OFFLINE_AFTER_MS),
		];
		return () => {
			for (const timer of timers) clearTimeout(timer);
			if (minutes !== undefined) clearInterval(minutes);
		};
	}, [live]);
	const status = connectionStatus(state, fatal, down?.since ?? null, down?.lastLiveAt ?? null, now);
	return (
		<View
			style={{
				paddingBottom: bottom,
				borderTopWidth: 0.5,
				borderColor: palette.edge,
				backgroundColor: palette.page,
			}}
		>
			<View style={{ height: 50, flexDirection: "row", alignItems: "center", paddingHorizontal: 8 }}>
				<View style={{ flex: 1 }} />
				{status ? (
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						accessibilityLiveRegion="polite"
						style={{ fontSize: 13 * scale, color: palette.inkMid }}
					>
						{status}
					</Text>
				) : null}
				<View style={{ flex: 1, alignItems: "flex-end" }}>
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="New session"
						accessibilityState={{ disabled: newSessionDisabled }}
						disabled={newSessionDisabled}
						onPress={onNewSession}
						style={({ pressed }) => ({
							width: 44,
							height: 44,
							alignItems: "center",
							justifyContent: "center",
							borderRadius: 10,
							backgroundColor: pressed ? palette.pressed : "transparent",
						})}
					>
						<SymbolView
							name="square.and.pencil"
							size={22}
							tintColor={newSessionDisabled ? palette.inkLow : palette.accentInk}
						/>
					</Pressable>
				</View>
			</View>
		</View>
	);
}
