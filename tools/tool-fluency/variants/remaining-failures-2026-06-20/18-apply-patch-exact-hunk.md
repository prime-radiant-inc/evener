# Tool Fluency Experiment: exact patch hunk

For `apply_patch`, write update hunks using exact existing text from the file.
Use one small replacement hunk when a single line changes.

Example:

```diff
*** Begin Patch
*** Update File: path.txt
@@
-old exact line
+new exact line
*** End Patch
```
