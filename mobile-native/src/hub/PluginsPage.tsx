// The Hub's Plugins page (spec 12; rulings 7 and 9): the plugins installed on
// the hub, grouped by marketplace, each with its "On by default" switch and a
// detail sheet; the marketplaces, added, refreshed and removed; and Browse, the
// marketplaces' catalogs to install from. The mutation gate, the applied-removal
// guard and the stores live here, above the connection's early returns, and the
// details open as sheets over the list so that machinery stays with it.
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
	Fragment,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import { ActivityIndicator, Alert } from "react-native";
import type {
	AnyNotification,
	ConnectionState,
	MarketplaceEntry,
	MethodName,
	MethodTypes,
	PluginEntry,
	PluginRefParams,
} from "@evener/appwire-client";
import { createMarketplacesStore, createPluginsStore } from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { isReady } from "../connectionDisplay";
import { destructiveButton } from "../haptics";
import { INSTALLED_PLUGINS_FAILED, MarketplaceBrowser } from "../MarketplaceBrowser";
import {
	createPluginMutationGate,
	PLUGIN_MUTATION_BUSY,
	runGatedMutation,
	type PluginMutationGate,
} from "../pluginMutationGate";
import { HUB_NO_LONGER_SELECTED, useRetainedScreenConnection } from "../retainedScreen";
import { Group, GroupedPage, GroupFooter, GroupGap, Row, SearchField, Segmented, SwitchRow } from "../sheet/Grouped";
import { ModalSheet } from "../sheet/ModalSheet";
import { Connecting, SheetStatus } from "../sheet/SheetStatus";
import type { HubRoutes } from "./hubSheetContext";

type Segment = "installed" | "marketplaces" | "browse";

const SEGMENTS: readonly { value: Segment; label: string }[] = [
	{ value: "installed", label: "Installed" },
	{ value: "marketplaces", label: "Marketplaces" },
	{ value: "browse", label: "Browse" },
];

type PluginFocus = NonNullable<HubRoutes["Plugins"]["focus"]>;

/** The applied marketplace removals one client's writes reported, keyed by
 * name with the publication version each fence predates: a name is one a
 * write said the hub already removed, held with the client whose write said
 * so because a fresh browser must not offer Remove again for it. Every name
 * carries its OWN baseline - the store's publication version at the outcome
 * that recorded it - and retires with the first authoritative read newer
 * than that baseline (reconcileAppliedRemovals): one watermark shared
 * browser-wide would advance on ANY later outcome's recording, past a read
 * that had not been reported yet, and the earlier fence would then wait for
 * a retirement read that already landed. Per-name baselines are the shape
 * the web guard keeps (marketplacesPlugins/index.tsx). The removed
 * registrations' wire identities are deliberately not stored: the fallback
 * retires on the read's arrival, not its contents, and the durable fix (a
 * hub-assigned registration id the wire can compare, PR #2137's protocol
 * backlog) is what will put identity back into the comparison. */
type AppliedRemovalGuard = {
	client: ConversationClientLike | null;
	publicationVersions: ReadonlyMap<string, number>;
};

const EMPTY_APPLIED_REMOVALS: ReadonlySet<string> = new Set();

// A mounted page re-keyed to another hub is a fresh page: the
// reconnect-retention state below - the display's everReady, the last
// client a retry's gap renders through - belongs to the hub it was built
// for, and a re-key remounts the body whole (the keyed wrapper's own
// rationale: useRetainedScreenConnection's doc).
export function PluginsPage(props: NativeStackScreenProps<HubRoutes, "Plugins">) {
	return <PluginsPageBody key={props.route.params.hubId} {...props} />;
}

