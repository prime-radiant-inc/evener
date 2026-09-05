import {
  DarkTheme,
  DefaultTheme,
  NavigationContainer,
} from "@react-navigation/native";
import { createNativeStackNavigator } from "@react-navigation/native-stack";
import { StatusBar } from "expo-status-bar";
import { useColorScheme } from "react-native";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { ConnectionProvider } from "./src/ConnectionProvider";
import { NewSessionScreen } from "./src/NewSessionScreen";
import {
  ConversationScreen,
  HubsScreen,
  type Routes,
  SessionsScreen,
} from "./src/screens";

import { useColors } from "./src/ui";

const Stack = createNativeStackNavigator<Routes>();

export default function App() {
  const colors = useColors();
  const dark = useColorScheme() === "dark";
  return (
    <SafeAreaProvider>
      <ConnectionProvider>
        <NavigationContainer theme={dark ? DarkTheme : DefaultTheme}>
          <StatusBar style={dark ? "light" : "dark"} />
          <Stack.Navigator
            screenOptions={{
              headerStyle: { backgroundColor: colors.background },
              headerTintColor: colors.accent,
              headerTitleStyle: { color: colors.text },
              headerShadowVisible: false,
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
      </ConnectionProvider>
    </SafeAreaProvider>
  );
}
