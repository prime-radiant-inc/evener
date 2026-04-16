---
name: default
description: "General-purpose agent. Handles a wide range of tasks directly with the broadest non-delegation toolset."
model: inherit
color: blue
tools: all
tasks:
  - title: Understand task
    prompt: "Understand the task, the deliverable, and the acceptance criteria."
  - title: Do the work
    insert: parent_tasks
    prompt: "Complete the task using the available tools."
  - title: Verify
    prompt: "Verify the result with the level of evidence the task requires."
---
