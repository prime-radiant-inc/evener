package evener_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// loadAwareHelper is the shared sizing library every gate stream sources.
const loadAwareHelper = "scripts/lib/load-aware-workers.sh"

// runLoadAwareHelper sources the helper and invokes call with args in one
// POSIX shell, returning trimmed stdout. The caller supplies core count and
// load average explicitly wherever the helper accepts them, so an assertion
// never depends on the load of the machine running the test.
func runLoadAwareHelper(t *testing.T, call string, args ...string) string {
	t.Helper()
	if _, err := os.Stat(loadAwareHelper); err != nil {
		t.Fatalf("stat %s: %v", loadAwareHelper, err)
	}
	script := `. "$1" && shift && ` + call
	shellArgs := append([]string{"-c", script, "--", loadAwareHelper}, args...)
	out, err := exec.Command("sh", shellArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("sh %s: %v\noutput:\n%s", strings.Join(shellArgs, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestLoadAwareWorkersSizesToSpareCapacity pins the sizing function. The
// ceiling is what a caller asks for on an idle machine (four for the frontend
// vitest pool); as the 1-minute load average rises the result falls toward one
// so a machine shared by concurrent gate runs is not sized as if each run were
// alone. Load is rounded UP before it is subtracted, so a partly busy core
// already costs a worker.
func TestLoadAwareWorkersSizesToSpareCapacity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cap   string
		cores string
		load  string
		want  string
	}{
		{"idle machine keeps the ceiling", "4", "16", "0", "4"},
		{"fractional idle load keeps the ceiling", "4", "16", "0.4", "4"},
		{"busy but not saturated keeps the ceiling", "4", "16", "12.0", "4"},
		{"one core past the ceiling backs off one", "4", "16", "12.1", "3"},
		{"heavily loaded backs off to one", "4", "16", "43.27", "1"},
		{"oversubscribed never drops below one", "4", "16", "99", "1"},
		{"auto ceiling is the core count", "0", "16", "0", "16"},
		{"auto ceiling still backs off under load", "0", "16", "6", "10"},
		{"ceiling above the core count is clamped", "64", "4", "0", "4"},
		{"agent ceiling holds under moderate load", "6", "16", "10", "6"},
		{"agent ceiling backs off past it", "6", "16", "10.1", "5"},
		{"unreadable load keeps the caller ceiling", "4", "16", "not-a-number", "4"},
		{"a lone dot is not a load average", "4", "16", ".", "4"},
		{"unreadable core count keeps the caller ceiling", "4", "garbage", "0", "4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runLoadAwareHelper(t, `load_aware_workers "$@"`, tc.cap, tc.cores, tc.load)
			if got != tc.want {
				t.Errorf("load_aware_workers %s %s %s = %q, want %q", tc.cap, tc.cores, tc.load, got, tc.want)
			}
		})
	}
}

// TestLoadAwareLoad1HonorsAnOverride pins LOAD_AWARE_LOAD1, which stands in for
// the measured load average. A dedicated CI runner sets it to 0: its own
// checkout and cache restore are still in the one-minute average when a gate
// starts, and sizing against that load halved every budget. A malformed value
// is not a load average, so it reads as unknown, like an unreadable one.
func TestLoadAwareLoad1HonorsAnOverride(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ override, want string }{
		{"0", "0"},
		{"2.5", "2.5"},
		{"busy", ""},
		{".", ""},
	} {
		t.Run(tc.override, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("sh", "-c", `. "$1" && load_aware_load1`, "--", loadAwareHelper)
			cmd.Env = envOverride(os.Environ(), "LOAD_AWARE_LOAD1="+tc.override)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("load_aware_load1: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("load_aware_load1 with LOAD_AWARE_LOAD1=%s = %q, want %q", tc.override, got, tc.want)
			}
		})
	}
}

