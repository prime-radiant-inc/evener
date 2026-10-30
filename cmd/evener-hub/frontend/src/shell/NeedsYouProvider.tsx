import {
  createContext,
  type ReactElement,
  type ReactNode,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
} from "react";
import { createNeedsYouController, type NeedsYouController, type NeedsYouSnapshot } from "./needsYouActions";

type NeedsYouValue = { controller: NeedsYouController; snapshot: NeedsYouSnapshot };
const NeedsYouContext = createContext<NeedsYouValue | null>(null);

export function NeedsYouProvider({ children }: { children: ReactNode }): ReactElement {
  // The controller is built during render so the shell's content commits on the
  // first pass: withholding children for an effect delayed placement behind the
  // provider and broke startup ordering. Building is pure - observation starts
  // on the first subscriber's effect - so a render React discards releases
  // nothing.
  const [controller, setController] = useState<NeedsYouController>(() =>
    createNeedsYouController({ observeOnSubscribe: true }),
  );
  useEffect(() => {
    // A StrictMode remount disposes the controller before running this effect
    // again; replace it rather than hand consumers a released owner.
    if (controller.disposed) {
      setController(createNeedsYouController({ observeOnSubscribe: true }));
      return;
    }
    return () => controller.dispose();
  }, [controller]);
  return <ConnectedNeedsYouProvider controller={controller}>{children}</ConnectedNeedsYouProvider>;
}

function ConnectedNeedsYouProvider({
  controller,
  children,
}: {
  controller: NeedsYouController;
  children: ReactNode;
}): ReactElement {
  const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot);
  return <NeedsYouContext.Provider value={{ controller, snapshot }}>{children}</NeedsYouContext.Provider>;
}

export function useNeedsYou(): NeedsYouValue {
  const value = useContext(NeedsYouContext);
  if (!value) throw new Error("useNeedsYou must be used within NeedsYouProvider");
  return value;
}

export function useOptionalNeedsYou(): NeedsYouValue | null {
  return useContext(NeedsYouContext);
}
