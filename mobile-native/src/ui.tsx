import type { ReactNode } from "react";
import {
  Platform,
  Pressable,
  StyleSheet,
  Text,
  useColorScheme,
} from "react-native";

export function useColors() {
  return useColorScheme() === "dark"
    ? {
        background: "#121417",
        surface: "#23272d",
        text: "#f1f2f3",
        secondary: "#a6adb5",
        border: "#363b42",
        accent: "#9cb4ff",
        error: "#ffaaa5",
        onAccent: "#18244b",
      }
    : {
        background: "#fafaf8",
        surface: "#eeefeb",
        text: "#202326",
        secondary: "#62676d",
        border: "#d8dcd9",
        accent: "#315ad7",
        error: "#b52b25",
        onAccent: "#ffffff",
      };
}

export function Action({
  children,
  onPress,
  disabled = false,
  label,
  expanded,
  tone = "accent",
}: {
  children: string;
  onPress: () => void;
  disabled?: boolean;
  label?: string;
  expanded?: boolean;
  tone?: "accent" | "quiet" | "primary";
}) {
  const colors = useColors();
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={label ?? children}
      accessibilityState={{ disabled, expanded }}
      disabled={disabled}
      onPress={onPress}
      style={({ pressed }) => [
        styles.action,
        tone === "primary" && {
          backgroundColor: colors.accent,
          borderRadius: 24,
          paddingHorizontal: 18,
        },
        { opacity: disabled ? 0.4 : pressed ? 0.65 : 1 },
      ]}
    >
      <Text
        style={{
          color:
            tone === "primary"
              ? colors.onAccent
              : tone === "quiet"
                ? colors.secondary
                : colors.accent,
          fontSize: 16,
          fontWeight: tone === "quiet" ? "400" : "600",
          flexShrink: 1,
        }}
      >
        {children}
      </Text>
    </Pressable>
  );
}

export function Copy({
  children,
  muted = false,
  label,
}: {
  children: ReactNode;
  muted?: boolean;
  label?: string;
}) {
  const colors = useColors();
  return (
    <Text
      selectable
      accessibilityLabel={label}
      style={{
        color: muted ? colors.secondary : colors.text,
        fontSize: muted ? 13 : Platform.OS === "ios" ? 17 : 16,
        lineHeight: muted ? 19 : 25,
      }}
    >
      {children}
    </Text>
  );
}

export function ErrorMessage({ message }: { message: string | null }) {
  const colors = useColors();
  return message ? (
    <Text
      accessibilityRole="alert"
      style={{ color: colors.error, padding: 12, fontSize: 16 }}
    >
      {message}
    </Text>
  ) : null;
}

export const styles = StyleSheet.create({
  fill: { flex: 1 },
  padded: { padding: 16, gap: 12 },
  row: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 12,
  },
  card: { borderRadius: 12, borderWidth: 1, padding: 16, gap: 8 },
  title: { fontSize: 22, fontWeight: "600" },
  input: {
    borderWidth: 1,
    borderRadius: 9,
    padding: 12,
    fontSize: 17,
    minHeight: 48,
  },
  action: {
    minHeight: Platform.OS === "android" ? 48 : 44,
    justifyContent: "center",
    paddingHorizontal: 8,
    paddingVertical: 8,
  },
});
