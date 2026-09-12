import { Platform, Text, useColorScheme } from "react-native";
import type { AnsiLine } from "../../cmd/evener-hub/frontend/src/widgets/codeblock/ansi";
import { ansiRunTextStyle } from "./ansiOutputStyles";
import { useColors } from "./ui";

export function AnsiOutputLine({ line }: { line: AnsiLine }) {
  const colors = useColors();
  const dark = useColorScheme() === "dark";
  let offset = 0;
  return (
    <Text
      selectable
      accessibilityLabel={line
        .filter((run) => !run.hidden)
        .map((run) => run.text)
        .join("")}
      style={{
        color: colors.text,
        fontFamily: Platform.OS === "ios" ? "Menlo" : "monospace",
        fontSize: 13,
        lineHeight: 16,
      }}
    >
      {line.map((run) => {
        const key = `${offset}`;
        offset += run.text.length;
        return (
          <Text key={key} style={ansiRunTextStyle(run, dark)}>
            {run.text}
          </Text>
        );
      })}
    </Text>
  );
}
