import { type PluginPreviewReadArgs, pluginPreviewReadInputs } from "@evener/appwire-client";
import { useScopedHostRead } from "./useScopedHostRead";

export function usePluginPreview(args: PluginPreviewReadArgs) {
  return useScopedHostRead(pluginPreviewReadInputs(args));
}
