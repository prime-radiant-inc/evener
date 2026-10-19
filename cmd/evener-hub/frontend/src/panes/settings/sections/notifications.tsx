import { useState } from "react";
import { type NotificationKey, prefsStore, usePrefsStore } from "../../../stores/prefs";
import { InlineNotice, RadioGroup, type RadioGroupOption, Switch, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./notifications.module.css";

const CLASS = {
  root: requireClass(styles.root, "notifications.module.css", "root"),
  intro: requireClass(styles.intro, "notifications.module.css", "intro"),
};

const LOUD_SCOPE_OPTIONS: RadioGroupOption[] = [
  { value: "asks", label: "Questions, reply requests, approvals & errors" },
  { value: "all", label: "Everything needing me" },
];

/**
 * Settings -> Notifications (parity-m7-settings.md §6): 4 toggles + a
 * "Loud for" radio, localStorage-only, plus the one browser-API-touching
 * flow in this section: OS notification's Notification.requestPermission()
 * gate. Originally all 4 toggles defaulted OFF - the pre-adjudicated
 * code-wins resolution of the floor doc's own copy/code discrepancy (legacy
 * copy claimed title/favicon default on; both the static markup and the JS
 * default landed all four at OFF, per assets/settings-notifications.js's
 * own `!!prefs[key]` against an empty object).
 *
 * 2026-08-14 (docs/web-ui/decisions.md): title bar count now defaults ON -
 * see stores/prefs.ts's loadNotifications for the reasoning. Favicon, OS
 * notification, and sound are still OFF by default. This section's intro
 * paragraph is worded to match that split rather than either the legacy
 * copy or the old all-OFF resolution verbatim - shipping copy that
 * contradicts the toggles rendered directly below it would be worse than
 * either "faithful" option alone.
 *
 * Browser availability is separate from the saved preference. Resolved
 * permission choices never re-prompt; failed or dismissed requests keep
 * the existing rollback to off and explain how to recover inline.
 */
export function NotificationsSection() {
  const notifications = usePrefsStore((s) => s.notifications);
  const loudScope = usePrefsStore((s) => s.notificationsLoudScope);
  const { push } = useToasts();
  const [permission, setPermission] = useState<NotificationPermission | null>(() =>
    "Notification" in window ? Notification.permission : null,
  );
  const [requestState, setRequestState] = useState<"idle" | "pending" | "failed" | "dismissed">("idle");
  let availability: string | null = null;
  if (requestState === "pending") {
    availability = "Waiting for your browser’s notification permission decision.";
  } else if (requestState === "failed") {
    availability =
      "Could not request notification permission. Desktop notifications remain off. Check this site’s browser permissions, then turn them on to try again.";
  } else if (permission === null) {
    availability =
      "Desktop notifications are unavailable in this browser. Your preference is saved, but cannot deliver notifications here. Use a browser that supports notifications, or turn on Sound.";
  } else if (permission === "denied") {
    availability =
      "Desktop notifications are blocked by your browser, regardless of the saved preference. Allow notifications in this site’s browser settings, then reload.";
  } else if (requestState === "dismissed" || (permission === "default" && notifications.os)) {
    availability = notifications.os
      ? "Browser permission has not been granted. Turn desktop notifications off and on to request permission again."
      : "Browser permission has not been granted. Turn desktop notifications on to request permission again.";
  }

  function commitPlain(key: Exclude<NotificationKey, "os">, value: boolean) {
    prefsStore.getState().setNotification(key, value);
    push("success", "Settings saved");
  }

  async function handleOsChange(value: boolean) {
    const currentPermission = "Notification" in window ? Notification.permission : null;
    setPermission(currentPermission);
    setRequestState("idle");
    // Turning off, or toggling while permission is already resolved
    // (granted/denied, not "default"): commit unconditionally - turning the
    // setting off never revokes or requests browser permission, and
    // turning it back on after a resolved decision never re-prompts.
    const shouldRequestPermission = value && currentPermission === "default";
    if (!shouldRequestPermission) {
      prefsStore.getState().setNotification("os", value);
      if (value && currentPermission !== "granted") {
        push("warning", "Preference saved, but desktop notifications are unavailable");
      } else {
        push("success", "Settings saved");
      }
      return;
    }

    setRequestState("pending");
    let permission: NotificationPermission;
    try {
      permission = await Notification.requestPermission();
    } catch {
      prefsStore.getState().setNotification("os", false);
      setRequestState("failed");
      push("warning", "Could not request notification permission");
      return;
    }
    setPermission(permission);
    setRequestState(permission === "default" ? "dismissed" : "idle");
    if (permission === "granted") {
      prefsStore.getState().setNotification("os", true);
      push("success", "Settings saved");
    } else {
      prefsStore.getState().setNotification("os", false);
      push("warning", "Browser notification permission was not granted");
    }
  }

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>
        Title bar count is on by default. Browser tab indicator, desktop notifications, and sound are opt-in. Saved
        per-browser.
      </p>

      <Switch
        label="Title bar count"
        checked={notifications.title}
        onChange={(value) => commitPlain("title", value)}
        description="Show the count of awaiting sessions in the browser tab title."
      />

      <Switch
        label="Browser tab indicator"
        checked={notifications.favicon}
        onChange={(value) => commitPlain("favicon", value)}
        description="Tint the favicon with the highest-attention session state."
      />

      <Switch
        label="Desktop notifications"
        checked={notifications.os}
        pending={requestState === "pending"}
        onChange={(value) => void handleOsChange(value)}
        description="Native notification when a thread needs you or errors."
      />

      {availability && (
        <InlineNotice data-testid="notification-availability" tone={requestState === "failed" ? "danger" : "neutral"}>
          {availability}
        </InlineNotice>
      )}

      <Switch
        label="Sound"
        checked={notifications.sound}
        onChange={(value) => commitPlain("sound", value)}
        description="Short tone on the same transitions."
      />

      {(notifications.os || notifications.sound) && (
        <RadioGroup
          label="Loud for"
          value={loudScope}
          options={LOUD_SCOPE_OPTIONS}
          onChange={(value) => {
            prefsStore.getState().setNotificationsLoudScope(value as "asks" | "all");
            push("success", "Settings saved");
          }}
          description="Desktop notifications and sound use this scope; the title and tab indicator always reflect everything needing you."
        />
      )}
    </div>
  );
}
