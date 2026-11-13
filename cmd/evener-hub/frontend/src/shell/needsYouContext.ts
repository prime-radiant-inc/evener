// The needs-you controller's contexts and the hooks that read them, apart
// from the provider that builds the controller: a consumer that only reads
// the shared snapshot (the rail asks whether a dialog is open) imports this
// module and none of what the controller itself depends on.
import { createContext, useContext } from "react";
import type { NeedsYouController, NeedsYouSnapshot } from "./needsYouActions";

export type NeedsYouValue = { controller: NeedsYouController; snapshot: NeedsYouSnapshot };
export const NeedsYouContext = createContext<NeedsYouValue | null>(null);
// The controller on its own: its identity never changes while the provider
// lives, so a consumer holding one keyed subscription (see
// useNextNeedsYouMount) reads the controller without also inheriting a
// re-render for every queue snapshot the full value above carries.
export const NeedsYouControllerContext = createContext<NeedsYouController | null>(null);

export function useNeedsYou(): NeedsYouValue {
  const value = useContext(NeedsYouContext);
  if (!value) throw new Error("useNeedsYou must be used within NeedsYouProvider");
  return value;
}

export function useOptionalNeedsYou(): NeedsYouValue | null {
  return useContext(NeedsYouContext);
}

export function useOptionalNeedsYouController(): NeedsYouController | null {
  return useContext(NeedsYouControllerContext);
}
