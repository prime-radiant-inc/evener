import { type PluginComponentsReadArgs, pluginComponentsReadInputs } from "@evener/appwire-client";
import { useScopedHostRead } from "./useScopedHostRead";

export function usePluginComponents(args: PluginComponentsReadArgs) {
  return useScopedHostRead(pluginComponentsReadInputs(args));
}
