import { useState } from "react";
import { Pressable, View } from "react-native";
import type { MCPServerSpec } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  basename,
  parentOf,
} from "../../cmd/evener-hub/frontend/src/widgets/pathfield/pathRows";
import { Action, Copy, useColors } from "./ui";

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
          <Action
            disabled={disabled}
            label={`Remove ${path ? "path" : "server"} ${path ? item : item.name}`}
            onPress={remove}
          >
            Remove
          </Action>
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
