// The Hub's Providers page (spec 12's Providers; rulings 6, 9 and 12): each
// provider instance with its sign-in state in words, and a detail sheet over
// the list with its status, models, sign-in and key actions, and the
// management the phone keeps (add, edit, make default, clear, remove). The
// detail opens as a sheet over the list, not a pushed page, because the
// mutation gates, fences and sign-in flow below live with the list (ruling 9).
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Alert } from "react-native";
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";
import {
	CONNECTION_REPLACED_ERROR,
	credentialLayers,
	fromEnvironment,
	ENDPOINT_CHANGED_TEST_MESSAGE,
	FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
	fingerprintUnavailable,
	isEndpointConflict,
	sessionActionError,
	styleInfoText,
} from "@evener/appwire-client";
import {
	type CredentialInstancesStore,
	isStaleListingRefusal,
	staleListingHeld,
} from "@evener/appwire-client/state/credentials";
import { appliedInstanceWrite } from "../appliedInstanceWrite";
import { isReady, whenReady } from "../connectionDisplay";
import { useCredentialStore } from "../credentialStore";
import { destructiveButton } from "../haptics";
import { type LeaveGuard, ProviderEditor } from "../ProviderEditor";
import { signInKind, statusOf } from "../providers/providerStatus";
import { useProviderSurface } from "../providerSurface";
import { ProviderSignInSheet } from "../ProviderSignInSheet";
import { ProviderSignIn } from "../providerSignIn";
import { HUB_NO_LONGER_SELECTED, useRetainedScreenConnection } from "../retainedScreen";
import { Group, GroupedPage, GroupFooter, GroupLabel, Row, RowValue, TextFieldRow } from "../sheet/Grouped";
import { guardLeave } from "../sheet/confirmDiscard";
import { ModalFrame } from "../sheet/ModalSheet";
import { Sheet } from "../sheet/Sheet";
import { FirstLoad, SheetStatus } from "../sheet/SheetStatus";
import { Spinner } from "../sheet/Spinner";
import type { HubRoutes } from "./hubSheetContext";
import { useAuthStatuses } from "./useAuthStatuses";
import {
	appliedButFailed,
	ENDPOINT_CHANGED_WARNING,
	FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE,
	FINGERPRINT_UNAVAILABLE_CREDENTIAL_MESSAGE,
	PROVIDERS_NOT_LOADED,
	UNCONFIRMED_CHANGE,
	UNCONFIRMED_CREDENTIAL,
} from "../providers/providerCopy";

// A mounted page re-keyed to another hub is a fresh page: the
// reconnect-retention state below - the status line's everReady, the sign-in
// flow, the credential store with its last listing - belongs to the hub it
// was built for, and a re-key remounts the body whole (the keyed wrapper's
// own rationale: useRetainedScreenConnection's doc).
export function ProvidersPage(props: NativeStackScreenProps<HubRoutes, "Providers">) {
	return <ProvidersPageBody key={props.route.params.hubId} {...props} />;
}

