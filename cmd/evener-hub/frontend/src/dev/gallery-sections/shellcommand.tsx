import { ShellCommandBlock } from "../../widgets/shellcommand";
import { GallerySection } from "../GallerySection";

const CHAINED_COMMAND =
  "cd /srv/evener && npm test -- --run src/widgets/shellcommand/shellCommand.test.ts && git status --short";

const AUTHORED_MULTILINE_COMMAND = `printf '%s\\n' "$HOME" \\
  | tee /tmp/evener-paths # preserve the authored continuation`;

export default function ShellCommandGallerySection() {
  return (
    <GallerySection
      title="ShellCommandBlock"
      flips={[
        <>
          <p>Chained commands wrap at shell control operators while preserving the command copied from the block.</p>
          <ShellCommandBlock command={CHAINED_COMMAND} />
        </>,
        <>
          <p>
            Authored newlines, continuations, quoting, and comments remain source text rather than display
            normalization.
          </p>
          <ShellCommandBlock command={AUTHORED_MULTILINE_COMMAND} />
        </>,
      ]}
    />
  );
}
