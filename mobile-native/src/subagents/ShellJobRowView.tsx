// One shell job in the Activity list (Jesse's ruling on shell jobs, beside
// spec 9's subagent rows): a $ glyph whose hue is the job's state, its
// description, the session or subagent that started it, and its status with
// its quiet age while it runs or how long it ran once it ends (shellJobMeta).
import { memo } from "react";
import { Text, View } from "react-native";
import { fonts } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type ShellJobRow, shellJobMeta } from "./subagentModel";

export const ShellJobRowView = memo(function ShellJobRowView({ row, now }: { row: ShellJobRow; now: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const meta = shellJobMeta(row, now);
	const owner = `under ${row.owner}`;
	const hue = row.state === "running" ? palette.aliveInk : row.state === "failed" ? palette.dangerInk : palette.inkLow;
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	return (
		<View
			accessible
			accessibilityLabel={["Shell job", row.title, meta, owner].join(", ")}
			style={{
				flexDirection: "row",
				alignItems: "flex-start",
				columnGap: 10,
				paddingHorizontal: 16,
				paddingTop: 11,
				paddingBottom: 12,
				minHeight: 44,
				backgroundColor: palette.page,
			}}
		>
			<View style={{ width: 28, height: 22 * scale, alignItems: "center", justifyContent: "center" }}>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontFamily: fonts.mono, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: hue }}
				>
					$
				</Text>
			</View>
			<View style={{ flex: 1, minWidth: 0, gap: 2 }}>
				<View style={{ flexDirection: "row", alignItems: "baseline", columnGap: 8 }}>
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ flex: 1, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi }}
					>
						{row.title}
					</Text>
					<Text allowFontScaling={allowFontScaling} style={{ ...small, fontVariant: ["tabular-nums"] }}>
						{meta}
					</Text>
				</View>
				<Text allowFontScaling={allowFontScaling} numberOfLines={1} style={small}>
					{owner}
				</Text>
			</View>
		</View>
	);
});
