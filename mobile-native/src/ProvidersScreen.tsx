import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  ActivityIndicator,
  Alert,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  SectionList,
  Text,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import {
  activeSourceLabel,
  credentialLayers,
  groupByProvider,
  styleInfoText,
} from "../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { ProviderEditor } from "./ProviderEditor";
import { ProviderSignInSheet } from "./ProviderSignInSheet";
import { ProviderInstances } from "./providerInstances";
import { ProviderSignIn } from "./providerSignIn";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ProvidersScreen({
  route,
}: NativeStackScreenProps<Routes, "Providers">) {
  const { activeProfile, client, state, retry } = useConnection();
  const [signIn, setSignIn] = useState<{
    hubId: string;
    name: string;
    flow: ProviderSignIn;
  } | null>(null);
  const [revision, setRevision] = useState(0);
  useEffect(() => () => signIn?.flow.dispose(), [signIn]);
  useEffect(() => {
    if (!signIn) return;
    if (activeProfile?.id !== signIn.hubId) {
      signIn.flow.dispose();
      setSignIn(null);
      return;
    }
    signIn.flow.setConnection(state === "ready" ? client : null);
  }, [signIn, activeProfile?.id, client, state]);
  if (activeProfile?.id !== route.params.hubId)
    return (
      <Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
    );
  return (
    <>
      {client && state === "ready" ? (
        <Providers
          key={`${activeProfile.id}:${revision}`}
          client={client}
          hubName={activeProfile.name}
          onSignIn={(name) => {
            const flow = new ProviderSignIn(client, name);
            setSignIn({ hubId: activeProfile.id, name, flow });
            void flow.start();
          }}
        />
      ) : (
        <View style={{ padding: 20 }}>
          <Copy>Connect to {activeProfile.name} to manage providers.</Copy>
          <Action onPress={retry}>Reconnect</Action>
        </View>
      )}
      {signIn && signIn.hubId === activeProfile.id && (
        <ProviderSignInSheet
          flow={signIn.flow}
          name={signIn.name}
          hubName={activeProfile.name}
          connected={state === "ready"}
          onClose={() => {
            signIn.flow.dispose();
            setSignIn(null);
            setRevision((value) => value + 1);
          }}
        />
      )}
    </>
  );
}

