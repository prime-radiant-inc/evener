# Task 132b — PR #1145, the flag path as an array

- `3755982c6` ci: carry the caller's arguments as an array, not a joined string
- Pushed head: `cadaf2840` (with round 29's commits; merge already up to date)

`flag_args=("$@")` where `flags="$*"` was; `module_test_flags` and `module_extra`
emit one flag per line; `run_module`, the two `go test` calls, the shards call and
`package_list_build_flags` all read arrays. No `in $flags` remains.

Reproduction (file named `-C` in the repo root, `-run '*'`):
```
before  test argv: test -short -count=1 -run cov_ev_registry_test.go envvars.go …
after   test argv: test -short -count=1 -run * -run ^(Test|Example) …
```
Gates: bash -n; envvars PASS 0.59s, `.` PASS 77.58s, agent PASS 70.01s, unsharded
race path ran; `make lint-generated` PASS.
