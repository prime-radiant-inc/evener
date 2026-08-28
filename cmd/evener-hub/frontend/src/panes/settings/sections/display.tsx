import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { Switch, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./display.module.css";

const CLASS = {
  root: requireClass(styles.root, "display.module.css", "root"),
  intro: requireClass(styles.intro, "display.module.css", "intro"),
  row: requireClass(styles.row, "display.module.css", "row"),
  help: requireClass(styles.help, "display.module.css", "help"),
};

/**
 * Settings -> Display: the composer preference remains here. Transcript
 * detail and estimated cost are hub defaults under Transcript display, while
 * this Enter-sends setting stays browser-local for compatibility.
 */
export function DisplaySection() {
  const enterToSend = usePrefsStore((s) => s.enterToSend);
  const { push } = useToasts();

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>Composer preferences. Saved per-browser.</p>

      <div className={CLASS.row}>
        <Switch
          label="Enter sends"
          checked={enterToSend}
          onChange={(value) => {
            prefsStore.getState().setEnterToSend(value);
            push("success", "Settings saved");
          }}
        />
        <p className={CLASS.help}>
          Default off: ⌘/Ctrl-Enter sends, Enter inserts a newline. On: Enter sends, Shift-Enter inserts a newline (the
          Shift-Enter shortcut for steering — sending the agent a mid-run correction — is unavailable in this mode; the
          steer button still works).
        </p>
      </div>
    </div>
  );
}
