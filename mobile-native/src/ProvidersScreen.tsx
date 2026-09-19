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
  fromEnvironment,
  groupByProvider,
  styleInfoText,
} from "@evener/appwire-client";
import type { CredentialInstancesStore } from "@evener/appwire-client/state/credentials";
import { appliedInstanceWrite } from "./appliedInstanceWrite";
import { useConnection } from "./ConnectionProvider";
import { useCredentialStore } from "./credentialStore";
import { ProviderEditor } from "./ProviderEditor";
import { ProviderSignInSheet } from "./ProviderSignInSheet";
import { ProviderInstances } from "./providerInstances";
import { ProviderSignIn } from "./providerSignIn";
import type { Routes } from "./screens";
import {
  Action,
  Copy,
  ErrorMessage,
  WarningMessage,
  styles,
  useColors,
} from "./ui";

// The warnings shown when the hub reports a provider-instance write it APPLIED
// before a later step failed. They are this client's own wording: the
// rejection's text came from the hub and can echo submitted credentials, so it
// must never reach the screen (the same rule the catch's generic error keeps).
const APPLIED_REMOVAL_WARNING =
  "The instance was removed on the hub before a later step failed. The provider list was refreshed; check it before trying again.";
const APPLIED_RENAME_WARNING =
  "The instance was renamed on the hub before a later step failed. The provider list was refreshed; check it before trying again.";
const ENDPOINT_CHANGED_WARNING =
  "This instance changed to a different endpoint since the form was opened. The provider list was refreshed; review its destination and try again.";

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
  const store = useCredentialStore();
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
          store={store}
          hubName={activeProfile.name}
          onSignIn={(name) => {
            const flow = new ProviderSignIn(store, name);
            flow.setConnection(client);
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
  store,
  hubName,
  onSignIn,
}: {
  store: CredentialInstancesStore;
  hubName: string;
  onSignIn(name: string): void;
}) {
  const colors = useColors();
  const model = useMemo(() => new ProviderInstances(store), [store]);
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const editorVersion = useRef(0);
  const [selected, setSelected] = useState<string | null>(null);
  const [configuration, setConfiguration] = useState<"create" | "edit" | null>(
    null,
  );
  const [editingCredential, setEditingCredential] = useState<"apiKey" | "credentialJson" | null>(null);
  const [key, setKey] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionWarning, setActionWarning] = useState<string | null>(null);
  const instance = state.data?.instances.find((item) => item.name === selected);
  useEffect(() => {
    model.start();
    return () => {
      editorVersion.current += 1;
      model.dispose();
    };
  }, [model]);
  useEffect(() => {
    if (editingCredential && !instance?.authModes?.includes(editingCredential)) {
      setEditingCredential(null);
      setKey("");
    }
    if (!instance) {
      setConfiguration((value) => (value === "edit" ? null : value));
      setSelected(null);
      setEditingCredential(null);
      setKey("");
    }
  }, [instance, editingCredential]);
  function close() {
    editorVersion.current += 1;
    setSelected(null);
    setConfiguration(null);
    setEditingCredential(null);
    setKey("");
    setActionError(null);
  }
  async function act(action: () => Promise<void>, secret = false) {
    const version = editorVersion.current;
    setActionError(null);
    setActionWarning(null);
    try {
      await action();
      if (version !== editorVersion.current) return;
      setEditingCredential(null);
      setKey("");
    } catch (err) {
      if (version !== editorVersion.current) return;
      const applied = appliedInstanceWrite(err);
      if (applied !== null) {
        // The write stands - the removal deleted the instance's credential (or
        // its config entry), or the rename is in providers.toml - so reporting
        // the generic failure would send the user to retry an operation whose
        // target is already gone. Close the editor and its selection the way a
        // completed write does, re-read the provider list instead of waiting
        // for the passive evener/auth/updated notification, and warn with our
        // own sentence rather than the rejection's text.
        close();
        setActionWarning(
          applied === "remove"
            ? APPLIED_REMOVAL_WARNING
            : APPLIED_RENAME_WARNING,
        );
        void model.refresh();
        return;
      }
      // Provider/transport errors may echo submitted credentials. Keep the
      // editor's error independent of upstream response text.
      setActionError(
        secret
          ? "Could not confirm the credential save. Check the connection and credential status before trying again."
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
            <WarningMessage message={actionWarning} />
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
                  onEndpointConflict={(name) => {
                    // The hub refused the endpoint the save asserted: the name
                    // moved since this editor was seeded, and nothing was
                    // written. Clear the editor like a completed save, re-read
                    // the provider list so a retry asserts the destination now
                    // on screen, and warn in this client's own words.
                    setConfiguration(null);
                    setSelected(name);
                    setActionWarning(ENDPOINT_CHANGED_WARNING);
                    void model.refresh();
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
                    {fromEnvironment(instance) && <Copy muted>From environment</Copy>}
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
                    <WarningMessage message={actionWarning} />
                    {state.busy && (
                      <ActivityIndicator accessibilityLabel="Updating provider" />
                    )}
                    {editingCredential ? (
                      <>
                        <Copy>{editingCredential === "credentialJson" ? "Google credential JSON" : "API key"}</Copy>
                        {editingCredential === "credentialJson" && (
                          <Copy muted>Paste a service-account key or application_default_credentials.json. The hub validates and stores it.</Copy>
                        )}
                        <TextInput
                          accessibilityLabel={editingCredential === "credentialJson" ? "Google credential JSON" : "API key"}
                          multiline={editingCredential === "credentialJson"}
                          secureTextEntry={editingCredential === "apiKey"}
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
                                () => editingCredential === "credentialJson"
                                  ? model.setCredentialJson(instance.name, value)
                                  : model.setApiKey(instance.name, value),
                                true,
                              );
                            }}
                          >
                            {editingCredential === "credentialJson" ? "Save credential JSON" : "Save key"}
                          </Action>
                          <Action
                            disabled={state.busy}
                            onPress={() => {
                              setEditingCredential(null);
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
                            onPress={() => setEditingCredential("apiKey")}
                          >
                            {instance.hasStoredFile ? "Replace key" : "Set key"}
                          </Action>
                        )}
                        {instance.authModes?.includes("credentialJson") && (
                          <Action
                            disabled={state.busy}
                            onPress={() => setEditingCredential("credentialJson")}
                          >
                            {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
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
                                confirm(instance.auth === "gcp-adc" ? "Clear stored credential JSON?" : "Clear stored key?", () =>
                                  model.clearStoredKey(instance.name),
                                )
                              }
                            >
                              {instance.auth === "gcp-adc" ? "Clear stored credential JSON" : "Clear stored key"}
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
                        {!fromEnvironment(instance) && (
                          <Action
                            disabled={state.busy || state.data?.writesRefused}
                            onPress={() =>
                              confirm("Remove provider instance?", () =>
                                model.remove(instance.name, instance.endpointFingerprint),
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