// TestLoadAwareWorkersSizesToTheMachineWhenLoadIsOverriddenToZero pins the
// end-to-end effect: with the measured load replaced by 0, the budget is the
// ceiling clamped to the core count, whatever the machine's real load.
func TestLoadAwareWorkersSizesToTheMachineWhenLoadIsOverriddenToZero(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("sh", "-c", `. "$1" && load_aware_workers 64 3`, "--", loadAwareHelper)
	cmd.Env = envOverride(os.Environ(), "LOAD_AWARE_LOAD1=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("load_aware_workers: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "3" {
		t.Errorf("load_aware_workers 64 3 with LOAD_AWARE_LOAD1=0 = %q, want 3", got)
	}
}

// TestLoadAwareWorkersRejectsMalformedCeiling keeps a bad argument loud: a
// caller that passes a non-numeric ceiling has a bug, and silently printing a
// worker count would hide it behind a test run that just looks slow.
func TestLoadAwareWorkersRejectsMalformedCeiling(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("sh", "-c", `. "$1" && shift && load_aware_workers "$@"`, "--",
		loadAwareHelper, "many", "16", "0").CombinedOutput()
	if err == nil {
		t.Fatalf("load_aware_workers with a malformed ceiling exited zero; output = %q", out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("malformed ceiling printed %q; a rejected argument must produce no worker count", out)
	}
}

// TestLoadAwareCoresReportsThisMachine checks the detector answers with a
// positive integer here, which is the branch the default (no explicit core
// count) takes on every real gate run.
func TestLoadAwareCoresReportsThisMachine(t *testing.T) {
	t.Parallel()
	got := runLoadAwareHelper(t, `load_aware_cores`)
	// A host with no detector legitimately answers nothing; assert only the
	// shape, so the result never depends on this machine's cgroup setup.
	if got == "" {
		return
	}
	n, err := strconv.Atoi(got)
	if err != nil || n < 1 {
		t.Fatalf("load_aware_cores printed %q, want a positive integer", got)
	}
}

// writeFixtureFile writes a fixture and creates its parents, so a fake cgroup
// hierarchy can be built a file at a time.
func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestLoadAwareQuotaCoresClampsToQuota pins the quota parse. A container
// whose cgroup allows fewer CPUs than the host advertises must size to the
// quota: overstating the core count leaves cores - load above the ceiling at
// any load, which silently disables the back-off this library exists for.
func TestLoadAwareQuotaCoresClampsToQuota(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		quota, period string
		want          string
	}{
		{"two whole CPUs", "200000", "100000", "2"},
		{"one whole CPU", "100000", "100000", "1"},
		{"a partial CPU rounds up", "150000", "100000", "2"},
		{"less than one CPU is still one", "50000", "100000", "1"},
		{"unlimited v2 spelling", "max", "100000", ""},
		{"unlimited v1 spelling", "-1", "100000", ""},
		{"zero period is unreadable", "200000", "0", ""},
		{"missing quota is unreadable", "", "100000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runLoadAwareHelper(t, `load_aware_quota_cores "$@"`, tc.quota, tc.period)
			if got != tc.want {
				t.Errorf("load_aware_quota_cores %q %q = %q, want %q", tc.quota, tc.period, got, tc.want)
			}
		})
	}
}

