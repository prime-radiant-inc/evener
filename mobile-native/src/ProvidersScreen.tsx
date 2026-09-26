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
import type { ConnectionState, InstanceEntry } from "@evener/appwire-client";
import {
  activeSourceLabel,
  CONNECTION_REPLACED_ERROR,
  credentialLayers,
  fromEnvironment,
  ENDPOINT_CHANGED_TEST_MESSAGE,
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
import { appliedInstanceWrite } from "./appliedInstanceWrite";
import { ConnectionStatus } from "./ConnectionStatus";
import { isReady, whenReady } from "./connectionDisplay";
import { useCredentialStore } from "./credentialStore";
import { ProviderEditor } from "./ProviderEditor";
import { useProviderSurface } from "./providerSurface";
import { ProviderSignInSheet } from "./ProviderSignInSheet";
import { ProviderSignIn } from "./providerSignIn";
import {
  ConnectionWall,
  HUB_NO_LONGER_SELECTED,
  ModalConnectionStatus,
  useRetainedScreenConnection,
} from "./retainedScreen";
import type { Routes } from "./screens";
import {
  Action,
  Copy,
  ErrorMessage,
  WarningMessage,
  styles,
  useColors,
} from "./ui";

// The warnings shown for the two refusals the generic "could not be confirmed"
// line would misreport: a provider-instance write the hub APPLIED before a
// later step failed, and the hub's refusal of an asserted destination. They are
// this client's own wording: the rejection's text came from the hub and can
// echo submitted credentials, so it must never reach the screen (the same rule
// the catch's generic error keeps).
const APPLIED_REMOVAL_WARNING =
  "The instance was removed on the hub before a later step failed. The provider list was refreshed; check it before trying again.";
const APPLIED_RENAME_WARNING =
  "The instance was renamed on the hub before a later step failed. The provider list was refreshed; check it before trying again.";
const ENDPOINT_CHANGED_WARNING =
  "This instance changed to a different endpoint since the form was opened. The provider list was refreshed; review its destination and try again.";

// What clearing a credential or removing an instance says when the hub cannot
// fingerprint the destination: no key is being sent, so it does not reuse the
// save-specific wording.
const FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE =
  "The hub cannot check this endpoint right now, so the action was not run. Review its destination and try again once it can be checked.";

// What a credential save (a key or a JSON blob) says for the same condition;
// neutral about which credential kind, unlike the key-specific package copy.
const FINGERPRINT_UNAVAILABLE_CREDENTIAL_MESSAGE =
  "The hub cannot check this endpoint right now, so the credential was not saved. Review its destination and try again once it can be checked.";

// A mounted screen re-keyed to another hub is a fresh screen: the
// reconnect-retention state below - the banner's everReady, the sign-in
// flow, the credential store with its last listing - belongs to the hub it
// was built for, and a re-key remounts the body whole (the keyed wrapper's
// own rationale: useRetainedScreenConnection's doc).
export function ProvidersScreen(
  props: NativeStackScreenProps<Routes, "Providers">,
) {
  return <ProvidersScreenBody key={props.route.params.hubId} {...props} />;
}

function ProvidersScreenBody({
  route,
}: NativeStackScreenProps<Routes, "Providers">) {
  const {
    activeProfile,
    client,
    state,
    retry,
    error,
    display,
    canUseConnection,
  } = useRetainedScreenConnection(route.params.hubId);
  const ready = isReady(state);
  const [signIn, setSignIn] = useState<{
    hubId: string;
    name: string;
    flow: ProviderSignIn;
  } | null>(null);
  const [revision, setRevision] = useState(0);
  // useCredentialStore already survives a flap on its own (connectionChanged
  // rebinds it - credentialStore.ts), so unlike Plugins/HubSettings this
  // screen reads no retained client: <Providers> below takes only `store`,
  // never `client` directly, and the wall below waits on the display alone.
  const store = useCredentialStore();
  // The write gate the credential core holds over a replaced connection's
  // rows, subscribed so the resume below can wait for it: a manual retry
  // turns the new connection ready before its own listing read lands, and a
  // device start issued in between is refused as a stale-listing write -
  // the exchange would strand in its error phase instead of resuming.
  const writesRefused = useSyncExternalStore(
    store.subscribe,
    () => staleListingHeld(store.getState()),
  );
  useEffect(() => () => signIn?.flow.dispose(), [signIn]);
  useEffect(() => {
    if (!signIn) return;
    if (activeProfile?.id !== signIn.hubId) {
      signIn.flow.dispose();
      setSignIn(null);
      return;
    }
    // The same authorization the open arm applies: `ready` alone says the
    // connection reports a client, not that the client serves THIS hub —
    // in the re-key window the previous hub's still-ready client would
    // otherwise run the flow's exchanges against the hub the route
    // re-keyed away from (connectionIdentity's record, round 58).
    const connection = canUseConnection() ? client : null;
    signIn.flow.setConnection(connection);
    // A sign-in started from behind the banner never started: with no
    // connection its first start() was a no-op, so the exchange resumes
    // when the connection it was opened without arrives. Only an idle flow
    // - start() publishes "starting" synchronously, so a started one can
    // never read as idle here - and a mid-flow disconnect keeps its own
    // phase until the user retries.
    if (
      connection &&
      !writesRefused &&
      signIn.flow.getSnapshot().phase === "idle"
    )
      void signIn.flow.start();
  }, [signIn, activeProfile?.id, client, state, writesRefused, canUseConnection]);
  if (activeProfile?.id !== route.params.hubId)
    return <Copy>{HUB_NO_LONGER_SELECTED}</Copy>;
  if (display === "wall")
    return (
      <ConnectionWall
        hubName={activeProfile.name}
        purpose="manage providers"
        error={error}
        onReconnect={retry}
      />
    );
  return (
    <>
      {display === "banner" ? <ConnectionStatus /> : null}
      <Providers
        key={`${activeProfile.id}:${revision}`}
        store={store}
        connectionState={state}
        hubName={activeProfile.name}
        ready={ready}
        canUseConnection={canUseConnection}
        onSignIn={(name) => {
          const flow = new ProviderSignIn(store, name);
          // The raw client cannot be handed to the flow while the
          // connection is away - its first exchange would fire at a
          // connection that cannot reach the hub, the very start the wall
          // this screen used to show made unreachable (null while a manual
          // retry dials, or a closed client the same generation guard keeps
          // set). The effect above hands the flow the connection once it is
          // usable again, applying the same rule on every later transition.
          flow.setConnection(canUseConnection() ? client : null);
          setSignIn({ hubId: activeProfile.id, name, flow });
          void flow.start();
        }}
      />
      {signIn && signIn.hubId === activeProfile.id && (
        <ProviderSignInSheet
          flow={signIn.flow}
          name={signIn.name}
          hubName={activeProfile.name}
          connected={ready}
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
  connectionState,
  hubName,
  ready,
  canUseConnection,
  onSignIn,
}: {
  store: CredentialInstancesStore;
  connectionState: ConnectionState;
  hubName: string;
  ready: boolean;
  canUseConnection: () => boolean;
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
  const [actionWarning, setActionWarning] = useState<string | null>(null);
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
    // The row the editor was opened for changed - another instance was picked,
    // or this one's endpoint moved - so re-anchor: a key typed for the old
    // target must never be saved against a different one.
    if (
      editingCredential &&
      credentialTarget !== null &&
      (credentialTarget.name !== instance.name ||
        credentialTarget.fingerprint !== instance.endpointFingerprint)
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
  async function act(
    action: () => Promise<unknown>,
    {
      secret = false,
      endpointAsserted = false,
    }: { secret?: boolean; endpointAsserted?: boolean } = {},
  ) {
    // The single place every mutation below (save key/JSON, make-default,
    // clear stored key, logout, remove) checks readiness: AppWire rejects
    // the request anyway, and bailing before touching any state here is
    // what keeps a request that cannot be sent from clearing input the user
    // may still want once ready again.
    if (!canUseConnection()) return;
    const version = editorVersion.current;
    setActionError(null);
    setActionWarning(null);
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
        surface.refresh();
        return;
      }
      // Only an operation that asserted a destination can be refused for a
      // changed one; a generic conflict (a duplicate name) is not that.
      if (endpointAsserted && isEndpointConflict(err)) {
        // The hub refused an asserted endpoint: the instance moved since the
        // row this action was confirmed against was listed, so nothing was
        // written and a retry carrying the same fingerprint would be refused
        // identically. Clear the editor and its selection like a completed
        // write, re-read the provider list so the next attempt asserts the
        // destination now on screen, and warn in our own words - the
        // rejection's text can echo submitted values and is never shown.
        close();
        setActionWarning(ENDPOINT_CHANGED_WARNING);
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
  function confirm(
    title: string,
    action: () => Promise<unknown>,
    options: { endpointAsserted?: boolean } = {},
  ) {
    if (!canUseConnection()) return;
    Alert.alert(title, `${selected} on ${hubName}`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Confirm",
        style: "destructive",
        onPress: () => {
          void act(action, options);
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
        onRefresh={() => {
          if (canUseConnection()) surface.refresh();
        }}
        ListHeaderComponent={
          <View style={{ gap: 8, paddingBottom: 12 }}>
            <Copy muted>{hubName}</Copy>
            <Action
              disabled={
                !core.listingEstablished ||
                surface.busy ||
                core.writesRefused ||
                stale ||
                !ready
              }
              onPress={whenReady(canUseConnection, () => {
                close();
                setConfiguration("create");
              })}
            >
              Add provider instance
            </Action>
            <ErrorMessage message={loadError} />
            <WarningMessage message={actionWarning} />
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
          {/* The status lives in the modal because the native modal covers
           * the screen's banner (ModalConnectionStatus's own doc) - and the
           * draft stays in reach of neither a dismissal nor a missed
           * recovery. */}
          <ModalConnectionStatus connectionState={connectionState} />
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
                  disabled={
                    surface.busy || core.writesRefused || stale || !ready
                  }
                  canUseConnection={canUseConnection}
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
                    surface.refresh();
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
                            disabled={surface.busy || stale || !key.trim() || !ready}
                            onPress={() => {
                              // A destination the hub cannot fingerprint has no
                              // endpoint to assert, so the save is refused here
                              // rather than stored without an assertion; and the
                              // clear happens on act()'s own success path
                              // (below), never here - clearing before knowing
                              // whether the request could even be sent would
                              // lose input act() is about to refuse to send.
                              if (fingerprintUnavailable(instance)) {
                                setActionError(
                                  FINGERPRINT_UNAVAILABLE_CREDENTIAL_MESSAGE,
                                );
                                return;
                              }
                              const value = key.trim();
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
                                { secret: true, endpointAsserted: true },
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
                            !!surface.credentialTest?.pending ||
                            !ready
                          }
                          onPress={whenReady(canUseConnection, () => {
                            probeCredentials(instance.name);
                          })}
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
                          disabled={surface.busy || core.writesRefused || stale || !ready}
                          onPress={whenReady(canUseConnection, () => setConfiguration("edit"))}
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
                            disabled={surface.busy || stale || !ready}
                            onPress={whenReady(canUseConnection, () => editCredential("apiKey", instance))}
                          >
                            {instance.hasStoredFile ? "Replace key" : "Set key"}
                          </Action>
                        )}
                        {instance.authModes?.includes("credentialJson") && (
                          <Action
                            disabled={surface.busy || stale || !ready}
                            onPress={whenReady(canUseConnection, () => editCredential("credentialJson", instance))}
                          >
                            {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
                          </Action>
                        )}
                        {!instance.isDefault && (
                          <Action
                            disabled={surface.busy || core.writesRefused || stale || !ready}
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
                              disabled={surface.busy || stale || !ready}
                              onPress={() => {
                                // A destination the hub cannot fingerprint has
                                // no endpoint to assert: refuse with a reason
                                // rather than grey the control out silently.
                                if (fingerprintUnavailable(instance)) {
                                  setActionError(
                                    FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE,
                                  );
                                  return;
                                }
                                confirm(instance.auth === "gcp-adc" ? "Clear stored credential JSON?" : "Clear stored key?", () =>
                                  surface.clearStoredKey(
                                    instance.name,
                                    instance.endpointFingerprint,
                                  ),
                                { endpointAsserted: true });
                              }}
                            >
                              {instance.auth === "gcp-adc" ? "Clear stored credential JSON" : "Clear stored key"}
                            </Action>
                          )}
                        {["store", "oauth"].includes(instance.activeSource) && (
                          <Action
                              disabled={surface.busy || stale || !ready}
                              onPress={() => {
                                if (fingerprintUnavailable(instance)) {
                                  setActionError(
                                    FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE,
                                  );
                                  return;
                                }
                              confirm("Clear active credentials?", () =>
                                surface.logout(
                                  instance.name,
                                  instance.endpointFingerprint,
                                ),
                              { endpointAsserted: true });
                            }}
                          >
                            Clear credentials
                          </Action>
                        )}
                        {!fromEnvironment(instance) && (
                          <Action
                            disabled={surface.busy || core.writesRefused || stale || !ready}
                            onPress={() => {
                              if (fingerprintUnavailable(instance)) {
                                setActionError(
                                  FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE,
                                );
                                return;
                              }
                              confirm("Remove provider instance?", () =>
                                surface.remove(
                                  instance.name,
                                  instance.endpointFingerprint,
                                ),
                              { endpointAsserted: true });
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
