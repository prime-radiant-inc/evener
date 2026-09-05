import type { ReactNode } from "react";
import { Pressable, StyleSheet, Text, useColorScheme } from "react-native";

export function useColors() {
  return useColorScheme() === "dark"
    ? {
        background: "#101214",
        surface: "#1d2024",
        text: "#f3f4f6",
        secondary: "#acb4bf",
        border: "#3c434d",
        accent: "#88b9ff",
        error: "#ffaaa5",
      }
    : {
        background: "#f8f9fb",
        surface: "#ffffff",
        text: "#17212e",
        secondary: "#536174",
        border: "#cbd2db",
        accent: "#185bb7",
        error: "#b52b25",
      };
}

export function Action({
  children,
  onPress,
  disabled = false,
  label,
}: {
  children: string;
  onPress: () => void;
  disabled?: boolean;
  label?: string;
}) {
  const colors = useColors();
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={label ?? children}
      accessibilityState={{ disabled }}
      disabled={disabled}
      onPress={onPress}
      style={({ pressed }) => [
        styles.action,
        { opacity: disabled ? 0.4 : pressed ? 0.65 : 1 },
      ]}
    >
      <Text style={{ color: colors.accent, fontSize: 17, fontWeight: "600" }}>
        {children}
      </Text>
    </Pressable>
  );
}

export function Copy({
  children,
  muted = false,
}: {
  children: ReactNode;
  muted?: boolean;
}) {
  const colors = useColors();
  return (
    <Text
      selectable
      style={{
        color: muted ? colors.secondary : colors.text,
        fontSize: 16,
        lineHeight: 23,
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
    minHeight: 44,
    justifyContent: "center",
    paddingHorizontal: 8,
    paddingVertical: 8,
  },
});
