import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
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
import type { InstanceEntry } from "@evener/appwire-client";
import {
  activeSourceLabel,
  CONNECTION_REPLACED_ERROR,
  credentialLayers,
  ENDPOINT_CHANGED_TEST_MESSAGE,
  FINGERPRINT_UNAVAILABLE_ERROR,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  fingerprintUnavailable,
  groupByProvider,
  isEndpointConflict,
  sessionActionError,
  styleInfoText,
} from "@evener/appwire-client";
import {
  type CredentialInstancesStore,
  isStaleListingRefusal,
  staleListingHeld,
} from "@evener/appwire-client/state/credentials";
import { useConnection } from "./ConnectionProvider";
import { useCredentialStore } from "./credentialStore";
import { ProviderEditor } from "./ProviderEditor";
import { useProviderSurface } from "./providerSurface";
import { ProviderSignInSheet } from "./ProviderSignInSheet";
import { ProviderSignIn } from "./providerSignIn";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

// What a credential save says when the hub refuses the destination it was
// asserted against: the name moved since the row was read, so nothing honest
// was saved and the user re-enters against the destination now on screen.
const ENDPOINT_CHANGED_SAVE_MESSAGE =
  "This connection changed to a different endpoint, so the change was not saved. Check its destination and try again.";

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
  // The store triple is what binds React to the credential core: every field
  // read below is the core's own state, with no projection in between.
  const core = useSyncExternalStore(
    store.subscribe,
    store.getState,
    store.getInitialState,
  );
  const editorVersion = useRef(0);
  // A screen the user has left must not act on a write that outlives it: the
  // bump makes every captured version stale, so a late `act` continuation or
  // probe result neither reports an error nor issues a listing read.
  useEffect(
    () => () => {
      editorVersion.current += 1;
    },
    [],
  );
  // The write gate and the credential probe are the screen's own state; the
  // listing fields below are the core's.
  const surface = useProviderSurface(store);
  // The rows on screen belong to a replaced connection: the core refuses every
  // write to them, so the controls that would issue one are disabled here too.
  const stale = staleListingHeld(core);
  const [selected, setSelected] = useState<string | null>(null);
  const [configuration, setConfiguration] = useState<"create" | "edit" | null>(
    null,
  );
  const [editingCredential, setEditingCredential] = useState<"apiKey" | "credentialJson" | null>(null);
  // The instance the credential editor was opened for, with the endpoint it
  // resolved to then: a key typed for that destination is never saved against a
  // different one, and a move re-anchors the editor.
  const [credentialTarget, setCredentialTarget] = useState<{
    name: string;
    fingerprint?: string;
  } | null>(null);
  const [key, setKey] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const instance = core.instances.find((item) => item.name === selected);
  const loadError =
    core.error === null
      ? null
      : sessionActionError("Could not load providers", core.error);

  useEffect(() => {
    if (editingCredential && !instance?.authModes?.includes(editingCredential)) {
      setEditingCredential(null);
      setCredentialTarget(null);
      setKey("");
    }
    if (!instance) {
      setConfiguration((value) => (value === "edit" ? null : value));
      setSelected(null);
      setEditingCredential(null);
      setCredentialTarget(null);
      setKey("");
      return;
    }
    // The endpoint the editor was opened against moved: re-anchor so a key
    // typed for the old destination cannot be saved against the new one.
    if (
      editingCredential &&
      credentialTarget?.name === instance.name &&
      credentialTarget.fingerprint !== instance.endpointFingerprint
    ) {
      setEditingCredential(null);
      setCredentialTarget(null);
      setKey("");
    }
  }, [instance, editingCredential, credentialTarget]);
  function editCredential(
    kind: "apiKey" | "credentialJson",
    target: InstanceEntry,
  ) {
    setActionError(null);
    setEditingCredential(kind);
    setCredentialTarget({
      name: target.name,
      fingerprint: target.endpointFingerprint,
    });
    setKey("");
  }
  function close() {
    editorVersion.current += 1;
    setSelected(null);
    setConfiguration(null);
    setEditingCredential(null);
    setCredentialTarget(null);
    setKey("");
    setActionError(null);
  }
  async function act(action: () => Promise<unknown>, secret = false) {
    const version = editorVersion.current;
    setActionError(null);
    try {
      const applied = await action();
      if (version !== editorVersion.current) return;
      // An instance mutation answers false when a newer request superseded the
      // listing it answered with: the write may have landed, but this screen
      // cannot confirm it, so it does not report success. The surface that
      // issued the write owns the recovery read.
      if (applied === false) {
        setActionError(
          "The operation could not be confirmed. Refresh and check the current state before trying again.",
        );
        return;
      }
      setEditingCredential(null);
      setCredentialTarget(null);
      setKey("");
    } catch (err) {
      if (version !== editorVersion.current) return;
      // A refusal for rows of a replaced connection, or a destination that
      // moved since the row was read, is not an unconfirmed operation: say what
      // changed and re-read so the action is retryable against the rows now on
      // screen.
      if (isStaleListingRefusal(err)) {
        setActionError(CONNECTION_REPLACED_ERROR);
        surface.refresh();
        return;
      }
      if (isEndpointConflict(err)) {
        setActionError(ENDPOINT_CHANGED_SAVE_MESSAGE);
        surface.refresh();
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
  function confirm(title: string, action: () => Promise<unknown>) {
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

  // probeCredentials asserts the endpoint the row was read from; a name the hub
  // cannot fingerprint has no destination to assert, so the probe is refused
  // here rather than dialing whatever the name resolves to now.
  function probeCredentials(name: string) {
    // Only the current probe's outcome is shown: a new probe drops whatever the
    // last one said before it reports its own.
    setActionError(null);
    const row = core.instances.find((item) => item.name === name);
    if (fingerprintUnavailable(row)) {
      setActionError(FINGERPRINT_UNAVAILABLE_TEST_MESSAGE);
      return;
    }
    // The error belongs to the provider the user is looking at when it lands:
    // selecting another row bumps editorVersion, so a probe whose row was left
    // behind neither names this row nor reports on the one just picked.
    const version = editorVersion.current;
    void surface
      .testCredentials(name, row?.endpointFingerprint)
      .catch((err) => {
        if (version !== editorVersion.current) return;
        if (isEndpointConflict(err)) setActionError(ENDPOINT_CHANGED_TEST_MESSAGE);
      });
  }

  const sections = groupByProvider(core.instances).map((group) => ({
    title: group.providerId,
    data: group.instances,
  }));
  return (
    <SafeAreaView edges={["bottom", "left", "right"]} style={styles.fill}>
      <SectionList
        sections={sections}
        keyExtractor={(item) => item.name}
        contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 20 }}
        refreshing={core.loading}
        onRefresh={surface.refresh}
        ListHeaderComponent={
          <View style={{ gap: 8, paddingBottom: 12 }}>
            <Copy muted>{hubName}</Copy>
            <Action
              disabled={
                !core.listingEstablished ||
                surface.busy ||
                core.writesRefused ||
                stale
              }
              onPress={() => {
                close();
                setConfiguration("create");
              }}
            >
              Add provider instance
            </Action>
            <ErrorMessage message={loadError} />
            {core.diagnostics.map((message) => (
              <Copy key={message}>{message}</Copy>
            ))}
            {core.loading && !core.listingEstablished && (
              <ActivityIndicator accessibilityLabel="Loading providers" />
            )}
          </View>
        }
        ListEmptyComponent={
          !core.loading ? <Copy>No provider instances available.</Copy> : null
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
                  providers={core.availableProviders}
                  onCreate={surface.create}
                  onEdit={surface.edit}
                  disabled={surface.busy || core.writesRefused || stale}
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
                    {surface.busy && (
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
                          editable={!surface.busy}
                          style={[
                            styles.input,
                            { color: colors.text, borderColor: colors.border },
                          ]}
                        />
                        <View style={styles.row}>
                          <Action
                            disabled={surface.busy || stale || !key.trim()}
                            onPress={() => {
                              // A destination the hub cannot fingerprint has no
                              // endpoint to assert, so the save is refused here
                              // rather than stored without an assertion.
                              if (fingerprintUnavailable(instance)) {
                                setActionError(FINGERPRINT_UNAVAILABLE_ERROR);
                                return;
                              }
                              const value = key.trim();
                              setKey("");
                              void act(
                                () => editingCredential === "credentialJson"
                                  ? surface.setCredentialJson(
                                      instance.name,
                                      value,
                                      credentialTarget?.fingerprint,
                                    )
                                  : surface.setApiKey(
                                      instance.name,
                                      value,
                                      credentialTarget?.fingerprint,
                                    ),
                                true,
                              );
                            }}
                          >
                            {editingCredential === "credentialJson" ? "Save credential JSON" : "Save key"}
                          </Action>
                          <Action
                            disabled={surface.busy}
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
                            surface.busy ||
                            core.loading ||
                            stale ||
                            !!surface.credentialTest?.pending
                          }
                          onPress={() => {
                            probeCredentials(instance.name);
                          }}
                        >
                          {surface.credentialTest?.provider === instance.name &&
                          surface.credentialTest.pending
                            ? "Testing credentials…"
                            : "Test credentials"}
                        </Action>
                        {surface.credentialTest?.provider === instance.name &&
                          surface.credentialTest.result && (
                            <Copy>{surface.credentialTest.result.message}</Copy>
                          )}
                        <Action
                          disabled={surface.busy || core.writesRefused || stale}
                          onPress={() => setConfiguration("edit")}
                        >
                          Edit instance
                        </Action>
                        {instance.authModes?.includes("oauth") && (
                          <Action
                            disabled={surface.busy || stale}
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
                            disabled={surface.busy || stale}
                            onPress={() => editCredential("apiKey", instance)}
                          >
                            {instance.hasStoredFile ? "Replace key" : "Set key"}
                          </Action>
                        )}
                        {instance.authModes?.includes("credentialJson") && (
                          <Action
                            disabled={surface.busy || stale}
                            onPress={() => editCredential("credentialJson", instance)}
                          >
                            {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
                          </Action>
                        )}
                        {!instance.isDefault && (
                          <Action
                            disabled={surface.busy || core.writesRefused || stale}
                            onPress={() => {
                              void act(() => surface.setDefault(instance.name));
                            }}
                          >
                            Make default
                          </Action>
                        )}
                        {instance.hasStoredFile &&
                          instance.activeSource !== "store" && (
                            <Action
                              disabled={surface.busy || stale}
                              onPress={() => {
                                // A destination the hub cannot fingerprint has
                                // no endpoint to assert: refuse with a reason
                                // rather than grey the control out silently.
                                if (fingerprintUnavailable(instance)) {
                                  setActionError(FINGERPRINT_UNAVAILABLE_ERROR);
                                  return;
                                }
                                confirm(instance.auth === "gcp-adc" ? "Clear stored credential JSON?" : "Clear stored key?", () =>
                                  surface.clearStoredKey(
                                    instance.name,
                                    instance.endpointFingerprint,
                                  ),
                                );
                              }}
                            >
                              {instance.auth === "gcp-adc" ? "Clear stored credential JSON" : "Clear stored key"}
                            </Action>
                          )}
                        {["store", "oauth"].includes(instance.activeSource) && (
                          <Action
                            disabled={surface.busy || stale}
                            onPress={() => {
                              if (fingerprintUnavailable(instance)) {
                                setActionError(FINGERPRINT_UNAVAILABLE_ERROR);
                                return;
                              }
                              confirm("Clear active credentials?", () =>
                                surface.logout(
                                  instance.name,
                                  instance.endpointFingerprint,
                                ),
                              );
                            }}
                          >
                            Clear credentials
                          </Action>
                        )}
                        {!instance.implicit && (
                          <Action
                            disabled={surface.busy || core.writesRefused || stale}
                            onPress={() => {
                              if (fingerprintUnavailable(instance)) {
                                setActionError(FINGERPRINT_UNAVAILABLE_ERROR);
                                return;
                              }
                              confirm("Remove provider instance?", () =>
                                surface.remove(
                                  instance.name,
                                  instance.endpointFingerprint,
                                ),
                              );
                            }}
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