function Providers({
  client,
  hubName,
  onSignIn,
}: {
  client: ConversationClientLike;
  hubName: string;
  onSignIn(name: string): void;
}) {
  const colors = useColors();
  const model = useMemo(() => new ProviderInstances(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const editorVersion = useRef(0);
  const [selected, setSelected] = useState<string | null>(null);
  const [configuration, setConfiguration] = useState<"create" | "edit" | null>(
    null,
  );
  const [editingKey, setEditingKey] = useState(false);
  const [key, setKey] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const instance = state.data?.instances.find((item) => item.name === selected);
  useEffect(() => {
    model.start();
    return () => {
      editorVersion.current += 1;
      model.dispose();
    };
  }, [model]);
  useEffect(() => {
    if (!instance) {
      setConfiguration((value) => (value === "edit" ? null : value));
      setSelected(null);
      setEditingKey(false);
      setKey("");
    }
  }, [instance]);
  function close() {
    editorVersion.current += 1;
    setSelected(null);
    setConfiguration(null);
    setEditingKey(false);
    setKey("");
    setActionError(null);
  }
  async function act(action: () => Promise<void>, secret = false) {
    const version = editorVersion.current;
    setActionError(null);
    try {
      await action();
      if (version !== editorVersion.current) return;
      setEditingKey(false);
      setKey("");
    } catch {
      if (version !== editorVersion.current) return;
      // Provider/transport errors may echo submitted credentials. Keep the
      // editor's error independent of upstream response text.
      setActionError(
        secret
          ? "Could not save the key. Check the connection and credential status before trying again."
          : "The operation could not be confirmed. Refresh and check the current state before trying again.",
      );
    }
  }
  function confirm(title: string, action: () => Promise<void>) {
    Alert.alert(title, `${selected} on ${hubName}`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Confirm",
        style: "destructive",
        onPress: () => {
          void act(action);
        },
      },
    ]);
  }
  const sections = groupByProvider(state.data?.instances ?? []).map(
    (group) => ({ title: group.providerId, data: group.instances }),
  );
  return (
    <SafeAreaView edges={["bottom", "left", "right"]} style={styles.fill}>
      <SectionList
        sections={sections}
        keyExtractor={(item) => item.name}
        contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 20 }}
        refreshing={state.loading}
        onRefresh={() => {
          void model.refresh();
        }}
        ListHeaderComponent={
          <View style={{ gap: 8, paddingBottom: 12 }}>
            <Copy muted>{hubName}</Copy>
            <Action
              disabled={!state.data || state.busy || state.data.writesRefused}
              onPress={() => {
                close();
                setConfiguration("create");
              }}
            >
              Add provider instance
            </Action>
            <ErrorMessage message={state.error} />
            {state.data?.diagnostics?.map((message) => (
              <Copy key={message}>{message}</Copy>
            ))}
            {state.loading && !state.data && (
              <ActivityIndicator accessibilityLabel="Loading providers" />
            )}
          </View>
        }
        ListEmptyComponent={
          !state.loading ? <Copy>No provider instances available.</Copy> : null
        }
        renderSectionHeader={({ section }) => (
          <Text
            style={{
              color: colors.secondary,
              backgroundColor: colors.background,
              paddingVertical: 8,
              fontSize: 14,
            }}
          >
            {section.title}
          </Text>
        )}
        renderItem={({ item }) => (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`${item.name}${item.isDefault ? ", default" : ""}. ${activeSourceLabel(item)}`}
            onPress={() => {
              editorVersion.current += 1;
              setActionError(null);
              setSelected(item.name);
            }}
            style={({ pressed }) => ({
              minHeight: 56,
              paddingVertical: 10,
              borderBottomWidth: 0.5,
              borderColor: colors.border,
              opacity: pressed ? 0.6 : 1,
            })}
          >
            <Text
              style={{ fontSize: 17, fontWeight: "600", color: colors.text }}
            >
              {item.name}
              {item.isDefault ? " · Default" : ""}
            </Text>
            <Text style={{ fontSize: 14, color: colors.secondary }}>
              {activeSourceLabel(item)}
            </Text>
          </Pressable>
        )}
      />
      <Modal
        visible={!!instance || configuration === "create"}
        animationType="slide"
        presentationStyle="pageSheet"
        onRequestClose={close}
      >
        <SafeAreaView
          style={[styles.fill, { backgroundColor: colors.background }]}
        >
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              paddingHorizontal: 16,
            }}
          >
            <View style={styles.fill}>
              <Copy muted>{hubName}</Copy>
            </View>
            <Action onPress={close}>Done</Action>
          </View>
          <View style={styles.fill}>
            <ScrollView
              automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
              keyboardDismissMode="interactive"
              keyboardShouldPersistTaps="handled"
              contentContainerStyle={{ padding: 20, gap: 12 }}
            >
              {configuration ? (
                <ProviderEditor
                  key={configuration === "create" ? "create" : instance?.name}
                  instance={configuration === "edit" ? instance : undefined}
                  providers={state.data?.availableProviders ?? []}
                  model={model}
                  disabled={state.busy || !!state.data?.writesRefused}
                  onSaved={(name) => {
                    setConfiguration(null);
                    setSelected(name);
                  }}
                  onCancel={() => {
                    if (configuration === "create") close();
                    else setConfiguration(null);
                  }}
                />
              ) : (
                instance && (
                  <>
                    <Text
                      accessibilityRole="header"
                      style={[styles.title, { color: colors.text }]}
                    >
                      {instance.name}
                    </Text>
                    <Copy muted>
                      {instance.providerId} · {styleInfoText(instance)}
                    </Copy>
                    {instance.isDefault && (
                      <Copy>Default provider instance</Copy>
                    )}
                    {instance.implicit && <Copy muted>From environment</Copy>}
                    <Copy>{activeSourceLabel(instance)}</Copy>
                    {credentialLayers(instance)
                      .filter((layer) => !layer.effective)
                      .map((layer) => (
                        <Copy key={layer.source} muted>
                          {layer.label} · Shadowed
                        </Copy>
                      ))}
                    {instance.warnings?.map((message) => (
                      <Copy key={message}>{message}</Copy>
                    ))}
                    <ErrorMessage message={actionError} />
                    {state.busy && (
                      <ActivityIndicator accessibilityLabel="Updating provider" />
                    )}
                    {editingKey ? (
                      <>
                        <Copy>API key</Copy>
                        <TextInput
                          accessibilityLabel="API key"
                          secureTextEntry
                          autoCapitalize="none"
                          autoCorrect={false}
                          value={key}
                          onChangeText={setKey}
                          editable={!state.busy}
                          style={[
                            styles.input,
                            { color: colors.text, borderColor: colors.border },
                          ]}
                        />
                        <View style={styles.row}>
                          <Action
                            disabled={state.busy || !key.trim()}
                            onPress={() => {
                              const value = key.trim();
                              setKey("");
                              void act(
                                () => model.setApiKey(instance.name, value),
                                true,
                              );
                            }}
                          >
                            Save key
                          </Action>
                          <Action
                            disabled={state.busy}
                            onPress={() => {
                              setEditingKey(false);
                              setKey("");
                            }}
                          >
                            Cancel
                          </Action>
                        </View>
                      </>
                    ) : (
                      <>
                        <Action
                          disabled={
                            state.busy ||
                            state.loading ||
                            !!state.credentialTest?.pending
                          }
                          onPress={() => {
                            void model.testCredentials(instance.name);
                          }}
                        >
                          {state.credentialTest?.provider === instance.name &&
                          state.credentialTest.pending
                            ? "Testing credentials…"
                            : "Test credentials"}
                        </Action>
                        {state.credentialTest?.provider === instance.name &&
                          state.credentialTest.result && (
                            <Copy>{state.credentialTest.result.message}</Copy>
                          )}
                        <Action
                          disabled={state.busy || state.data?.writesRefused}
                          onPress={() => setConfiguration("edit")}
                        >
                          Edit instance
                        </Action>
                        {instance.authModes?.includes("oauth") && (
                          <Action
                            disabled={state.busy}
                            onPress={() => {
                              const name = instance.name;
                              close();
                              onSignIn(name);
                            }}
                          >
                            {instance.hasStoredOAuth
                              ? "Refresh sign-in"
                              : "Sign in"}
                          </Action>
                        )}
                        {instance.authModes?.includes("apiKey") && (
                          <Action
                            disabled={state.busy}
                            onPress={() => setEditingKey(true)}
                          >
                            {instance.hasStoredFile ? "Replace key" : "Set key"}
                          </Action>
                        )}
                        {!instance.isDefault && (
                          <Action
                            disabled={state.busy || state.data?.writesRefused}
                            onPress={() => {
                              void act(() => model.setDefault(instance.name));
                            }}
                          >
                            Make default
                          </Action>
                        )}
                        {instance.hasStoredFile &&
                          instance.activeSource !== "store" && (
                            <Action
                              disabled={state.busy}
                              onPress={() =>
                                confirm("Clear stored key?", () =>
                                  model.clearStoredKey(instance.name),
                                )
                              }
                            >
                              Clear stored key
                            </Action>
                          )}
                        {["store", "oauth"].includes(instance.activeSource) && (
                          <Action
                            disabled={state.busy}
                            onPress={() =>
                              confirm("Clear active credentials?", () =>
                                model.logout(instance.name),
                              )
                            }
                          >
                            Clear credentials
                          </Action>
                        )}
                        {!instance.implicit && (
                          <Action
                            disabled={state.busy || state.data?.writesRefused}
                            onPress={() =>
                              confirm("Remove provider instance?", () =>
                                model.remove(instance.name),
                              )
                            }
                          >
                            Remove instance
                          </Action>
                        )}
                      </>
                    )}
                  </>
                )
              )}
            </ScrollView>
          </View>
        </SafeAreaView>
      </Modal>
    </SafeAreaView>
  );
}
