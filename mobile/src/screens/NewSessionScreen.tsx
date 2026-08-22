/**
 * New session screen — honest placeholder for Task 8.
 *
 * A short mobile flow: project path, initial prompt, model/effort, launch.
 * This placeholder shows the intended form structure without fake
 * functionality; Task 8 wires it to the live Hub. The Start button is disabled
 * — it does not start a session.
 */
import type { JSX } from "react";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Empty } from "../ui/States";
import { TopBar } from "../ui/TopBar";

export function NewSessionScreen(): JSX.Element {
  return (
    <div>
      <TopBar title="New Session" />
      <div className="evener-list-group">
        <Input
          label="Project path"
          name="project"
          placeholder="/path/to/project"
        />
        <Input
          label="Initial prompt"
          name="prompt"
          placeholder="What should the agent do?"
        />
      </div>
      <div style={{ padding: "16px" }}>
        <Button variant="primary" disabled>
          Start
        </Button>
      </div>
      <Empty
        title="Session creation arrives in Task 8"
        hint="This form is a placeholder — it does not start a session yet."
      />
    </div>
  );
}
