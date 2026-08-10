# Unbounded Eval Rounds and Explorer Inheritance

## Goal

Remove two artificial harness constraints that have already caused Terminal-Bench 2.1 failures:

- Harbor must not stop Serf after 100 tool rounds. Harbor's task timeout remains the outer runtime authority.
- The bundled `explorer` agent must not force `openai/gpt-5.4-mini`. It should inherit the parent model unless the delegate call explicitly selects another model.

## Design

The Harbor adapter will pass `--max-rounds 0`, using Serf's existing zero-means-unlimited contract. This keeps the setting explicit in run evidence without adding a second timeout mechanism.

The bundled explorer definition will omit its `model` field. The loader normalizes an absent model to its existing `inherit` sentinel, and subagent model selection honors an explicit delegate model. No selection code or compatibility path is needed.

## Validation

- Exercise explorer selection through the real built-in agent loader and subagent model selector for both inherited and explicit models.
- Parse the Harbor command argv and assert that the adapter selects the public unlimited-round value.
- Run focused tests, then the repositories' full applicable gates.
- Run only the five failures with evidence that these harness changes or current infrastructure could change the outcome: `train-fasttext`, `extract-moves-from-video`, `build-pov-ray`, `install-windows-3.11`, and `dna-insert`.

The eval run will use Luna at maximum reasoning, one attempt per task, and will not submit results.
