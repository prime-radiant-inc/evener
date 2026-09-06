import {
  DarkTheme,
  DefaultTheme,
  NavigationContainer,
  type NavigationState,
} from "@react-navigation/native";
import { createNativeStackNavigator } from "@react-navigation/native-stack";
import { StatusBar } from "expo-status-bar";
import { useEffect, useState } from "react";
import { ActivityIndicator, useColorScheme, View } from "react-native";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { ConnectionProvider, useConnection } from "./src/ConnectionProvider";
import { locationForRoute, restoredStack } from "./src/location";
import { NewSessionScreen } from "./src/NewSessionScreen";
import { locations } from "./src/nativeLocation";
import {
  ConversationScreen,
  HubsScreen,
  type Routes,
  SessionsScreen,
} from "./src/screens";
import { ErrorMessage, useColors } from "./src/ui";

const Stack = createNativeStackNavigator<Routes>();

export default function App() {
  return (
    <SafeAreaProvider>
      <ConnectionProvider>
        <Navigation />
      </ConnectionProvider>
    </SafeAreaProvider>
  );
}
function Navigation() {
  const { loading, initialLocation, activeProfile, restorationError } =
    useConnection();
  const [state, setState] = useState<NavigationState>();
  const [saveError, setSaveError] = useState<string | null>(null);
  useEffect(() => {
    if (!state || loading) return;
    const route = state.routes[state.index];
    if (!route) return;
    try {
      locations.save(locationForRoute(route, activeProfile?.id ?? null));
      setSaveError(null);
    } catch {
      setSaveError(
        "This screen could not be saved for reopening after restart.",
      );
    }
  }, [state, activeProfile?.id, loading]);
  const colors = useColors();
  const dark = useColorScheme() === "dark";
  if (loading)
    return (
      <View
        style={{
          flex: 1,
          justifyContent: "center",
          backgroundColor: colors.background,
        }}
      >
        <ActivityIndicator accessibilityLabel="Loading saved hubs" />
      </View>
    );
  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <ErrorMessage message={saveError || restorationError} />
      <NavigationContainer
        initialState={restoredStack(initialLocation)}
        onStateChange={setState}
        theme={dark ? DarkTheme : DefaultTheme}
      >
        <StatusBar style={dark ? "light" : "dark"} />
        <Stack.Navigator
          screenOptions={{
            headerStyle: { backgroundColor: colors.background },
            headerTintColor: colors.accent,
            headerTitleStyle: { color: colors.text },
            headerShadowVisible: false,
            headerBackButtonDisplayMode: "minimal",
            contentStyle: { backgroundColor: colors.background },
          }}
        >
          <Stack.Screen
            name="Hubs"
            component={HubsScreen}
            options={{ title: "Evener · Hubs" }}
          />
          <Stack.Screen name="Sessions" component={SessionsScreen} />
          <Stack.Screen
            name="NewSession"
            component={NewSessionScreen}
            options={{ title: "New session" }}
          />
          <Stack.Screen
            name="Conversation"
            component={ConversationScreen}
            options={({ route }) => ({
              title: route.params.title || "Conversation",
            })}
          />
        </Stack.Navigator>
      </NavigationContainer>
    </View>
  );
}
