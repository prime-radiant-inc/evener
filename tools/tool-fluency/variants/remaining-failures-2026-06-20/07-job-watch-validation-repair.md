# Tool Fluency Experiment: validation repair

If a strict tool call reports unknown top-level fields, rebuild the call from
the tool's schema. Use `source`, not `target`, and omit delivery fields; watch
delivery is implicit to the watcher that created it.
