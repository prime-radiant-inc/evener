Extract shared checkpointed draft persistence and discard primitives from the keybindings store, preserving storage failure reporting through draftError and conflict restoration behavior. Export and package the helper module, and strip only the internal generation stamp when projecting native keybinding drafts while retaining structured errors.

Qualification at 98cb71188f6bb30656b01e1d055e64d8db063e2c: AppWire package, full web gate, native gate (946 native and 793 shared tests, types/scripts and bundle), Biome, package-import lint, and diff check passed. Exact-base RoboRev2680 found no issues. The restack preserves the intended semantic patch over merged settings generation d1d056a24f616e2d1bda1cc9581b776416332f0d.

This is the shared editor prerequisite for transcript checkpointed drafts; it does not claim the later web adapter adoption is complete.
