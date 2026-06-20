# Tool Fluency Experiment: validation repair

If a strict tool call reports unknown top-level fields, rebuild the call from
the tool's schema. Move delivery fields into the documented nested object and
keep operation fields at the top level.
