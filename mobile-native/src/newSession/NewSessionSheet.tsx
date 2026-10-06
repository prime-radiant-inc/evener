// New session (spec 11): a large sheet from the Board's New session button
// that holds its own stack, so the pickers push inside the sheet and Back
// returns to the form (ruling 1). The sheet binds the hub's creation store
// (creations.ts) while the hub is ready, holds the hub's hosts and live
// sessions and this phone's memory of starts, and hands them to its pages
// through newSessionContext.tsx. Swiping it down closes it and keeps the draft
// (ruling 18).
import { randomUUID } from "expo-crypto";
import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { useStore } from "zustand";
import { LOCAL_HOST } from "@evener/appwire-client";
import { hasHub } from "../connection";
import { useConnection } from "../ConnectionProvider";
import { isReady } from "../connectionDisplay";
import { useHubFleet } from "../hosts/useHubFleet";
import { nativeDrafts } from "../nativeDrafts";
import { useRetainedScreenConnection } from "../retainedScreen";
import type { Routes } from "../screens";
import { useSheetStackOptions } from "../sheet/sheetStack";
import { BrowseFolders } from "./BrowseFolders";
import { bindCreation, creationStore } from "./creations";
import { AccessPicker } from "./AccessPicker";
import { HostPicker } from "./HostPicker";
import { ModelPicker } from "./ModelPicker";
import { MoreOptions } from "./MoreOptions";
import { launchMemory } from "./nativeLaunchMemory";
import { NewSessionForm } from "./NewSessionForm";
import { type NewSessionContextValue, NewSessionProvider, type NewSessionRoutes } from "./newSessionContext";
import { openForm } from "./openForm";
import { PluginChecklist } from "./PluginChecklist";
import { ProjectPicker } from "./ProjectPicker";
import { useSheetPlugins } from "./sheetPlugins";
import { useSheetLaunchDefaults } from "./useLaunchDefaults";

const NewSessionStack = createNativeStackNavigator<NewSessionRoutes>();

export function NewSessionSheet(props: NativeStackScreenProps<Routes, "NewSession">) {
	const { profiles } = useConnection();
	const { navigation } = props;
	// A removed hub has no store to make or show: its sheet closes, whether the
	// hub went while the sheet was open or on its way here. It closes in a
	// layout effect, before the empty sheet is ever painted.
	const removed = !hasHub(profiles, props.route.params.hubId);
	useLayoutEffect(() => {
		if (removed) navigation.goBack();
	}, [removed, navigation]);
	if (removed) return null;
	return <NewSessionSheetBody {...props} />;
}

function NewSessionSheetBody({ route }: NativeStackScreenProps<Routes, "NewSession">) {
	const { hubId, hubName, like } = route.params;
	const { activeProfile, client, state, renderClient } = useRetainedScreenConnection(hubId);
	const sheetStackOptions = useSheetStackOptions();
	const ready = activeProfile?.id === hubId && isReady(state) && !!client;
	const store = creationStore(hubId, () => nativeDrafts().creation);
	const memory = useMemo(() => launchMemory(hubId), [hubId]);

	// The form is placed once, as it opens with its draft loaded.
	const storageLoaded = useStore(store, (form) => form.storageLoaded);
	const opened = useRef(false);
	const stopOpening = useRef(() => {});
	useEffect(() => {
		if (opened.current || !storageLoaded) return;
		opened.current = true;
		stopOpening.current = openForm(store, memory.lastSetup(), like);
	}, [store, memory, like, storageLoaded]);
	useEffect(() => () => stopOpening.current(), []);

	const bindTo = ready && client ? client : null;
	// A new or lost connection rebinds the store, which makes anything in
	// flight on the old one obsolete. Closing the sheet doesn't: a start the
	// sheet was swiped away from still lands, so the form can say the session
	// started and clear its draft rather than leave it to be started twice.
	useEffect(() => {
		bindCreation(store, bindTo);
		if (bindTo) {
			void store.getState().loadMetadata();
			void store.getState().loadModels(true);
		}
	}, [store, bindTo]);
	// The hub announces a refreshed model list on evener/auth/updated (it
	// serves a stale list at once and refreshes it behind the request), so the
	// form reads its list again in place (#3539).
	useEffect(
		() =>
			bindTo?.onNotification((notification) => {
				if (notification.method === "evener/auth/updated") void store.getState().refreshModels();
			}),
		[store, bindTo],
	);

	const { hosts, live } = useHubFleet(renderClient, randomUUID);
	const hostLabel = useCallback((host: string) => (host === LOCAL_HOST ? hubName : host), [hubName]);
	const plugins = useSheetPlugins(store, renderClient, ready);
	const launchDefaults = useSheetLaunchDefaults(store, renderClient, ready);
	const value = useMemo<NewSessionContextValue>(
		() => ({
			store,
			hubId,
			hubName,
			client: renderClient,
			ready,
			hosts,
			live,
			memory,
			hostLabel,
			plugins,
			launchDefaults,
		}),
		[store, hubId, hubName, renderClient, ready, hosts, live, memory, hostLabel, plugins, launchDefaults],
	);
	return (
		<NewSessionProvider value={value}>
			<NewSessionStack.Navigator screenOptions={sheetStackOptions}>
				{/* The form sets its own title and its Cancel and Start. */}
				<NewSessionStack.Screen name="Form" component={NewSessionForm} />
				<NewSessionStack.Screen name="Host" component={HostPicker} options={{ title: "Host" }} />
				<NewSessionStack.Screen name="Project" component={ProjectPicker} options={{ title: "Project" }} />
				<NewSessionStack.Screen name="Browse" component={BrowseFolders} options={{ title: "Browse folders" }} />
				<NewSessionStack.Screen name="Model" component={ModelPicker} options={{ title: "Model" }} />
				<NewSessionStack.Screen name="Plugins" component={PluginChecklist} options={{ title: "Plugins" }} />
				<NewSessionStack.Screen name="Access" component={AccessPicker} options={{ title: "Access" }} />
				<NewSessionStack.Screen name="MoreOptions" component={MoreOptions} options={{ title: "More options" }} />
			</NewSessionStack.Navigator>
		</NewSessionProvider>
	);
}
