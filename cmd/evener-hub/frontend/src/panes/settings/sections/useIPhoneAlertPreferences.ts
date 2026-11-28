import { useCallback, useEffect, useState } from "react";
import { iPhoneCommand, iPhoneHost } from "../../../shell/iPhoneHost";

export interface IPhoneAlertPreferences {
  failures: boolean;
  questions: boolean;
  hold: boolean;
  haptics: boolean;
}

export type IPhoneAlertPreferenceKey = keyof IPhoneAlertPreferences;

function isAlertPreferences(value: unknown): value is IPhoneAlertPreferences {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const preferences = value as Record<string, unknown>;
  return ["failures", "questions", "hold", "haptics"].every((key) => typeof preferences[key] === "boolean");
}

export function useIPhoneAlertPreferences(): {
  preferences: IPhoneAlertPreferences | null;
  setPreference: (key: IPhoneAlertPreferenceKey, value: boolean) => void;
} {
  const [preferences, setPreferences] = useState<IPhoneAlertPreferences | null>(null);

  useEffect(() => {
    const host = iPhoneHost();
    if (!host) return;

    const receive = (event: MessageEvent) => {
      const command = iPhoneCommand(event);
      if (command?.type !== "alertPreferences") return;
      const snapshot = (command as { preferences?: unknown }).preferences;
      if (isAlertPreferences(snapshot)) setPreferences(snapshot);
    };

    window.addEventListener("message", receive);
    try {
      host.postMessage(JSON.stringify({ type: "getAlertPreferences" }));
    } catch {
      // A closing WebView can drop the request while settings is still mounted.
    }
    return () => window.removeEventListener("message", receive);
  }, []);

  const setPreference = useCallback((key: IPhoneAlertPreferenceKey, value: boolean) => {
    const host = iPhoneHost();
    if (!host) return;
    try {
      host.postMessage(JSON.stringify({ type: "setAlertPreference", key, value }));
    } catch {
      // Native state remains authoritative if the WebView is closing.
    }
  }, []);

  return { preferences, setPreference };
}
