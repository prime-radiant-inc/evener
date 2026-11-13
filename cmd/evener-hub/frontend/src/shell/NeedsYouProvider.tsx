import { type ReactElement, type ReactNode, useEffect, useState, useSyncExternalStore } from "react";
import { createNeedsYouController, type NeedsYouController } from "./needsYouActions";
import { NeedsYouContext, NeedsYouControllerContext } from "./needsYouContext";

export { useNeedsYou, useOptionalNeedsYou, useOptionalNeedsYouController } from "./needsYouContext";

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
  return (
    <NeedsYouControllerContext.Provider value={controller}>
      <NeedsYouContext.Provider value={{ controller, snapshot }}>{children}</NeedsYouContext.Provider>
    </NeedsYouControllerContext.Provider>
  );
}
