import { useState } from "react";
import { Pressable, View } from "react-native";
import { basename, parentOf } from "@evener/appwire-client";
import type { MCPServerSpec } from "@evener/appwire-client";
import { Copy, useColors } from "./ui";
import { Button } from "./sheet/Grouped";

export function LaunchResourceRow({
	item,
	remove,
	disabled = false,
}: {
	item: string | MCPServerSpec;
	remove?: () => void;
	disabled?: boolean;
}) {
	const colors = useColors();
	const [expanded, setExpanded] = useState(false);
	const path = typeof item === "string";
	const title = path ? basename(item) || item : item.name;
	const hint = path ? basename(parentOf(item)) || "/" : item.command;
	return (
		<View style={{ borderBottomWidth: 0.5, borderColor: colors.border }}>
			<View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`Details for ${path ? item : item.name}`}
					accessibilityState={{ expanded }}
					onPress={() => setExpanded(!expanded)}
					style={{ flex: 1, minWidth: 0, minHeight: 48, paddingVertical: 6 }}
				>
					<Copy>{`${expanded ? "⌄" : "›"} ${title}`}</Copy>
					<Copy muted numberOfLines={1}>
						{hint}
					</Copy>
				</Pressable>
				{remove && (
					<Button
						text
						label="Remove"
						disabled={disabled}
						accessibilityLabel={`Remove ${path ? "path" : "server"} ${path ? item : item.name}`}
						onPress={remove}
					/>
				)}
			</View>
			{expanded && (
				<View style={{ paddingBottom: 10, gap: 4 }}>
					{path ? (
						<Copy>{item}</Copy>
					) : (
						<>
							<Copy>{item.command}</Copy>
							<Copy muted>
								{item.args?.length
									? `Arguments\n${item.args.map((arg) => JSON.stringify(arg)).join("\n")}`
									: "No arguments"}
							</Copy>
						</>
					)}
				</View>
			)}
		</View>
	);
}
