import { type PluginComponentsReadArgs, pluginComponentsReadInputs } from "@evener/appwire-client";
import { useScopedHostRead } from "./useScopedHostRead";

export function usePluginComponents(args: PluginComponentsReadArgs, resolvedRevision = "") {
  const inputs = pluginComponentsReadInputs(args);
  return useScopedHostRead({ ...inputs, inputKey: JSON.stringify([inputs.inputKey, resolvedRevision]) });
}
