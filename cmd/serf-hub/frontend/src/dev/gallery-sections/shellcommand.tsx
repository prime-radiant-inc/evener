import { ShellCommandBlock } from "../../widgets/shellcommand";
import { ThemeFlip } from "../ThemeFlip";

const CHAINED_COMMAND =
  "cd /srv/serf && npm test -- --run src/widgets/shellcommand/shellCommand.test.ts && git status --short";

const AUTHORED_MULTILINE_COMMAND = `printf '%s\\n' "$HOME" \\
  | tee /tmp/serf-paths # preserve the authored continuation`;

export default function ShellCommandGallerySection() {
  return (
    <section>
      <h2>ShellCommandBlock</h2>
      <ThemeFlip>
        <p>Chained commands wrap at shell control operators while preserving the command copied from the block.</p>
        <ShellCommandBlock command={CHAINED_COMMAND} />
      </ThemeFlip>
      <ThemeFlip>
        <p>
          Authored newlines, continuations, quoting, and comments remain source text rather than display normalization.
        </p>
        <ShellCommandBlock command={AUTHORED_MULTILINE_COMMAND} />
      </ThemeFlip>
    </section>
  );
}
