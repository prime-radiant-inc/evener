# make help's listing: one block per make/<family>.mk, one line per target.
#
# This mirrors ParseFamily (internal/maketargetsdoc/parse.go), the grammar both
# this listing and the generated doc tables read. A "## " block attaches to the
# next rule line; "##   " (three spaces) continues whichever field is open, a
# structured key like "## trigger:" opens one, and anything else is summary
# prose. The doc generator still enforces the grammar and fails on a violation,
# so a file this script misreads is a build failure, not a silent difference.
FNR == 1 { fname = FILENAME; sub(/.*\//, "", fname); sub(/\.mk$/, "", fname); sum = ""; lastkey = "" }
/^##   / { c = substr($0, 6); if (lastkey != "") next; if (sum != "") sum = sum " " c; next }
/^## / {
  rest = substr($0, 4)
  if (rest ~ /^[a-z][a-z0-9-]*:/) { k = rest; sub(/:.*/, "", k); lastkey = k; next }
  if (sum == "") sum = rest; else if (lastkey == "") sum = sum " " rest
  lastkey = ""; next
}
/^[ \t]*$/ { next }
/^#/ { next }
/^\./ { next }
/^[A-Za-z0-9_][A-Za-z0-9_.-]*:/ {
  nm = $0; sub(/:.*/, "", nm)
  if (sum == "") next
  t[++n] = nm; s[n] = sum; f[n] = fname; sum = ""; lastkey = ""; next
}
END {
  for (i = 1; i <= n; i++) if (!(f[i] in seen)) { order[++k] = f[i]; seen[f[i]] = 1 }
  for (j = 1; j <= k; j++) {
    fam = order[j]; w = 0
    for (i = 1; i <= n; i++) if (f[i] == fam && length(t[i]) > w) w = length(t[i])
    printf "%s%s:\n", (j > 1 ? "\n" : ""), fam
    for (i = 1; i <= n; i++) if (f[i] == fam) printf "  %-*s  %s\n", w, t[i], s[i]
  }
}
