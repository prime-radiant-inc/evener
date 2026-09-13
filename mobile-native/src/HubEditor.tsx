import { useRef, useState } from "react";
import {
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  Switch,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { HubProfile, HubUpdate } from "./connection";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function HubEditor({
  profile,
  save,
  close,
}: {
  profile: HubProfile;
  save: (id: string, update: HubUpdate) => Promise<void>;
  close: () => void;
}) {
  const colors = useColors();
  const [name, setName] = useState(profile.name);
  const [replaceToken, setReplaceToken] = useState(false);
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const pending = useRef(false);
  const [error, setError] = useState<string | null>(null);
  const inputStyle = [
    styles.input,
    { color: colors.text, borderColor: colors.border },
  ];
  async function submit() {
    if (pending.current) return;
    pending.current = true;
    setSaving(true);
    setError(null);
    try {
      await save(profile.id, { name, ...(replaceToken ? { token } : {}) });
      close();
    } catch {
      setError(
        "Could not save this hub. Check the name and token, then retry.",
      );
    } finally {
      pending.current = false;
      setSaving(false);
    }
  }
  return (
    <Modal
      animationType="slide"
      presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
      onRequestClose={() => {
        if (!pending.current) close();
      }}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View
          style={[styles.row, { paddingHorizontal: 20, paddingVertical: 8 }]}
        >
          <View style={styles.fill}>
            <Copy>Edit hub</Copy>
          </View>
          <Action disabled={saving} onPress={close}>
            Cancel
          </Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "ios" ? "padding" : "height"}
        >
          <ScrollView
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={styles.padded}
          >
            <TextInput
              accessibilityLabel="Hub name"
              value={name}
              onChangeText={setName}
              editable={!saving}
              style={inputStyle}
            />
            <Copy muted>{profile.origin}</Copy>
            <Copy muted>
              To connect to another address, add a separate hub.
            </Copy>
            <View style={[styles.row, { justifyContent: "space-between" }]}>
              <Copy>Replace saved token</Copy>
              <Switch
                accessibilityLabel="Replace saved token"
                value={replaceToken}
                disabled={saving}
                onValueChange={setReplaceToken}
              />
            </View>
            {replaceToken ? (
              <>
                <TextInput
                  accessibilityLabel="New bearer token"
                  placeholder="New bearer token"
                  placeholderTextColor={colors.secondary}
                  value={token}
                  onChangeText={setToken}
                  editable={!saving}
                  secureTextEntry
                  autoCapitalize="none"
                  autoCorrect={false}
                  style={inputStyle}
                />
                <Copy muted>Leave empty to remove the saved token.</Copy>
              </>
            ) : (
              <Copy muted>The saved token will be kept.</Copy>
            )}
            <ErrorMessage message={error} />
            <Action
              tone="primary"
              disabled={saving || !name.trim()}
              onPress={() => {
                void submit();
              }}
            >
              {saving ? "Saving…" : "Save changes"}
            </Action>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
