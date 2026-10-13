import { Button } from "../../widgets/button";
import { CloseIcon } from "../../widgets/dialog/CloseIcon";
import { IconButton } from "../../widgets/iconbutton";
import { InlineNotice } from "../../widgets/inlinenotice";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./inlinenotice.module.css";

export default function InlineNoticeGallerySection() {
  return (
    <section>
      <h2>InlineNotice</h2>
      <ThemeFlip>
        <div className={styles.stack}>
          <InlineNotice live="off">neutral: Enter a working directory.</InlineNotice>
          <InlineNotice tone="danger" live="off">
            danger: Failed to load: connection refused.
          </InlineNotice>
          <InlineNotice
            live="off"
            action={
              <Button variant="quiet" size="sm">
                Retry
              </Button>
            }
          >
            neutral + action: Could not check provider configuration.
          </InlineNotice>
          <InlineNotice
            tone="danger"
            live="off"
            action={
              <Button variant="quiet" size="sm">
                Retry
              </Button>
            }
          >
            danger + action: Couldn't save the transcript setting.
          </InlineNotice>
          <InlineNotice
            live="off"
            action={<IconButton label="Dismiss notice" icon={<CloseIcon />} variant="quiet" size="sm" />}
          >
            neutral + dismiss: Discarded last-used model — no longer offered by this hub.
          </InlineNotice>
          <InlineNotice live="off" title="Warnings">
            <ul className={styles.list}>
              <li>title + list: Shift+K is bound twice.</li>
              <li>Mod+J has no action.</li>
            </ul>
          </InlineNotice>
          <InlineNotice
            tone="danger"
            live="off"
            title="Plugin errors"
            action={
              <Button variant="quiet" size="sm">
                Retry
              </Button>
            }
          >
            danger + title + action: superpowers could not be inspected.
          </InlineNotice>
        </div>
      </ThemeFlip>
    </section>
  );
}