function ProvidersPageBody({ route, navigation }: NativeStackScreenProps<HubRoutes, "Providers">) {
	const { activeProfile, client, state, display, canUseConnection } = useRetainedScreenConnection(route.params.hubId);
	const ready = isReady(state);
	const [signIn, setSignIn] = useState<{
		hubId: string;
		name: string;
		flow: ProviderSignIn;
	} | null>(null);
	const [revision, setRevision] = useState(0);
	// useCredentialStore already survives a flap on its own (connectionChanged
	// rebinds it - credentialStore.ts), so unlike Plugins/HubSettings this
	// page reads no retained client: <Providers> below takes only `store`,
	// never `client` directly, and FirstLoad below waits on the display alone.
	const store = useCredentialStore();
	// The sign-in statuses read under the same authorization the sign-in flow
	// gets below, so a re-key window never reads the previous hub's.
	const auth = useAuthStatuses(canUseConnection() ? client : null);
	// The write gate the credential core holds over a replaced connection's
	// rows, subscribed so the resume below can wait for it: a manual retry
	// turns the new connection ready before its own listing read lands, and a
	// device start issued in between is refused as a stale-listing write -
	// the exchange would strand in its error phase instead of resuming.
	const writesRefused = useSyncExternalStore(store.subscribe, () => staleListingHeld(store.getState()));
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
		if (connection && !writesRefused && signIn.flow.getSnapshot().phase === "idle") void signIn.flow.start();
	}, [signIn, activeProfile?.id, client, state, writesRefused, canUseConnection]);
	if (activeProfile?.id !== route.params.hubId)
		return (
			<GroupedPage>
				<GroupFooter>{HUB_NO_LONGER_SELECTED}</GroupFooter>
			</GroupedPage>
		);
	// Before anything has shown, or after a close no retry can clear, the page
	// says it is connecting (or why it can't), in place of a wall (ruling 21).
	if (display === "wall")
		return (
			<GroupedPage>
				<SheetStatus />
				<FirstLoad hubName={activeProfile.name} label="Loading providers" />
			</GroupedPage>
		);
	const { focus, signIn: signInFocus } = route.params;
	return (
		<>
			<Providers
				key={`${activeProfile.id}:${revision}`}
				store={store}
				auth={auth}
				hubName={activeProfile.name}
				ready={ready}
				canUseConnection={canUseConnection}
				focus={focus}
				signInFocus={signInFocus ?? false}
				onFocused={() => navigation.setParams({ focus: undefined, signIn: undefined })}
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
	auth,
	hubName,
	ready,
	canUseConnection,
	focus,
	signInFocus,
	onFocused,
	onSignIn,
}: {
	store: CredentialInstancesStore;
	auth: ReadonlyMap<string, AuthStatusResponse> | null;
	hubName: string;
	ready: boolean;
	canUseConnection: () => boolean;
	/** The instance a notice or link names: its detail opens once the list
	 * has it, or with signInFocus its sign-in starts as the detail's Sign in
	 * does (ruling 25). */
	focus: string | undefined;
	signInFocus: boolean;
	/** The page has acted on `focus`, so the route can drop it. */
	onFocused(): void;
	onSignIn(name: string): void;
}) {
	// The store triple is what binds React to the credential core: every field
	// read below is the core's own state, with no projection in between.
	const core = useSyncExternalStore(store.subscribe, store.getState, store.getInitialState);
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
	const [configuration, setConfiguration] = useState<"create" | "edit" | null>(null);
	// The open editor's leave check: a swipe down asks it, as its Cancel does.
	const editorLeave = useRef<LeaveGuard | null>(null);
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
	// A Google provider's stored credential is a JSON file, not a key.
	const json = instance?.auth === "gcp-adc";
	const loadError = core.error === null ? null : sessionActionError(PROVIDERS_NOT_LOADED, core.error);
	// A failed listing reads again when the page comes back to the front, as
	// the store's notifications and a reconnect already do; nothing asks you
	// to (no pull-to-refresh, ruling 21).
	// Refs keep the effect's identity fixed, so it runs on focus alone and
	// not again the moment a read fails.
	const failed = useRef(false);
	failed.current = core.error !== null;
	const refreshSurface = useRef(surface.refresh);
	refreshSurface.current = surface.refresh;
	useFocusEffect(
		useCallback(() => {
			if (failed.current) refreshSurface.current();
		}, []),
	);
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
			(credentialTarget.name !== instance.name || credentialTarget.fingerprint !== instance.endpointFingerprint)
		) {
			setEditingCredential(null);
			setCredentialTarget(null);
			setKey("");
		}
	}, [instance, editingCredential, credentialTarget]);
	function editCredential(kind: "apiKey" | "credentialJson", target: InstanceEntry) {
		setActionError(null);
		setEditingCredential(kind);
		setCredentialTarget({
			name: target.name,
			fingerprint: target.endpointFingerprint,
		});
		setKey("");
	}
	// A pasted key or credential JSON: leaving it waits out its save, and asks
	// before the text goes (spec 6), whether by its Cancel, Done or a swipe.
	const leaveKey = (leave: () => void) =>
		guardLeave({ busy: !!editingCredential && surface.busy, dirty: !!(editingCredential && key.trim()) }, leave);
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
		{ secret = false, endpointAsserted = false }: { secret?: boolean; endpointAsserted?: boolean } = {},
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
				setActionError(UNCONFIRMED_CHANGE);
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
				setActionWarning(appliedButFailed(applied === "remove" ? "removed" : "renamed"));
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
			setActionError(secret ? UNCONFIRMED_CREDENTIAL : UNCONFIRMED_CHANGE);
		}
	}
	/** Asks before a destructive action, naming the provider and the hub, with
	 * the action's own verb on its button (spec 5). */
	function confirm(
		title: string,
		verb: string,
		action: () => Promise<unknown>,
		options: { endpointAsserted?: boolean } = {},
	) {
		if (!canUseConnection()) return;
		Alert.alert(title, `${selected} on ${hubName}`, [
			{ text: "Cancel", style: "cancel" },
			destructiveButton(verb, () => act(action, options)),
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
		void surface.testCredentials(name, row?.endpointFingerprint).catch((err) => {
			if (version !== editorVersion.current) return;
			if (isEndpointConflict(err)) setActionError(ENDPOINT_CHANGED_TEST_MESSAGE);
		});
	}

	// A notice's Sign in, or a link to one provider, acts once the list has
	// it and could act on it (not held stale or mid-write, as the detail's
	// own Sign in is disabled then): a sign-in starts as the detail's Sign in
	// does, and anything else opens the detail. A provider the list doesn't
	// have opens nothing. Each link acts once; clearing it readies the next.
	const focusHandled = useRef(false);
	useEffect(() => {
		if (!focus) {
			focusHandled.current = false;
			return;
		}
		if (focusHandled.current || !core.listingEstablished || stale || surface.busy) return;
		focusHandled.current = true;
		const target = core.instances.find((item) => item.name === focus);
		if (target && signInFocus && target.authModes?.includes("oauth")) onSignIn(target.name);
		else if (target) {
			editorVersion.current += 1;
			setSelected(target.name);
		}
		onFocused();
	}, [focus, signInFocus, core.listingEstablished, core.instances, stale, surface.busy, onSignIn, onFocused]);

	const writeHeld = surface.busy || core.writesRefused || stale || !ready;
	return (
		<>
			<GroupedPage>
				<SheetStatus />
				{core.listingEstablished ? null : <FirstLoad hubName={hubName} label="Loading providers" />}
				{loadError ? <GroupFooter tone="danger">{loadError}</GroupFooter> : null}
				{actionWarning ? <GroupFooter tone="attention">{actionWarning}</GroupFooter> : null}
				{core.listingEstablished ? (
					<>
						{core.instances.length > 0 ? (
							<Group>
								{core.instances.map((item) => {
									// Nothing, while an account sign-in's state isn't read yet.
									const status = statusOf(item, auth);
									const sub = `${item.providerId}${item.isDefault ? " · default" : ""}`;
									return (
										<Row
											key={item.name}
											label={item.name}
											sub={sub}
											value={
												status?.tone === "attention" ? (
													<RowValue tag={{ text: status.word, tone: "amber" }} />
												) : (
													status?.word
												)
											}
											accessibilityLabel={[item.name, sub, status?.word].filter(Boolean).join(", ")}
											chevron
											onPress={() => {
												editorVersion.current += 1;
												setActionError(null);
												setSelected(item.name);
											}}
										/>
									);
								})}
							</Group>
						) : (
							<GroupFooter>No providers yet.</GroupFooter>
						)}
						{core.diagnostics.map((message) => (
							<GroupFooter key={message}>{message}</GroupFooter>
						))}
						<Group label="Manage">
							<Row
								label="Add provider"
								tone="accent"
								disabled={writeHeld}
								onPress={whenReady(canUseConnection, () => {
									close();
									setConfiguration("create");
								})}
							/>
						</Group>
					</>
				) : null}
			</GroupedPage>
			<ModalFrame
				visible={!!instance || configuration === "create"}
				onRequestClose={() => {
					// A swipe down asks before an edit or a pasted key goes (spec 6),
					// and waits out a save in flight, as each Cancel does.
					// An open editor always answers for itself, never the key guard.
					if (configuration) editorLeave.current?.(close);
					else leaveKey(close);
				}}
			>
				{/* The native modal covers the page's status line, so each sheet in it
				    carries its own - and the draft stays in reach of neither a
				    dismissal nor a missed recovery. */}
				{configuration ? (
					<ProviderEditor
						key={configuration === "create" ? "create" : instance?.name}
						instance={configuration === "edit" ? instance : undefined}
						providers={core.availableProviders}
						onCreate={surface.create}
						onEdit={surface.edit}
						disabled={surface.busy || core.writesRefused || stale || !ready}
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
						leaveGuard={editorLeave}
						accessory={<SheetStatus />}
					/>
				) : (
					<Sheet title={instance?.name ?? ""} done={{ onPress: () => leaveKey(close) }} accessory={<SheetStatus />}>
						<GroupedPage>
							{instance ? (
								<>
									<ProviderFacts instance={instance} auth={auth} />
									{editingCredential ? (
										<>
											<Group>
												<TextFieldRow
													label={editingCredential === "credentialJson" ? "Google credential JSON" : "API key"}
													placeholder={
														editingCredential === "credentialJson" ? "Paste the credential JSON" : "Paste the API key"
													}
													multiline={editingCredential === "credentialJson"}
													secure={editingCredential === "apiKey"}
													value={key}
													onChangeText={setKey}
													disabled={surface.busy}
												/>
												<Row
													label="Save"
													accessibilityLabel={
														editingCredential === "credentialJson" ? "Save credential JSON" : "Save key"
													}
													tone="accent"
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
															setActionError(FINGERPRINT_UNAVAILABLE_CREDENTIAL_MESSAGE);
															return;
														}
														const value = key.trim();
														void act(
															() =>
																editingCredential === "credentialJson"
																	? surface.setCredentialJson(instance.name, value, credentialTarget?.fingerprint)
																	: surface.setApiKey(instance.name, value, credentialTarget?.fingerprint),
															{ secret: true, endpointAsserted: true },
														);
													}}
												/>
												<Row
													label="Cancel"
													tone="accent"
													disabled={surface.busy}
													onPress={() => {
														leaveKey(() => {
															setEditingCredential(null);
															setKey("");
														});
													}}
												/>
											</Group>
											<GroupFooter>
												{editingCredential === "credentialJson"
													? "Paste a service-account key or application_default_credentials.json. The hub validates and stores it."
													: "The key is stored on the hub, not on this phone."}
											</GroupFooter>
										</>
									) : (
										<>
											<Group>
												{instance.authModes?.includes("oauth") && (
													<Row
														label={instance.hasStoredOAuth ? "Sign in again" : "Sign in"}
														tone="accent"
														disabled={surface.busy || stale}
														onPress={() => {
															const name = instance.name;
															close();
															onSignIn(name);
														}}
													/>
												)}
												{instance.authModes?.includes("apiKey") && (
													<Row
														label={instance.hasStoredFile ? "Replace key" : "Set key"}
														tone="accent"
														disabled={surface.busy || stale || !ready}
														onPress={whenReady(canUseConnection, () => editCredential("apiKey", instance))}
													/>
												)}
												{instance.authModes?.includes("credentialJson") && (
													<Row
														label={instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
														tone="accent"
														disabled={surface.busy || stale || !ready}
														onPress={whenReady(canUseConnection, () => editCredential("credentialJson", instance))}
													/>
												)}
												<Row
													label={
														surface.credentialTest?.provider === instance.name && surface.credentialTest.pending
															? "Testing…"
															: "Test connection"
													}
													accessibilityLabel="Test connection"
													tone="accent"
													disabled={
														surface.busy || core.loading || stale || !!surface.credentialTest?.pending || !ready
													}
													onPress={whenReady(canUseConnection, () => {
														probeCredentials(instance.name);
													})}
												/>
											</Group>
											{surface.credentialTest?.provider === instance.name && surface.credentialTest.result ? (
												surface.credentialTest.result.status === "success" ? (
													<GroupFooter>Works</GroupFooter>
												) : (
													<GroupFooter tone="danger">{surface.credentialTest.result.message}</GroupFooter>
												)
											) : null}
										</>
									)}
									{actionError ? <GroupFooter tone="danger">{actionError}</GroupFooter> : null}
									{actionWarning ? <GroupFooter tone="attention">{actionWarning}</GroupFooter> : null}
									{surface.busy && <Spinner label="Updating provider" />}
									{editingCredential ? null : (
										<>
											<Group label="Manage">
												<Row
													label="Edit"
													tone="accent"
													disabled={surface.busy || core.writesRefused || stale || !ready}
													onPress={whenReady(canUseConnection, () => setConfiguration("edit"))}
												/>
												{!instance.isDefault && (
													<Row
														label="Make default"
														tone="accent"
														disabled={surface.busy || core.writesRefused || stale || !ready}
														onPress={() => {
															void act(() => surface.setDefault(instance.name));
														}}
													/>
												)}
												{instance.hasStoredFile && instance.activeSource !== "store" && (
													<Row
														label={json ? "Clear stored credential JSON" : "Clear stored key"}
														tone="danger"
														disabled={surface.busy || stale || !ready}
														onPress={() => {
															// A destination the hub cannot fingerprint has
															// no endpoint to assert: refuse with a reason
															// rather than grey the control out silently.
															if (fingerprintUnavailable(instance)) {
																setActionError(FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE);
																return;
															}
															confirm(
																json ? "Clear stored credential JSON?" : "Clear stored key?",
																json ? "Clear JSON" : "Clear key",
																() => surface.clearStoredKey(instance.name, instance.endpointFingerprint),
																{ endpointAsserted: true },
															);
														}}
													/>
												)}
												{["store", "oauth"].includes(instance.activeSource) && (
													<Row
														label="Clear credentials"
														tone="danger"
														disabled={surface.busy || stale || !ready}
														onPress={() => {
															if (fingerprintUnavailable(instance)) {
																setActionError(FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE);
																return;
															}
															confirm(
																"Clear credentials?",
																"Clear credentials",
																() => surface.logout(instance.name, instance.endpointFingerprint),
																{ endpointAsserted: true },
															);
														}}
													/>
												)}
												{!fromEnvironment(instance) && (
													<Row
														label="Remove"
														tone="danger"
														disabled={surface.busy || core.writesRefused || stale || !ready}
														onPress={() => {
															if (fingerprintUnavailable(instance)) {
																setActionError(FINGERPRINT_UNAVAILABLE_ACTION_MESSAGE);
																return;
															}
															confirm(
																"Remove provider?",
																"Remove",
																() => surface.remove(instance.name, instance.endpointFingerprint),
																{ endpointAsserted: true },
															);
														}}
													/>
												)}
											</Group>
										</>
									)}
								</>
							) : null}
						</GroupedPage>
					</Sheet>
				)}
			</ModalFrame>
		</>
	);
}