// TestLoadAwareCgroupHierarchyTakesMostRestrictiveQuota is the nested-cgroup
// case: a systemd CPUQuota slice leaves the hierarchy root and the leaf
// unlimited while the limit sits in between. Reading only the root, as the
// first version of this library did, finds nothing and reports the host's
// full core count, so the back-off never engages.
func TestLoadAwareCgroupHierarchyTakesMostRestrictiveQuota(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		root  string
		slice string
		leaf  string
		want  string
	}{
		{"limit above the leaf", "max 100000", "200000 100000", "max 100000", "2"},
		{"limit at the leaf wins", "max 100000", "400000 100000", "100000 100000", "1"},
		{"no finite limit anywhere", "max 100000", "max 100000", "max 100000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mount := filepath.Join(root, "cgroup")
			writeFixtureFile(t, filepath.Join(mount, "cpu.max"), tc.root+"\n")
			writeFixtureFile(t, filepath.Join(mount, "slice", "cpu.max"), tc.slice+"\n")
			writeFixtureFile(t, filepath.Join(mount, "slice", "leaf", "cpu.max"), tc.leaf+"\n")
			membership := filepath.Join(root, "self-cgroup")
			writeFixtureFile(t, membership, "0::/slice/leaf\n")
			mountinfo := filepath.Join(root, "mountinfo")
			writeFixtureFile(t, mountinfo, fmt.Sprintf("29 23 0:26 / %s rw - cgroup2 cgroup2 rw\n", mount))

			got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
			if got != tc.want {
				t.Errorf("load_aware_cgroup_cores_from = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoadAwareCgroupV1Quota covers the split-file hierarchy, whose quota and
// period live in two files rather than one "quota period" line.
func TestLoadAwareCgroupV1Quota(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mount := filepath.Join(root, "cgroup")
	leaf := filepath.Join(mount, "v1leaf")
	writeFixtureFile(t, filepath.Join(leaf, "cpu.cfs_quota_us"), "150000\n")
	writeFixtureFile(t, filepath.Join(leaf, "cpu.cfs_period_us"), "100000\n")
	membership := filepath.Join(root, "self-cgroup")
	writeFixtureFile(t, membership, "5:cpu,cpuacct:/v1leaf\n")
	mountinfo := filepath.Join(root, "mountinfo")
	writeFixtureFile(t, mountinfo, fmt.Sprintf("31 23 0:27 / %s rw - cgroup cgroup rw,cpu,cpuacct\n", mount))

	got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
	if got != "2" {
		t.Errorf("load_aware_cgroup_cores_from = %q, want %q", got, "2")
	}
}

// TestLoadAwareCgroupHybridPrefersTheFiniteQuota is the hybrid case: the v2
// unified hierarchy is mounted, so a v2 membership and mount both exist, but
// the cpu controller lives in v1 and only v1 states a finite quota. Committing
// to v2 on sight finds "max" there and reports the host's full core count.
func TestLoadAwareCgroupHybridPrefersTheFiniteQuota(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	v2Mount := filepath.Join(root, "cgroup2")
	writeFixtureFile(t, filepath.Join(v2Mount, "slice", "cpu.max"), "max 100000\n")
	v1Mount := filepath.Join(root, "cpu")
	v1Leaf := filepath.Join(v1Mount, "v1leaf")
	writeFixtureFile(t, filepath.Join(v1Leaf, "cpu.cfs_quota_us"), "100000\n")
	writeFixtureFile(t, filepath.Join(v1Leaf, "cpu.cfs_period_us"), "100000\n")
	membership := filepath.Join(root, "self-cgroup")
	writeFixtureFile(t, membership, "0::/slice\n5:cpu,cpuacct:/v1leaf\n")
	mountinfo := filepath.Join(root, "mountinfo")
	writeFixtureFile(t, mountinfo, fmt.Sprintf(
		"29 23 0:26 / %s rw - cgroup2 cgroup2 rw\n31 23 0:27 / %s rw - cgroup cgroup rw,cpu,cpuacct\n",
		v2Mount, v1Mount))

	got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
	if got != "1" {
		t.Errorf("load_aware_cgroup_cores_from = %q, want %q (the v1 quota)", got, "1")
	}
}

// TestLoadAwareCgroupNonRootMountBoundary pins the walk boundary when
// mountinfo's root field is not "/". The membership path is relative to the
// root the mount exposes at the mount point, so the walk must stop at the
// mount point itself; treating root as a directory beneath it skips the
// delegated root's own limit.
func TestLoadAwareCgroupNonRootMountBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mount := filepath.Join(root, "cgroup")
	writeFixtureFile(t, filepath.Join(mount, "cpu.max"), "200000 100000\n")
	writeFixtureFile(t, filepath.Join(mount, "child", "cpu.max"), "max 100000\n")
	membership := filepath.Join(root, "self-cgroup")
	writeFixtureFile(t, membership, "0::/child\n")
	mountinfo := filepath.Join(root, "mountinfo")
	writeFixtureFile(t, mountinfo, fmt.Sprintf("29 23 0:26 /delegated %s rw - cgroup2 cgroup2 rw\n", mount))

	got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
	if got != "2" {
		t.Errorf("load_aware_cgroup_cores_from = %q, want %q", got, "2")
	}
}

// mountinfoEscaped renders a path the way /proc/self/mountinfo does: a space,
// tab, newline, or backslash becomes its octal escape, so a path containing any
// of them arrives in one whitespace-free field.
func mountinfoEscaped(path string) string {
	return strings.NewReplacer(
		`\`, `\134`,
		" ", `\040`,
		"\t", `\011`,
		"\n", `\012`,
	).Replace(path)
}

// TestLoadAwareCgroupAbsoluteMembership covers a delegated mount whose
// membership path is hierarchy-absolute rather than namespace-relative. Without
// a cgroup namespace the path in /proc/self/cgroup starts at the hierarchy root,
// but the mount exposes a subtree at its mount point, so joining the whole path
// against the mount point names a directory that does not exist and the quota
// walk climbs past the limit. The two readings are indistinguishable from the
// files alone, so the fix must consider both and keep the most restrictive
// finite result.
func TestLoadAwareCgroupAbsoluteMembership(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		root   string
		member string
		// files maps a path under the mount ("" is the mount point itself) to
		// its cpu.max contents.
		files map[string]string
		want  string
	}{
		{
			name:   "a finite limit only the absolute reading reaches",
			root:   "/delegated",
			member: "/delegated/child",
			files: map[string]string{
				"":      "max 100000\n",
				"child": "100000 100000\n",
			},
			want: "1",
		},
		{
			name:   "the most restrictive of both readings wins",
			root:   "/delegated",
			member: "/delegated/child",
			files: map[string]string{
				"":                "max 100000\n",
				"child":           "100000 100000\n",
				"delegated/child": "400000 100000\n",
			},
			want: "1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mount := filepath.Join(root, "cgroup")
			for rel, content := range tc.files {
				writeFixtureFile(t, filepath.Join(mount, rel, "cpu.max"), content)
			}
			membership := filepath.Join(root, "self-cgroup")
			writeFixtureFile(t, membership, "0::"+tc.member+"\n")
			mountinfo := filepath.Join(root, "mountinfo")
			writeFixtureFile(t, mountinfo, fmt.Sprintf("29 23 0:26 %s %s rw - cgroup2 cgroup2 rw\n", tc.root, mount))

			got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
			if got != tc.want {
				t.Errorf("load_aware_cgroup_cores_from = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoadAwareCgroupEscapedMountPath covers a mount point that mountinfo could
// not write literally. Field 5 arrives with its spaces, tabs, and backslashes
// escaped as octal, so the mount point must be decoded before it can name a
// directory; a fixture that never exercises the escape cannot catch the miss.
func TestLoadAwareCgroupEscapedMountPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		dir    string
		root   string
		member string
		files  map[string]string
		want   string
	}{
		{
			name:   "space in the mount point",
			dir:    "cg space",
			root:   "/",
			member: "/slice",
			files:  map[string]string{"": "200000 100000\n"},
			want:   "2",
		},
		{
			name:   "backslash in the mount point",
			dir:    `cg\back`,
			root:   "/",
			member: "/slice",
			files:  map[string]string{"": "200000 100000\n"},
			want:   "2",
		},
		{
			name:   "tab in the mount point",
			dir:    "cg\tspace",
			root:   "/",
			member: "/slice",
			files:  map[string]string{"": "200000 100000\n"},
			want:   "2",
		},
		{
			name:   "escaped mount point with an absolute membership",
			dir:    "cg space",
			root:   "/delegated",
			member: "/delegated/child",
			files: map[string]string{
				"":      "max 100000\n",
				"child": "100000 100000\n",
			},
			want: "1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mount := filepath.Join(root, tc.dir)
			for rel, content := range tc.files {
				writeFixtureFile(t, filepath.Join(mount, rel, "cpu.max"), content)
			}
			membership := filepath.Join(root, "self-cgroup")
			writeFixtureFile(t, membership, "0::"+tc.member+"\n")
			mountinfo := filepath.Join(root, "mountinfo")
			writeFixtureFile(t, mountinfo, fmt.Sprintf("29 23 0:26 %s %s rw - cgroup2 cgroup2 rw\n",
				tc.root, mountinfoEscaped(mount)))

			got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
			if got != tc.want {
				t.Errorf("load_aware_cgroup_cores_from = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoadAwareCgroupMountDecodesEscapes pins the mount point itself: an
// escaped field must come back naming a real directory, not a path that still
// contains the literal "\040" spelling.
func TestLoadAwareCgroupMountDecodesEscapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mount := filepath.Join(root, "cg space")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", mount, err)
	}
	mountinfo := filepath.Join(root, "mountinfo")
	writeFixtureFile(t, mountinfo, fmt.Sprintf("29 23 0:26 / %s rw - cgroup2 cgroup2 rw\n", mountinfoEscaped(mount)))

	got := runLoadAwareHelper(t, `load_aware_cgroup_mount "$@"`, mountinfo)
	if got != mount {
		t.Errorf("load_aware_cgroup_mount = %q, want %q", got, mount)
	}
}

// TestLoadAwareCgroupTrailingNewlinePaths covers a mountinfo path that ends in
// a newline (escaped as \012). Command substitution strips trailing newlines,
// so a decoded path must be carried with a sentinel through every capture or
// the newline is dropped, the directory it names cannot be read, and the walk
// climbs past the quota the mount actually declares.
//
// The membership path cannot express a trailing newline the same way: the
// kernel writes /proc/self/cgroup one path per line, so a newline inside that
// path is not parseable by any line-oriented reader. The mount point and root
// are the fields mountinfo escapes for exactly this reason.
func TestLoadAwareCgroupTrailingNewlinePaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		dir    string
		root   string
		member string
		files  map[string]string
		want   string
	}{
		{
			name:   "newline at the end of the mount point",
			dir:    "cg\n",
			root:   "/",
			member: "/",
			files:  map[string]string{"": "200000 100000\n"},
			want:   "2",
		},
		{
			name:   "newline at the end of the mount point with an absolute membership",
			dir:    "cg\n",
			root:   "/delegated",
			member: "/delegated/child",
			files: map[string]string{
				"":      "max 100000\n",
				"child": "100000 100000\n",
			},
			want: "1",
		},
		{
			name:   "newline mount point survives the walk up to the mount point",
			dir:    "cg\n",
			root:   "/",
			member: "/leaf",
			files: map[string]string{
				"":     "400000 100000\n",
				"leaf": "100000 100000\n",
			},
			want: "1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mount := filepath.Join(root, tc.dir)
			for rel, content := range tc.files {
				writeFixtureFile(t, filepath.Join(mount, rel, "cpu.max"), content)
			}
			membership := filepath.Join(root, "self-cgroup")
			writeFixtureFile(t, membership, "0::"+tc.member+"\n")
			mountinfo := filepath.Join(root, "mountinfo")
			writeFixtureFile(t, mountinfo, fmt.Sprintf("29 23 0:26 %s %s rw - cgroup2 cgroup2 rw\n",
				tc.root, mountinfoEscaped(mount)))

			got := runLoadAwareHelper(t, `load_aware_cgroup_cores_from "$@"`, membership, mountinfo)
			if got != tc.want {
				t.Errorf("load_aware_cgroup_cores_from = %q, want %q", got, tc.want)
			}
		})
	}
}