function PluginsPageBody({ route, navigation }: NativeStackScreenProps<HubRoutes, "Plugins">) {
	// One plugin mutation at a time, across this list AND the browser: switching
	// tabs unmounts whichever one started it, so the gate lives here, at the
	// screen's own top level - above the early returns below, which unmount and
	// remount the ready-only child on every connection transition. A gate held
	// inside that child would be destroyed mid-mutation by the same transition
	// a write outlives, and a reopened screen would permit a second write
	// beside the first. It belongs to this screen rather than to the client - a
	// mutation outlives the store it was issued on, so a gate rebuilt per
	// client would let the next one start beside it - and is held the way the
	// credential store is (credentialStore.ts), as committed state a discarded
	// render cannot leave behind.
	const [gate] = useState(createPluginMutationGate);
	const { activeProfile, client, state, display, canUseConnection, renderClient } = useRetainedScreenConnection(
		route.params.hubId,
	);
	// An applied marketplace removal's residue lives beside the gate, above the
	// early returns below, for the same reason it does: they unmount and remount
	// the ready-only child on every connection transition, and the browser that
	// asked for the write does not outlive even a tab switch - a remount
	// replaces its store, and its select() clears the slot an outcome rendered
	// into. The guard fences a name the hub already removed and the warning
	// survives every one of those remounts, both scoped to the client whose
	// write reported them, so a replaced client's late result changes nothing
	// (currentClient's checks below).
	const currentClient = useRef<ConversationClientLike | null>(null);
	const [marketplaceWarning, setMarketplaceWarning] = useState<{
		client: ConversationClientLike;
		name: string;
		text: string;
	} | null>(null);
	const [appliedRemovalGuard, setAppliedRemovalGuard] = useState<AppliedRemovalGuard>(() => ({
		client: null,
		publicationVersions: new Map(),
	}));
	// The guard's own store is a ref, so the recording paths below check and
	// store against one synchronous source; the state beside it exists only to
	// publish each change to renders (appliedRemovalNames below).
	// markAppliedRemoval's return has to mean the screen actually accepted the
	// outcome - fence or no fence - and an updater's view of the guard arrives
	// only when React applies it: a client switch landing between a passing
	// check and that apply would let the function answer true for a store the
	// updater then refused. Reading the same ref the entry lands in, in one
	// synchronous block nothing can interleave, makes true structural.
	const appliedRemovalGuardRef = useRef<AppliedRemovalGuard>(appliedRemovalGuard);
	const appliedRemovalNames =
		appliedRemovalGuard.client === client
			? new Set(appliedRemovalGuard.publicationVersions.keys())
			: EMPTY_APPLIED_REMOVALS;
	const visibleMarketplaceWarning = marketplaceWarning?.client === client ? marketplaceWarning.text : null;
	useEffect(() => {
		// Assigned in an effect, never during render: mutating a ref mid-render
		// is unsafe under concurrent rendering, and effects run before any
		// outcome a replaced client could send arrives.
		currentClient.current = client ?? null;
		if (appliedRemovalGuardRef.current.client !== (client ?? null)) {
			// A replaced client's fence is not the replacement's: reset the store
			// and its render mirror together, the way every guard change does.
			appliedRemovalGuardRef.current = {
				client: client ?? null,
				publicationVersions: new Map(),
			};
			setAppliedRemovalGuard(appliedRemovalGuardRef.current);
		}
		setMarketplaceWarning((current) => (current?.client === client ? current : null));
	}, [client]);
	// The fallback ruling: a guard name retires with the FIRST authoritative
	// read that lands after the outcome recorded it. A read omitting the name
	// reconciles the removal the way it always did; a read still CARRYING it -
	// whatever registration the row bears, even the removed one's own
	// whole-second identity, which the wire cannot tell from a stale read -
	// now retires the fence too. Once the outcome said the removal stood, a
	// row a trusted read vouches for can only be a hub re-registration, and
	// the fence's alternative is a permanent lockout: a same-source
	// same-second re-registration is indistinguishable on the wire, so keeping
	// the fence for it could never be undone from this client. The worst case
	// is a row the read had not caught up with - pressing Remove on it draws
	// the same idempotent applied outcome, which re-fences the name and
	// re-raises the warning. The durable fix is a hub-assigned registration id
	// the wire can compare (protocol backlog, PR #2137).
	//
	// WHICH read retires a name is per name: the read's publication version
	// must be newer than the baseline the recording outcome left for THAT name
	// alone (the guard's own doc). A read published before a later outcome's
	// recording - one a browser-wide watermark would have swallowed by
	// advancing to it - still reaches every earlier fence here, so an earlier
	// fence never misses its retirement read. The read's contents no longer
	// decide anything - its arrival does - so the list it carried goes
	// unread here; the report's shape stays the browser's contract. Only
	// reads the browser's own revision fencing already vouches for are
	// reported, so stale and in-flight replies never retire anything.
	const reconcileAppliedRemovals = useCallback(
		(_marketplaces: readonly MarketplaceEntry[], owner: ConversationClientLike, publicationVersion: number): void => {
			if (currentClient.current !== owner) return;
			const current = appliedRemovalGuardRef.current;
			if (current.client !== owner || !current.publicationVersions.size) return;
			const next = new Map(current.publicationVersions);
			for (const [name, baseline] of next) {
				if (publicationVersion > baseline) next.delete(name);
			}
			if (next.size === current.publicationVersions.size) return;
			appliedRemovalGuardRef.current = {
				client: owner,
				publicationVersions: next,
			};
			setAppliedRemovalGuard(appliedRemovalGuardRef.current);
		},
		[],
	);
	// The browser's recording path for an applied removal: fences the name
	// against the publication baseline the store carried when the outcome
	// landed - the snapshot and version the browser read off the store, which
	// the store's rejection processing has already settled - for the window
	// between the outcome and the first authoritative read after it
	// (reconcile's doc), sets the warning to the outcome's own notice, and
	// answers whether the outcome was actually accepted: false means it came
	// from a client this screen has replaced, and the browser drops it whole.
	// An accepted snapshot that already omits the target is the outcome's own
	// reconciliation, so it leaves no fence at all - the shape the web's
	// sheet records (MarketplaceSheet.tsx) - while the cleanup warning still
	// reports what the outcome said.
	const markAppliedRemoval = useCallback(
		(
			name: string,
			notice: string | null,
			owner: ConversationClientLike,
			marketplaces: readonly MarketplaceEntry[] | null,
			publicationVersion: number,
		): boolean => {
			// The check reads the same synchronous store the entry lands in
			// below, so `true` structurally means the screen accepted this
			// outcome: a client switch that already landed has reset the ref and
			// answers false here, and one that lands after cannot come between the
			// check and the store - the block is synchronous. An owner the guard
			// no longer belongs to changes nothing and answers false, for the
			// browser to drop the outcome whole.
			if (currentClient.current !== owner) return false;
			const current = appliedRemovalGuardRef.current;
			if (current.client !== owner) return false;
			const publicationVersions = new Map(current.publicationVersions);
			if (marketplaces === null || marketplaces.some((item) => item.name === name))
				publicationVersions.set(name, publicationVersion);
			else publicationVersions.delete(name);
			appliedRemovalGuardRef.current = { client: owner, publicationVersions };
			setAppliedRemovalGuard(appliedRemovalGuardRef.current);
			// The warning slot reports the latest outcome for the marketplace it
			// holds: a noticed outcome replaces whatever the slot showed, and a
			// clean one (null notice) retires the warning for its own name,
			// leaving any other marketplace's warning alone.
			setMarketplaceWarning((current) =>
				notice !== null ? { client: owner, name, text: notice } : current?.name === name ? null : current,
			);
			return true;
		},
		[],
	);
	// A name this screen's own add just registered: the write replaced the
	// registration the fence guards - which the wire's whole-second
	// timestamps can fail to distinguish - so the fence clears for it here.
	const clearAddedMarketplace = useCallback((name: string, owner: ConversationClientLike): void => {
		if (currentClient.current !== owner) return;
		const current = appliedRemovalGuardRef.current;
		if (current.client !== owner || !current.publicationVersions.has(name)) return;
		const publicationVersions = new Map(current.publicationVersions);
		publicationVersions.delete(name);
		appliedRemovalGuardRef.current = { client: owner, publicationVersions };
		setAppliedRemovalGuard(appliedRemovalGuardRef.current);
	}, []);
	// A removal the hub confirmed outright: the outcome is neither a residue
	// nor a failure, and the screen-level warning reports the latest outcome
	// for the name it holds: a clean success retires the warning only for
	// ITS OWN marketplace, so an unrelated marketplace's success says nothing
	// about the clone files this one warned about.
	const clearMarketplaceWarning = useCallback((name: string, owner: ConversationClientLike): void => {
		if (currentClient.current !== owner) return;
		setMarketplaceWarning((current) => (current?.name === name ? null : current));
	}, []);
	const clearFocus = useCallback(() => navigation.setParams({ focus: undefined }), [navigation]);
	if (activeProfile?.id !== route.params.hubId)
		return (
			<GroupedPage>
				<GroupFooter>{HUB_NO_LONGER_SELECTED}</GroupFooter>
			</GroupedPage>
		);
	if (display === "wall" || !renderClient)
		return (
			<GroupedPage>
				<SheetStatus />
				<Connecting hubName={activeProfile.name} />
			</GroupedPage>
		);
	return (
		<Plugins
			key={activeProfile.id}
			client={renderClient}
			connectionState={state}
			canUseConnection={canUseConnection}
			hubName={activeProfile.name}
			gate={gate}
			appliedRemovalNames={appliedRemovalNames}
			marketplaceWarning={visibleMarketplaceWarning}
			onAppliedRemoval={markAppliedRemoval}
			onAuthoritativeMarketplaces={reconcileAppliedRemovals}
			onMarketplaceAdded={clearAddedMarketplace}
			onRemovedMarketplace={clearMarketplaceWarning}
			focus={route.params.focus}
			onFocused={clearFocus}
		/>
	);
}

