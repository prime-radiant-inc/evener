Correct the transcript-default decoder comment to describe its actual contract: validate revision and config while ignoring unknown wrapper fields. Remove the reference to a nonexistent PATCH decoder and the incorrect implication that strict PATCH responses accept the same extra fields.

Comment-only; runtime behavior is unchanged. RoboRev2681 passed and the post-parent-merge range-diff preserves the reviewed patch.

Fixes #1984
