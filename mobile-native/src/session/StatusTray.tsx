// The status tray (spec 8.3): one 36pt line above the composer, only while
// the agent works. It carries the Session's one pulse meter, what the agent
// is doing now, and Stop. Tapping the line jumps to the live end.
import { SymbolView } from "expo-symbols";
import { useEffect, useMemo, useReducer } from "react";
import { Platform, Pressable, Text, useWindowDimensions, View } from "react-native";
import { PulseMeter } from "../board/PulseMeter";
import { useColors } from "../ui";
import { FrameCounter, type TrayLine, type TraySource, trayLine } from "./trayLine";

export interface StatusTrayProps {
	line: TrayLine | null;
	perMinute: readonly number[] | undefined;
	connected: boolean;
	canStop: boolean;
	/** A Stop, or another change to this session, is in flight. */
	stopping: boolean;
	onStop(): void;
	onJumpToLive(): void;
}

export function StatusTray({
	line,
	perMinute,
	connected,
	canStop,
	stopping,
	onStop,
	onJumpToLive,
}: StatusTrayProps) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	if (!line) return null;
	// Stop is shown whenever the turn can be stopped, but only pressable while
	// the durable submitter has a client to carry it.
	const stopDisabled = !connected || stopping;
	return (
		<View style={{ flexDirection: "row", alignItems: "center", minHeight: 36, paddingLeft: 16, paddingRight: 4 }}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={line.text}
				accessibilityHint="Shows the latest"
				onPress={onJumpToLive}
				style={{ flex: 1, flexDirection: "row", alignItems: "center", gap: 8, minHeight: 36 }}
			>
				<PulseMeter
					perMinute={perMinute}
					tone={!connected ? "gray" : line.attention ? "attention" : "alive"}
				/>
				<Text
					allowFontScaling={Platform.OS !== "ios"}
					numberOfLines={1}
					ellipsizeMode="tail"
					style={{
						flexShrink: 1,
						color: line.attention ? palette.attentionInk : palette.inkMid,
						fontSize: 15 * scale,
						lineHeight: 20 * scale,
						fontVariant: ["tabular-nums"],
					}}
				>
					{line.text}
				</Text>
			</Pressable>
			{canStop ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Stop"
					accessibilityState={{ disabled: stopDisabled }}
					disabled={stopDisabled}
					onPress={onStop}
					// A 44pt target that overhangs the 36pt row.
					style={({ pressed }) => ({
						width: 44,
						height: 44,
						marginVertical: -4,
						alignItems: "center",
						justifyContent: "center",
						opacity: stopDisabled ? 0.4 : pressed ? 0.6 : 1,
					})}
				>
					<SymbolView name="stop.fill" tintColor={palette.inkHi} size={15 * scale} />
				</Pressable>
			) : null}
		</View>
	);
}

/** The tray with its own one-second clock, so only the tray re-renders each
 * second. The clock runs only while the tray shows a line, so an idle session
 * runs no timer. */
export function LiveStatusTray({
	session,
	frames,
	...tray
}: Omit<StatusTrayProps, "line" | "perMinute"> & {
	session: TraySource | null;
	frames: FrameCounter;
}) {
	const [, tick] = useReducer((count: number) => count + 1, 0);
	const now = Date.now();
	const line = session ? trayLine(session, now) : null;
	const shown = line !== null;
	useEffect(() => {
		if (!shown) return;
		const clock = setInterval(tick, 1000);
		return () => clearInterval(clock);
	}, [shown]);
	return (
		<StatusTray {...tray} line={line} perMinute={line && frames.hasFrames() ? frames.perMinute(now) : undefined} />
	);
}

interface FrameSource {
	getState(): { conversation: { lastFrameAt: number } | null };
	subscribe(listener: () => void): () => void;
}

/** One FrameCounter per conversation binding. It records this phone's clock
 * each time the session's lastFrameAt moves, which the reducer restamps on
 * every streamed frame. */
export function useFrameCounter(store: FrameSource): FrameCounter {
	// biome-ignore lint/correctness/useExhaustiveDependencies: A new binding starts a new count.
	const frames = useMemo(() => new FrameCounter(), [store]);
	useEffect(() => {
		let last = store.getState().conversation?.lastFrameAt;
		return store.subscribe(() => {
			const at = store.getState().conversation?.lastFrameAt;
			if (at === last) return;
			last = at;
			if (at !== undefined) frames.record(Date.now());
		});
	}, [store, frames]);
	return frames;
}
