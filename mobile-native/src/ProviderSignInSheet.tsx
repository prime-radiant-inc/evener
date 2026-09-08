import * as Clipboard from "expo-clipboard";
import { useEffect, useState, useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  AppState,
  Linking,
  Modal,
  Platform,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { ProviderSignIn } from "./providerSignIn";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ProviderSignInSheet({
  flow,
  name,
  hubName,
  connected,
  onClose,
}: {
  flow: ProviderSignIn;
  name: string;
  hubName: string;
  connected: boolean;
  onClose(): void;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(flow.subscribe, flow.getSnapshot);
  const [redirect, setRedirect] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    flow.setActive(AppState.currentState === "active");
    const subscription = AppState.addEventListener("change", (value) =>
      flow.setActive(value === "active"),
    );
    return () => subscription.remove();
  }, [flow]);
  async function open(url: string) {
    setLocalError(null);
    try {
      const parsed = new URL(url);
      if (
        !["https:", "http:"].includes(parsed.protocol) ||
        parsed.username ||
        parsed.password
      )
        throw new Error("Unsupported URL");
      await Linking.openURL(url);
    } catch {
      setLocalError("Could not open the authorization page.");
    }
  }
  async function copy() {
    setLocalError(null);
    try {
      if (
        state.device &&
        (await Clipboard.setStringAsync(state.device.userCode))
      )
        setCopied(true);
      else
        setLocalError("Could not copy the code. Select it to copy manually.");
    } catch {
      setLocalError("Could not copy the code. Select it to copy manually.");
    }
  }
  return (
    <Modal
      visible
      animationType="slide"
      presentationStyle="pageSheet"
      onRequestClose={onClose}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View style={[styles.row, { paddingHorizontal: 16 }]}>
          <View style={styles.fill}>
            <Copy muted>{hubName}</Copy>
          </View>
          <Action onPress={onClose}>
            {state.phase === "authorized" ? "Done" : "Cancel"}
          </Action>
        </View>
        <ScrollView
          automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
          keyboardShouldPersistTaps="handled"
          contentContainerStyle={{ padding: 20, gap: 12 }}
        >
          <Copy>Sign in to {name}</Copy>
          {!connected && <Copy muted>Waiting for this hub to reconnect…</Copy>}
          <ErrorMessage message={localError || state.error} />
          {state.busy && (
            <ActivityIndicator accessibilityLabel="Checking sign-in" />
          )}
          {state.credentialState !== "unknown" && (
            <Copy>
              {state.credentialState === "configured"
                ? "Current OAuth sign-in is configured."
                : "Current OAuth sign-in is not configured."}
            </Copy>
          )}
          {state.phase === "authorized" && (
            <Copy>
              Signed in. Provider credentials will refresh when connected.
            </Copy>
          )}
          {state.phase === "device" && state.device && (
            <>
              <Copy>Copy the code, then authorize in your browser.</Copy>
              <Copy>{state.device.userCode}</Copy>
              <View style={[styles.row, { flexWrap: "wrap" }]}>
                <Action
                  onPress={() => {
                    void copy();
                  }}
                >
                  {copied ? "Code copied" : "Copy code"}
                </Action>
                <Action
                  onPress={() => {
                    if (state.device) void open(state.device.verificationUrl);
                  }}
                >
                  Open authorization page
                </Action>
              </View>
              <Copy muted>
                Waiting for authorization. You can return here after approving
                it.
              </Copy>
              {state.error && (
                <Action
                  disabled={!connected || state.busy}
                  onPress={() => {
                    void flow.retryPoll();
                  }}
                >
                  Retry status check
                </Action>
              )}
            </>
          )}
          {state.phase === "browser" && state.browser && (
            <>
              <Copy>
                Authorize in your browser, then paste the full redirect URL
                here.
              </Copy>
              <Action
                onPress={() => {
                  if (state.browser) void open(state.browser.url);
                }}
              >
                Open authorization page
              </Action>
              <TextInput
                accessibilityLabel="Redirect URL"
                value={redirect}
                onChangeText={setRedirect}
                autoCapitalize="none"
                autoCorrect={false}
                editable={!state.busy}
                style={[
                  styles.input,
                  { color: colors.text, borderColor: colors.border },
                ]}
              />
              <Action
                disabled={!connected || state.busy || !redirect.trim()}
                onPress={() => {
                  const value = redirect;
                  setRedirect("");
                  void flow.complete(value);
                }}
              >
                Finish sign-in
              </Action>
            </>
          )}
          {["device", "browser", "error", "expired"].includes(state.phase) && (
            <Action
              disabled={!connected || state.busy}
              onPress={() => {
                void flow.checkStatus();
              }}
            >
              Check credential status
            </Action>
          )}
          {["error", "expired"].includes(state.phase) && (
            <Action
              disabled={!connected || state.busy}
              onPress={() => {
                setCopied(false);
                setRedirect("");
                void flow.start();
              }}
            >
              Start again
            </Action>
          )}
        </ScrollView>
      </SafeAreaView>
    </Modal>
  );
}
