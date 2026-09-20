Extract the shared wire rejection-payload decoder and use it from the keybindings store. Transcript-display default decoders now ignore unknown wrapper fields while still validating known fields, matching the PATCH wrapper contract.

This small wire-contract slice precedes the shared transcript read and PATCH stores. Validation: 164 focused tests, package build and external-import qualification, frontend typecheck, Biome, and package-import checks passed. Local RoboRev2679 found no issues. The restack preserves every owned added/removed line from the previously reviewed slice.

Low follow-up #1984 remains separate.
