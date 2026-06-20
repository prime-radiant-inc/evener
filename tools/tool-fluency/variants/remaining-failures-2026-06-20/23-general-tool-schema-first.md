# Tool Fluency Experiment: schema first

For strict tools, map the task to the tool schema before calling the tool.
Place values only in the fields documented for that operation and nesting
level. When a tool error names a field, use that error to rebuild the schema
shape.