/** What a provider is: its sign-in state (amber only when expired), its
 * type, how it signs in and where it points, where its credential comes
 * from, and the models it offers. */
function ProviderFacts({
	instance,
	auth,
}: {
	instance: InstanceEntry;
	auth: ReadonlyMap<string, AuthStatusResponse> | null;
}) {
	const status = statusOf(instance, auth);
	const defaultTag = instance.isDefault ? ({ text: "Default", tone: "gray" } as const) : null;
	const models = (instance.models ?? []).filter((model) => !model.disabled);
	return (
		<>
			<Group>
				{status ? (
					<Row
						label="Status"
						value={<RowValue text={status.word} tone={status.tone === "attention" ? "attention" : "normal"} />}
						accessibilityLabel={`Status, ${status.word}`}
					/>
				) : null}
				<Row
					label="Type"
					value={<RowValue text={instance.providerId} tag={defaultTag} />}
					accessibilityLabel={defaultTag ? `Type, ${instance.providerId}, Default` : `Type, ${instance.providerId}`}
				/>
				<Row label="Sign-in" value={signInKind(instance)} />
				<Row label="Endpoint" sub={styleInfoText(instance)} machineSub />
				{fromEnvironment(instance) ? <Row label="Defined in" value="Environment" /> : null}
				{credentialLayers(instance).map((layer) =>
					layer.effective ? (
						<Row key={layer.source} label="Credential" sub={layer.label} />
					) : (
						<Row key={layer.source} label="Also stored" sub={layer.label} value="Not used" />
					),
				)}
			</Group>
			{instance.warnings?.map((message) => (
				<GroupFooter key={message} tone="attention">
					{message}
				</GroupFooter>
			))}
			{models.length > 0 ? (
				<Group label="Models">
					{models.map((model) => (
						<Row key={model.id} label={model.id} machineLabel />
					))}
				</Group>
			) : (
				<>
					<GroupLabel>Models</GroupLabel>
					<GroupFooter>No models listed</GroupFooter>
				</>
			)}
		</>
	);
}
