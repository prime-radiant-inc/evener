// New session (spec 11): a large sheet from the Board's New session button
// that holds its own stack, so the pickers push inside the sheet and Back
// returns to the form (ruling 1). The sheet owns the creation store, bound to
// the hub while it is ready, the hub's hosts and live sessions, and this
// phone's memory of starts, and hands them to its pages through
// newSessionContext.tsx. Swiping it down closes it and keeps the draft
// (ruling 18).
import { randomUUID } from "expo-crypto";
import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect, useMemo, useRef } from "react";
import { useStore } from "zustand";
import { LOCAL_HOST } from "../../../cmd/evener-hub/frontend/src/stores/hostRouting";
import { createNewSessionService } from "../../../mobile/src/services/newSession";
import { isReady } from "../connectionDisplay";
import { useHubFleet } from "../hosts/useHubFleet";
import { nativeDrafts } from "../nativeDrafts";
import { createNewSessionStore } from "../newSession";
import { useRetainedScreenConnection } from "../retainedScreen";
import type { Routes } from "../screens";
import { sheetStackOptions } from "../sheet/sheetStack";
import { useColors } from "../ui";
import { BrowseFolders } from "./BrowseFolders";
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

const NewSessionStack = createNativeStackNavigator<NewSessionRoutes>();

export function NewSessionSheet({ route }: NativeStackScreenProps<Routes, "NewSession">) {
	const { hubId, hubName, like } = route.params;
	const { activeProfile, client, state, renderClient } = useRetainedScreenConnection(hubId);
	const { palette } = useColors();
	const ready = activeProfile?.id === hubId && isReady(state) && !!client;
	const store = useMemo(() => createNewSessionStore(hubId, () => nativeDrafts().creation), [hubId]);
	const memory = useMemo(() => launchMemory(hubId), [hubId]);

	// The form is placed once, as it opens with its draft loaded.
	const storageLoaded = useStore(store, (form) => form.storageLoaded);
	const opened = useRef(false);
	const stopOpening = useRef(() => {});
	useEffect(() => {
		if (opened.current || !storageLoaded) return;
		opened.current = true;
		stopOpening.current = openForm(store, memory.history(), like);
	}, [store, memory, like, storageLoaded]);
	useEffect(() => () => stopOpening.current(), []);

	const service = useMemo(() => (ready && client ? createNewSessionService(client) : null), [ready, client]);
	useEffect(() => {
		store.getState().bind(service);
		if (service) {
			void store.getState().loadMetadata();
			void store.getState().loadModels(true);
		}
		return () => store.getState().bind(null);
	}, [store, service]);

	const { hosts, live } = useHubFleet(renderClient, randomUUID);
	const hostLabel = useCallback((host: string) => (host === LOCAL_HOST ? hubName : host), [hubName]);
	const plugins = useSheetPlugins(store, renderClient, ready);
	const value = useMemo<NewSessionContextValue>(
		() => ({ store, hubId, hubName, client: renderClient, ready, hosts, live, memory, hostLabel, plugins }),
		[store, hubId, hubName, renderClient, ready, hosts, live, memory, hostLabel, plugins],
	);
	return (
		<NewSessionProvider value={value}>
			<NewSessionStack.Navigator screenOptions={sheetStackOptions(palette)}>
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
