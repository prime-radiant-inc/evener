import { useState } from "react";
import { Button } from "../../widgets/button";
import { IconButton } from "../../widgets/iconbutton";
import { PromptCard } from "../../widgets/promptcard";
import { Textarea } from "../../widgets/textarea";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./promptcard.module.css";

function ClipIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <path
        d="M10 4.5 5.5 9a2 2 0 0 0 2.8 2.8l4.2-4.2a3.5 3.5 0 0 0-5-5L3 7.2"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
      />
    </svg>
  );
}

function LiveCard({ label, actions }: { label: string; actions: React.ReactNode }) {
  const [value, setValue] = useState("");
  return (
    <div className={styles.case}>
      <p className={styles.caseLabel}>{label}</p>
      <PromptCard
        field={
          <Textarea
            value={value}
            onChange={(e) => setValue(e.target.value)}
            autoGrow
            seamless
            placeholder="Message the agent…"
            aria-label={`Message (${label})`}
          />
        }
        leading={<IconButton label="Attach image" icon={<ClipIcon />} variant="quiet" size="xs" />}
        actions={actions}
      />
    </div>
  );
}

export default function PromptCardGallerySection() {
  return (
    <section>
      <h2>PromptCard</h2>
      <ThemeFlip>
        <LiveCard
          label="one verb"
          actions={
            <Button variant="primary" size="xs">
              Send
            </Button>
          }
        />
        <LiveCard
          label="three verbs, Stop pinned leftmost"
          actions={
            <>
              <Button variant="dangerQuiet" size="xs">
                Stop
              </Button>
              <Button variant="quiet" size="xs">
                Send
              </Button>
              <Button variant="primary" size="xs">
                Steer
              </Button>
            </>
          }
        />
        <div className={styles.case}>
          <p className={styles.caseLabel}>taller floor (spawn's prompt), no control row</p>
          <PromptCard
            field={
              <Textarea
                value=""
                onChange={() => {}}
                autoGrow
                seamless
                minLines={6}
                placeholder="What should the agent work on?"
                aria-label="Prompt (gallery)"
              />
            }
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
