## edit_file

Use the edit_file tool to make precise changes to existing files. It replaces an exact
occurrence of old_string with new_string. Rules:

- The old_string must be unique within the file. If it matches multiple locations, the
  edit will fail. Include enough surrounding context to make old_string unique.
- Always read the file first so you know the exact content to match.
- Keep edits small and focused. Make one logical change per edit_file call.
- Set replace_all to true only when renaming a symbol across the entire file.