function Plugins({
	client,
	connectionState,
	hubName,
	gate,
	appliedRemovalNames,
	marketplaceWarning,
	onAppliedRemoval,
	onAuthoritativeMarketplaces,
	onMarketplaceAdded,
	onRemovedMarketplace,
	canUseConnection,
	focus,
	onFocused,
}: {
	client: ConversationClientLike;
	connectionState: ConnectionState;
	hubName: string;
	gate: PluginMutationGate;
	appliedRemovalNames: ReadonlySet<string>;
	marketplaceWarning: string | null;
	onAppliedRemoval(
		name: string,
		notice: string | null,
		owner: ConversationClientLike,
		marketplaces: readonly MarketplaceEntry[] | null,
		publicationVersion: number,
	): boolean;
	onAuthoritativeMarketplaces(
		marketplaces: readonly MarketplaceEntry[],
		owner: ConversationClientLike,
		publicationVersion: number,
	): void;
	onMarketplaceAdded(name: string, owner: ConversationClientLike): void;
	onRemovedMarketplace(name: string, owner: ConversationClientLike): void;
	canUseConnection: () => boolean;
	/** A plugin to open once, from a notice (ruling 25). */
	focus: PluginFocus | undefined;
	onFocused(): void;
}) {
	const model = useMemo(() => createPluginsStore(client), [client]);
	// The hub's add answer is the one place that names what the write
	// registered, and the store cannot be trusted to hand it over: a newer
	// list read holds its publication. Capture the answer as it passes
	// through the client the marketplaces store is built on, so the
	// browser's add flow reads it independent of every store.
	const lastAddMarketplaces = useRef<readonly MarketplaceEntry[] | null>(null);
	// The marketplaces store is this screen's, not the browser's: a write
	// that outlives a tab switch keeps publishing into it, and the browser a
	// remount replaces reads what it retained instead of starting cold.
	const marketplaceStoreClient = useMemo(() => {
		const request = <M extends MethodName>(
			method: M,
			params: MethodTypes[M]["params"],
			opts?: { timeoutMs?: number },
		): Promise<MethodTypes[M]["result"]> => {
			const pending = client.request(method, params, opts);
			if (method === "evener/marketplace/add") {
				lastAddMarketplaces.current = null;
				void (pending as Promise<MethodTypes["evener/marketplace/add"]["result"]>).then(
					(answer) => {
						lastAddMarketplaces.current = answer.marketplaces;
					},
					() => {},
				);
			}
			return pending;
		};
		return {
			request,
			onNotification: (callback: (notification: AnyNotification) => void) => client.onNotification(callback),
		};
	}, [client]);
	const marketplaces = useMemo(() => createMarketplacesStore(marketplaceStoreClient), [marketplaceStoreClient]);
	const state = useSyncExternalStore(model.subscribe, model.getState);
	const ready = isReady(connectionState);
	const [panel, setPanel] = useState<Segment>("installed");
	const busy = useSyncExternalStore(gate.subscribe, gate.isBusy);
	const [selected, setSelected] = useState<PluginRefParams | null>(null);
	const [query, setQuery] = useState("");
	const [actionError, setActionError] = useState<string | null>(null);
	const [notice, setNotice] = useState<string | null>(null);
	const editorVersion = useRef(0);
	const entry = state.plugins?.find(
		(item) => item.plugin === selected?.plugin && item.marketplace === selected?.marketplace,
	);
	// The browser's first list read is a passive effect. Bind this screen-owned
	// store before child effects run so that first read is not mistaken for a
	// reconnect and issued twice by the lifecycle's wanted-list recovery.
	useLayoutEffect(() => {
		marketplaces.connectionChanged(client, connectionState);
	}, [marketplaces, client, connectionState]);
	useLayoutEffect(() => {
		marketplaces.start();
		return () => marketplaces.dispose();
	}, [marketplaces]);
	// The store's own reconnect recovery is what a banner over a live screen
	// needs: the hub broadcasts a change only to clients connected when it
	// happens, so everything that moved while this one was away arrives as
	// nothing at all, and the store re-reads a list something wants once
	// connectionChanged says the connection is ready again (storeLifecycle.ts).
	// The wall this screen used to show remounted the store on every recovery,
	// so that read happened for free with the remount; keeping the screen
	// mounted behind a banner removes it, and this drives the store through
	// every transition itself, the way useCredentialStore drives the credential
	// store (credentialStore.ts). A layout effect, so the store knows its
	// connection before the mount effect's first read issues.
	useLayoutEffect(() => {
		model.connectionChanged(client, connectionState);
	}, [model, client, connectionState]);
	useEffect(() => {
		model.start();
		void model.getState().fetchPlugins();
		return () => {
			editorVersion.current += 1;
			model.dispose();
		};
	}, [model]);
	const close = useCallback(() => {
		editorVersion.current += 1;
		setSelected(null);
		setActionError(null);
		setNotice(null);
	}, []);
	useEffect(() => {
		if (selected && state.plugins && !entry) close();
	}, [selected, state.plugins, entry, close]);
	// A notice's plugin opens once, when the list first mounts; the page
	// clears the param so a remount doesn't open it again.
	useEffect(() => {
		if (!focus) return;
		setSelected({ plugin: focus.plugin, marketplace: focus.marketplace });
		onFocused();
	}, [focus, onFocused]);
	async function act(action: () => Promise<void>, success?: () => string) {
		const version = editorVersion.current;
		const outcome = await runGatedMutation(gate, canUseConnection, action);
		if (version !== editorVersion.current) return;
		// A not-ready press never ran the action, so it must not retire the
		// diagnostics an earlier outcome left either: the copy the user was
		// reading survives the no-op, and the status the banner already shows
		// is the reason nothing ran.
		if (outcome === "not-ready") return;
		setActionError(null);
		setNotice(null);
		if (outcome === "refused") setActionError(PLUGIN_MUTATION_BUSY);
		else if (outcome === "failed")
			setActionError("Could not confirm the change. Check this plugin’s status before trying again.");
		else if (success) setNotice(success());
	}
	function remove() {
		if (!selected || busy || !canUseConnection()) return;
		const target = selected;
		const version = editorVersion.current;
		Alert.alert("Remove plugin?", `${target.plugin} from ${target.marketplace} on ${hubName}`, [
			{ text: "Cancel", style: "cancel" },
			destructiveButton("Remove", () => {
				if (version === editorVersion.current && canUseConnection())
					void act(() => state.removePlugin(target.plugin, target.marketplace));
			}),
		]);
	}
	function upgrade(target: PluginRefParams, before: PluginEntry) {
		void act(
			() => state.upgradePlugin(target.plugin, target.marketplace),
			// The upgrade answers with the whole list; the entry in it says
			// whether anything changed (ruling 7).
			() => upgradeOutcome(before, model.getState().plugins),
		);
	}
	function setOnByDefault(target: PluginRefParams, enabled: boolean) {
		void act(() =>
			enabled
				? state.enablePlugin(target.plugin, target.marketplace)
				: state.disablePlugin(target.plugin, target.marketplace),
		);
	}
	const listError = state.pluginsError === null ? null : INSTALLED_PLUGINS_FAILED;
	// Today's filter, by plugin or marketplace name: useful on a hub with many.
	const needle = query.trim().toLowerCase();
	const visible = (state.plugins ?? []).filter(
		(item) => item.plugin.toLowerCase().includes(needle) || item.marketplace.toLowerCase().includes(needle),
	);
	// A failed installed list reads again when the page comes back to the
	// front, as evener/plugin/updated already does; nothing asks you to (no
	// pull-to-refresh, no button: Task 15). Refs keep the effect's identity
	// fixed, so it runs on focus alone.
	const failed = useRef(false);
	failed.current = state.pluginsError !== null;
	useFocusEffect(
		useCallback(() => {
			if (failed.current) void model.getState().fetchPlugins();
		}, [model]),
	);
	return (
		<GroupedPage>
			<SheetStatus />
			<GroupGap />
			<Segmented<Segment> label="Plugins" options={SEGMENTS} value={panel} onChange={setPanel} />
			{marketplaceWarning ? <GroupFooter tone="danger">{marketplaceWarning}</GroupFooter> : null}
			{panel === "installed" ? (
				<>
					<GroupFooter>
						“On by default” sets which plugins new sessions start with. You can still choose per session.
					</GroupFooter>
					{listError ? <GroupFooter tone="danger">{listError}</GroupFooter> : null}
					{!selected && actionError ? <GroupFooter tone="danger">{actionError}</GroupFooter> : null}
					{state.plugins === null && state.pluginsLoading ? (
						<ActivityIndicator accessibilityLabel="Loading installed plugins" />
					) : null}
					{/* The field stays while a filter is set, so it can always be cleared. */}
					{state.plugins?.length || query !== "" ? (
						<SearchField label="Filter installed plugins" query={query} onChange={setQuery} />
					) : null}
					{state.plugins?.length === 0 && query === "" ? (
						<GroupFooter>No plugins installed on this hub.</GroupFooter>
					) : null}
					{state.plugins !== null && query !== "" && visible.length === 0 ? (
						<GroupFooter>No matching plugins.</GroupFooter>
					) : null}
					{byMarketplace(visible).map(([marketplace, plugins]) => (
						<Fragment key={marketplace}>
							<Group label={marketplace} machineLabel>
								{plugins.map((item) => {
									const sub = item.broken
										? "Broken"
										: `${item.version || "Unknown version"}${item.autoUpgrade ? " · Upgrades automatically" : ""}`;
									return (
										<SwitchRow
											key={item.plugin}
											label={item.plugin}
											sub={sub}
											subTone={item.broken ? "danger" : "normal"}
											accessibilityLabel={`${item.plugin}, ${item.marketplace}, ${sub}`}
											switchLabel={`${item.plugin} on by default`}
											value={item.enabled}
											disabled={busy || !ready}
											onChange={(enabled) => setOnByDefault(item, enabled)}
											onPress={() => {
												close();
												setSelected({ plugin: item.plugin, marketplace: item.marketplace });
											}}
										/>
									);
								})}
							</Group>
						</Fragment>
					))}
				</>
			) : (
				<MarketplaceBrowser
					key={panel}
					segment={panel}
					client={client}
					connectionState={connectionState}
					hubName={hubName}
					installed={model}
					marketplaces={marketplaces}
					lastAddMarketplaces={lastAddMarketplaces}
					gate={gate}
					ready={ready}
					canUseConnection={canUseConnection}
					onOpenPlugin={(target) => {
						close();
						setSelected(target);
					}}
					appliedRemovalNames={appliedRemovalNames}
					onAppliedRemoval={onAppliedRemoval}
					onAuthoritativeMarketplaces={onAuthoritativeMarketplaces}
					onMarketplaceAdded={onMarketplaceAdded}
					onRemovedMarketplace={onRemovedMarketplace}
				/>
			)}
			{entry && selected && (
				<ModalSheet title={entry.plugin} done={{ onPress: close }} onRequestClose={close}>
					<GroupedPage>
						{/* The sheet covers the page's status line, so it carries its own. */}
						{connectionState === "ready" ? null : <SheetStatus />}
						{entry.broken ? (
							<GroupFooter tone="danger">This plugin is broken. Upgrade it or remove it.</GroupFooter>
						) : null}
						<Group>
							<SwitchRow
								label="On by default"
								value={entry.enabled}
								disabled={busy || !ready}
								onChange={(enabled) => setOnByDefault(selected, enabled)}
							/>
							<SwitchRow
								label="Upgrade automatically"
								value={entry.autoUpgrade}
								disabled={busy || !ready}
								onChange={(value) => {
									const target = selected;
									void act(() => state.setPluginAutoUpgrade(target.plugin, target.marketplace, value));
								}}
							/>
						</Group>
						<Group>
							<Row label="Upgrade" tone="accent" disabled={busy || !ready} onPress={() => upgrade(selected, entry)} />
							<Row
								label="Remove"
								accessibilityLabel="Remove plugin"
								tone="danger"
								disabled={busy || !ready}
								onPress={remove}
							/>
						</Group>
						{actionError ? <GroupFooter tone="danger">{actionError}</GroupFooter> : null}
						{notice ? <GroupFooter>{notice}</GroupFooter> : null}
						{busy ? <ActivityIndicator accessibilityLabel="Updating plugin" /> : null}
						<Group label="Details">
							<Row label="Version" value={entry.version || "Unknown version"} />
							<Row label="Marketplace" sub={entry.marketplace} machineSub />
							<Row label={`Path on ${hubName}`} sub={entry.installPath} machineSub />
							{entry.gitCommitSha ? <Row label="Commit" sub={entry.gitCommitSha} machineSub /> : null}
						</Group>
					</GroupedPage>
				</ModalSheet>
			)}
		</GroupedPage>
	);
}

/** The installed plugins by marketplace, in the order the hub lists them. */
function byMarketplace(plugins: readonly PluginEntry[]): [string, PluginEntry[]][] {
	const groups = new Map<string, PluginEntry[]>();
	for (const item of plugins) groups.set(item.marketplace, [...(groups.get(item.marketplace) ?? []), item]);
	return [...groups];
}

/** What an upgrade did, from the entry before it and the list it answered
 * with: the same version and commit is "Already up to date" (ruling 7). */
function upgradeOutcome(before: PluginEntry, after: readonly PluginEntry[] | null): string {
	const now = after?.find((item) => item.plugin === before.plugin && item.marketplace === before.marketplace);
	if (!now || (now.version === before.version && now.gitCommitSha === before.gitCommitSha)) return "Already up to date";
	return `Upgraded to ${now.version || "an unknown version"}`;
}
