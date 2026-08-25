import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { StoreApi } from "zustand/vanilla";
import { canonicalFixture } from "../core/fixtures";
import { createNavigationController } from "../core/history";
import type { DiagnosticSink } from "../core/model";
import {
  getPlatformPrimitives,
  type PlatformDetectionInput,
  type PlatformPrimitives,
  resolveEffectiveAppearance,
  resolveEffectiveReducedMotion,
  resolvePlatform,
} from "../core/platform";
import type { PrototypeAction } from "../core/state";
import {
  createPrototypeStore,
  type PreferenceStorage,
  PrototypeProvider,
  type PrototypeStore,
  usePrototypeState,
} from "../core/store";
import { installViewportMetrics } from "../core/viewport";
import { ConceptGallery } from "./ConceptGallery";
import { LabControls } from "./LabControls";
import { RecoveryBoundary } from "./RecoveryBoundary";

interface DiagnosticConsole {
  warn(message: string, detail: { code: string; path: string }): void;
}

export function createRuntimeDiagnosticSink(
  enabled: boolean,
  logger: DiagnosticConsole = console,
): DiagnosticSink {
  if (!enabled) return { report: () => {} };
  return {
    report(diagnostic) {
      logger.warn("Evener concept diagnostic", {
        code: diagnostic.code,
        path: diagnostic.path,
      });
    },
  };
}

const diagnosticsEnabled =
  import.meta.env.DEV ||
  import.meta.env.MODE === "test" ||
  import.meta.env.MODE === "browser-test";
const runtimeDiagnostics = createRuntimeDiagnosticSink(diagnosticsEnabled);

export interface AppProps {
  platformInput: PlatformDetectionInput;
  storage: PreferenceStorage;
  fixtureInput?: unknown;
}

interface SystemPreferences {
  prefersDark: boolean;
  prefersReducedMotion: boolean;
}

function useSystemPreferences(): SystemPreferences {
  const [preferences, setPreferences] = useState<SystemPreferences>(() => ({
    prefersDark: window.matchMedia("(prefers-color-scheme: dark)").matches,
    prefersReducedMotion: window.matchMedia("(prefers-reduced-motion: reduce)")
      .matches,
  }));

  useEffect(() => {
    const darkQuery = window.matchMedia("(prefers-color-scheme: dark)");
    const motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
    const updateDark = () =>
      setPreferences((current) => ({
        ...current,
        prefersDark: darkQuery.matches,
      }));
    const updateMotion = () =>
      setPreferences((current) => ({
        ...current,
        prefersReducedMotion: motionQuery.matches,
      }));

    updateDark();
    updateMotion();
    darkQuery.addEventListener("change", updateDark);
    motionQuery.addEventListener("change", updateMotion);
    return () => {
      darkQuery.removeEventListener("change", updateDark);
      motionQuery.removeEventListener("change", updateMotion);
    };
  }, []);

  return preferences;
}

function FoundationApp({
  store,
  primitives,
}: {
  store: StoreApi<PrototypeStore>;
  primitives: PlatformPrimitives;
}) {
  const appearance = usePrototypeState((state) => state.appearance);
  const textScale = usePrototypeState((state) => state.textScale);
  const reducedMotion = usePrototypeState((state) => state.reducedMotion);
  const overlay = usePrototypeState((state) => state.overlay);
  const { prefersDark, prefersReducedMotion } = useSystemPreferences();
  const navigation = useRef<ReturnType<
    typeof createNavigationController
  > | null>(null);

  const dispatch = useCallback((action: PrototypeAction) => {
    navigation.current?.dispatch(action);
  }, []);

  useEffect(() => installViewportMetrics(window, document.documentElement), []);

  useEffect(() => {
    const controller = createNavigationController(store, window);
    navigation.current = controller;
    return () => {
      navigation.current = null;
      controller.dispose();
    };
  }, [store]);

  useEffect(() => {
    const root = document.documentElement;
    root.dataset.appearance = resolveEffectiveAppearance(
      appearance,
      prefersDark,
    );
    root.dataset.reducedMotion = String(
      resolveEffectiveReducedMotion(reducedMotion, prefersReducedMotion),
    );
    root.dataset.platform = primitives.platform;
    root.dataset.textScale = textScale;
  }, [
    appearance,
    prefersDark,
    prefersReducedMotion,
    primitives,
    reducedMotion,
    textScale,
  ]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") dispatch({ type: "goBack" });
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [dispatch]);

  return (
    <main
      className="foundation-app"
      data-network-mode="offline"
      data-navigation={primitives.navigation}
      data-title={primitives.title}
      data-sheet={primitives.sheet}
      data-dialog={primitives.dialog}
      data-elevation={primitives.elevation}
      data-feedback={primitives.feedback}
      data-back={primitives.back}
      data-safe-area={primitives.safeArea}
      data-minimum-target={primitives.minimumTarget}
    >
      <RecoveryBoundary resetPrototype={() => dispatch({ type: "reset" })}>
        <div
          data-testid="foundation-background"
          aria-hidden={overlay !== null || undefined}
          inert={overlay !== null || undefined}
        >
          <ConceptGallery primitives={primitives} dispatch={dispatch} />
        </div>
        <LabControls primitives={primitives} dispatch={dispatch} />
      </RecoveryBoundary>
    </main>
  );
}

export function App({
  platformInput,
  storage,
  fixtureInput = canonicalFixture,
}: AppProps) {
  const platform = useMemo(
    () => resolvePlatform(platformInput),
    [platformInput],
  );
  const primitives = useMemo(() => getPlatformPrimitives(platform), [platform]);
  const store = useMemo(
    () =>
      createPrototypeStore({
        platform,
        storage,
        fixtureInput,
        diagnostics: runtimeDiagnostics,
      }),
    [fixtureInput, platform, storage],
  );

  return (
    <PrototypeProvider store={store}>
      <FoundationApp store={store} primitives={primitives} />
    </PrototypeProvider>
  );
}
