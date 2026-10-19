import { DisplaySection } from "./display";
import styles from "./inputShortcuts.module.css";
import { KeybindingsSection } from "./keybindings";

export function InputShortcutsSection() {
  return (
    <div className={styles.root}>
      <DisplaySection />
      <KeybindingsSection />
    </div>
  );
}
