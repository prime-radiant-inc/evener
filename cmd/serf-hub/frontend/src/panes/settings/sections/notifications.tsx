import { type NotificationKey, prefsStore, usePrefsStore } from "../../../stores/prefs";
import { RadioGroup, type RadioGroupOption, Switch, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./notifications.module.css";

const CLASS = {
  root: requireClass(styles.root, "notifications.module.css", "root"),
  intro: requireClass(styles.intro, "notifications.module.css", "intro"),
  row: requireClass(styles.row, "notifications.module.css", "row"),
  help: requireClass(styles.help, "notifications.module.css", "help"),
};

const LOUD_SCOPE_OPTIONS: RadioGroupOption[] = [
  { value: "asks", label: "Questions & errors" },
  { value: "all", label: "Everything needing me" },
];

/**
 * Settings -> Notifications (parity-m7-settings.md §6): 4 toggles + a
 * "Loud for" radio, localStorage-only, plus the one browser-API-touching
 * flow in this section: OS notification's Notification.requestPermission()
 * gate. All 4 toggles default OFF - the pre-adjudicated code-wins
 * resolution of the floor doc's own copy/code discrepancy (legacy copy
 * claimed title/favicon default on; both the static markup and the JS
 * default landed all four at OFF, per assets/settings-notifications.js's
 * own `!!prefs[key]` against an empty object). This section's intro
 * paragraph is corrected to match that same resolution rather than copied
 * verbatim - shipping copy that contradicts the toggles rendered directly
 * below it would be worse than either "faithful" option alone.
 *
 * OS notification's permission gate has no timer/polling to clean up
 * (unlike T2's OAuth device-code flow) - one `requestPermission()` promise
 * per click, so a plain async handler suffices; there is nothing to abort
 * on unmount (the store setter + toast below are both fire-and-forget
 * module-level calls, not local component state a stale closure could
 * clobber).
 */
export function NotificationsSection() {
  const notifications = usePrefsStore((s) => s.notifications);
  const loudScope = usePrefsStore((s) => s.notificationsLoudScope);
  const { push } = useToasts();

  function commitPlain(key: Exclude<NotificationKey, "os">, value: boolean) {
    prefsStore.getState().setNotification(key, value);
    push("success", "Settings saved");
  }

  async function handleOsChange(value: boolean) {
    // Turning off, or toggling while permission is already resolved
    // (granted/denied, not "default"): commit unconditionally - turning the
    // setting off never revokes/rechecks the browser permission, and
    // turning it back on after a resolved decision never re-prompts.
    const shouldRequestPermission = value && "Notification" in window && Notification.permission === "default";
    if (!shouldRequestPermission) {
      prefsStore.getState().setNotification("os", value);
      push("success", "Settings saved");
      return;
    }

    let permission: NotificationPermission;
    try {
      permission = await Notification.requestPermission();
    } catch {
      // requestPermission() itself threw/rejected: revert silently - no
      // toast at all, matching the legacy's own empty-reason suppression.
      prefsStore.getState().setNotification("os", false);
      return;
    }
    if (permission === "granted") {
      prefsStore.getState().setNotification("os", true);
      push("success", "Settings saved");
    } else {
      prefsStore.getState().setNotification("os", false);
      push("warning", "Browser denied notification permission");
    }
  }

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>Title, favicon, OS notification, and sound are all opt-in. Saved per-browser.</p>

      <div className={CLASS.row}>
        <Switch
          label="Title bar count"
          checked={notifications.title}
          onChange={(value) => commitPlain("title", value)}
        />
        <p className={CLASS.help}>Show the count of awaiting sessions in the browser tab title.</p>
      </div>

      <div className={CLASS.row}>
        <Switch
          label="Favicon dot"
          checked={notifications.favicon}
          onChange={(value) => commitPlain("favicon", value)}
        />
        <p className={CLASS.help}>Tint the favicon with the highest-attention session state.</p>
      </div>

      <div className={CLASS.row}>
        <Switch label="OS notification" checked={notifications.os} onChange={(value) => void handleOsChange(value)} />
        <p className={CLASS.help}>Native notification when a thread needs you or errors.</p>
      </div>

      <div className={CLASS.row}>
        <Switch label="Sound" checked={notifications.sound} onChange={(value) => commitPlain("sound", value)} />
        <p className={CLASS.help}>Short tone on the same transitions.</p>
      </div>

      <div className={CLASS.row}>
        <RadioGroup
          label="Loud for"
          value={loudScope}
          options={LOUD_SCOPE_OPTIONS}
          onChange={(value) => {
            prefsStore.getState().setNotificationsLoudScope(value as "asks" | "all");
            push("success", "Settings saved");
          }}
        />
        <p className={CLASS.help}>
          OS notification and sound fire for this scope only; the title/favicon count always reflects everything needing
          you.
        </p>
      </div>
    </div>
  );
}
