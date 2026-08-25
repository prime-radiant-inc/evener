package agent

import (
	"encoding/json"
	"io/fs"
	"maps"
	"math"
	"runtime"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
)

// resourceFixtureEnv keeps the environment snapshot on the real production
// path while supplying process-visible host files from a fixture. The
// optional ReadHostFile seam is intentionally narrower than ExecutionEnvironment:
// production LocalExecutionEnvironment falls back to os.ReadFile, while this
// fixture can model cgroup layouts without modifying the host or running a
// privileged container.
type resourceFixtureEnv struct {
	*execenv.LocalExecutionEnvironment
	files map[string]string
}

func (e *resourceFixtureEnv) Platform() string  { return "linux" }
func (e *resourceFixtureEnv) OSVersion() string { return "Linux fixture" }

func (e *resourceFixtureEnv) ReadHostFile(path string) ([]byte, error) {
	content, ok := e.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(content), nil
}

func newResourceFixtureEnv(t *testing.T, files map[string]string) *resourceFixtureEnv {
	t.Helper()
	root := t.TempDir()
	return &resourceFixtureEnv{
		LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(root),
		files:                     files,
	}
}

func fixtureHostMemInfo(kb int64) string {
	return "MemTotal:       " + formatInt(kb) + " kB\nMemAvailable:   " + formatInt(kb/2) + " kB\n"
}

func formatInt(n int64) string {
	// Kept in a helper so fixture text is generated from numbers, not copied
	// from the implementation's parser.
	return strconv.FormatInt(n, 10)
}

func resourceFixtureV2(cpuMax, memoryMax string) map[string]string {
	return map[string]string{
		"/proc/self/cgroup":                        "0::/docker/fixture\n",
		"/proc/self/mountinfo":                     "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/docker/fixture/cpu.max":    cpuMax + "\n",
		"/sys/fs/cgroup/docker/fixture/memory.max": memoryMax + "\n",
		"/sys/fs/cgroup/docker/cpu.max":            "max 100000\n",
		"/sys/fs/cgroup/docker/memory.max":         "max\n",
		"/proc/meminfo":                            fixtureHostMemInfo(65536000),
	}
}

func resourceFixtureV1() map[string]string {
	return map[string]string{
		"/proc/self/cgroup": "11:memory:/docker/fixture\n10:cpu,cpuacct:/docker/fixture\n",
		"/proc/self/mountinfo": "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - tmpfs tmpfs rw\n" +
			"30 29 0:27 / /sys/fs/cgroup/cpu,cpuacct rw,relatime - cgroup cgroup rw,cpu,cpuacct\n" +
			"31 29 0:28 / /sys/fs/cgroup/memory rw,relatime - cgroup cgroup rw,memory\n",
		"/sys/fs/cgroup/cpu,cpuacct/docker/fixture/cpu.cfs_quota_us":  "150000\n",
		"/sys/fs/cgroup/cpu,cpuacct/docker/fixture/cpu.cfs_period_us": "100000\n",
		"/sys/fs/cgroup/memory/docker/fixture/memory.limit_in_bytes":  "2147483648\n",
		"/proc/meminfo": fixtureHostMemInfo(65536000),
	}
}

func resourceFixtureNoCgroup() map[string]string {
	return map[string]string{
		"/proc/self/cgroup": "0::/\n",
		"/proc/meminfo":     fixtureHostMemInfo(16384),
	}
}

func resourceCapsJSON(t *testing.T, env *resourceFixtureEnv) map[string]any {
	t.Helper()
	info := envInfoFromEnv(env, clock.Real())
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal EnvironmentInfo: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal EnvironmentInfo: %v", err)
	}
	return wire
}

func assertResourceCaps(t *testing.T, wire map[string]any, wantCPU, wantMemory float64) {
	t.Helper()
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources = %#v, want structured resource object", wire["resources"])
	}
	gotCPU, cpuOK := resources["cpus"].(float64)
	gotMemory, memoryOK := resources["memory_mb"].(float64)
	if !cpuOK || math.Abs(gotCPU-wantCPU) > 1e-9 {
		t.Errorf("resources.cpus = %#v, want %v", resources["cpus"], wantCPU)
	}
	if !memoryOK || gotMemory != wantMemory {
		t.Errorf("resources.memory_mb = %#v, want %v", resources["memory_mb"], wantMemory)
	}
}

func assertResourceCapsUnknown(t *testing.T, wire map[string]any) {
	t.Helper()
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources = %#v, want structured resource object", wire["resources"])
	}
	if _, ok := resources["cpus"]; ok {
		t.Errorf("resources.cpus = %#v, want unknown/omitted", resources["cpus"])
	}
	if _, ok := resources["memory_mb"]; ok {
		t.Errorf("resources.memory_mb = %#v, want unknown/omitted", resources["memory_mb"])
	}
}

func TestEnvironmentInfoReportsEffectiveCgroupV2Resources(t *testing.T) {
	t.Parallel()
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, resourceFixtureV2("100000 100000", "2147483648")))
	assertResourceCaps(t, wire, 1, 2048)
}

func TestEnvironmentInfoReportsEffectiveCgroupV1Resources(t *testing.T) {
	t.Parallel()
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, resourceFixtureV1()))
	assertResourceCaps(t, wire, 1.5, 2048)
}

func TestEnvironmentInfoUsesHostFallbackWhenCgroupIsExplicitlyUnlimited(t *testing.T) {
	t.Parallel()
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, resourceFixtureV2("max 100000", "max")))
	assertResourceCaps(t, wire, float64(runtime.NumCPU()), 64000)
}

func TestEnvironmentInfoUsesHostFallbackForUnlimitedCgroupV1Resources(t *testing.T) {
	t.Parallel()
	files := resourceFixtureV1()
	files["/sys/fs/cgroup/cpu,cpuacct/docker/fixture/cpu.cfs_quota_us"] = "-1\n"
	files["/sys/fs/cgroup/memory/docker/fixture/memory.limit_in_bytes"] = "9223372036854771712\n"
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
	assertResourceCaps(t, wire, float64(runtime.NumCPU()), 64000)
}

func TestEnvironmentInfoUsesHostFallbackWhenNoCgroupIsVisible(t *testing.T) {
	t.Parallel()
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, resourceFixtureNoCgroup()))
	assertResourceCaps(t, wire, float64(runtime.NumCPU()), 16)
}

func TestEnvironmentInfoDoesNotUseHostFactsWhenCgroupMetadataIsUnreadable(t *testing.T) {
	t.Parallel()
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, map[string]string{
		"/proc/meminfo": fixtureHostMemInfo(65536000),
	}))
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources = %#v, want structured resource object", wire["resources"])
	}
	if _, ok := resources["cpus"]; ok {
		t.Errorf("host CPU value was reported without cgroup metadata: %#v", resources["cpus"])
	}
	if _, ok := resources["memory_mb"]; ok {
		t.Errorf("host memory value was reported without cgroup metadata: %#v", resources["memory_mb"])
	}
}

func TestEnvironmentInfoUsesCgroupV2CPUSetWhenQuotaIsUnlimited(t *testing.T) {
	t.Parallel()
	files := resourceFixtureV2("max 100000", "max")
	files["/sys/fs/cgroup/docker/fixture/cpuset.cpus.effective"] = "0-1,4\n"
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
	assertResourceCaps(t, wire, 3, 64000)
}

func TestEnvironmentInfoUsesLegacyCgroupV2CPUSet(t *testing.T) {
	t.Parallel()
	files := resourceFixtureV2("max 100000", "max")
	files["/sys/fs/cgroup/docker/fixture/cpuset.cpus"] = "2-3\n"
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
	assertResourceCaps(t, wire, 2, 64000)
}

func TestEnvironmentInfoDoesNotTreatMalformedCgroupAsHostResources(t *testing.T) {
	t.Parallel()
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, resourceFixtureV2("not-a-quota", "not-a-limit")))
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources = %#v, want structured resource object", wire["resources"])
	}
	if _, ok := resources["cpus"]; ok {
		t.Errorf("malformed cgroup CPU limit was reported as host value: %#v", resources["cpus"])
	}
	if _, ok := resources["memory_mb"]; ok {
		t.Errorf("malformed cgroup memory limit was reported as host value: %#v", resources["memory_mb"])
	}
}

func TestEnvironmentInfoDoesNotTreatUnreadableCgroupAsHostResources(t *testing.T) {
	t.Parallel()
	files := resourceFixtureV2("100000 100000", "2147483648")
	delete(files, "/sys/fs/cgroup/docker/fixture/cpu.max")
	delete(files, "/sys/fs/cgroup/docker/fixture/memory.max")
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources = %#v, want structured resource object", wire["resources"])
	}
	if _, ok := resources["cpus"]; ok {
		t.Errorf("unreadable cgroup CPU limit was reported as host value: %#v", resources["cpus"])
	}
	if _, ok := resources["memory_mb"]; ok {
		t.Errorf("unreadable cgroup memory limit was reported as host value: %#v", resources["memory_mb"])
	}
}

func TestEnvironmentInfoCombinesCgroupV2AncestorLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		leafCPU    string
		parentCPU  string
		leafMemory string
		parentMem  string
		wantCPU    float64
		wantMemory float64
	}{
		{
			name:       "finite parent limits unlimited leaf",
			leafCPU:    "max 100000",
			parentCPU:  "100000 100000",
			leafMemory: "max",
			parentMem:  "2147483648",
			wantCPU:    1,
			wantMemory: 2048,
		},
		{
			name:       "more restrictive parent wins over finite leaf",
			leafCPU:    "200000 100000",
			parentCPU:  "50000 100000",
			leafMemory: "4294967296",
			parentMem:  "1073741824",
			wantCPU:    .5,
			wantMemory: 1024,
		},
		{
			name:       "finite leaf survives unlimited parent",
			leafCPU:    "150000 100000",
			parentCPU:  "max 100000",
			leafMemory: "3221225472",
			parentMem:  "max",
			wantCPU:    1.5,
			wantMemory: 3072,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := resourceFixtureV2(tt.leafCPU, tt.leafMemory)
			files["/sys/fs/cgroup/docker/cpu.max"] = tt.parentCPU + "\n"
			files["/sys/fs/cgroup/docker/memory.max"] = tt.parentMem + "\n"
			wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
			assertResourceCaps(t, wire, tt.wantCPU, tt.wantMemory)
		})
	}
}

func TestEnvironmentInfoBoundsCgroupV2AncestorWalkAtMountRoot(t *testing.T) {
	t.Parallel()
	for _, membership := range []string{"/delegated/root/team/leaf", "/team/leaf"} {
		t.Run(membership, func(t *testing.T) {
			files := map[string]string{
				"/proc/self/cgroup":                          "0::" + membership + "\n",
				"/proc/self/mountinfo":                       "29 23 0:26 /delegated/root /sys/fs/cgroup/tenant rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
				"/sys/fs/cgroup/tenant/team/leaf/cpu.max":    "max 100000\n",
				"/sys/fs/cgroup/tenant/team/leaf/memory.max": "max\n",
				"/sys/fs/cgroup/tenant/team/cpu.max":         "200000 100000\n",
				"/sys/fs/cgroup/tenant/team/memory.max":      "4294967296\n",
				"/sys/fs/cgroup/tenant/cpu.max":              "max 100000\n",
				"/sys/fs/cgroup/tenant/memory.max":           "max\n",
				// These limits are above the process-visible mount root and must never
				// influence the result.
				"/sys/fs/cgroup/cpu.max":    "50000 100000\n",
				"/sys/fs/cgroup/memory.max": "1073741824\n",
				"/proc/meminfo":             fixtureHostMemInfo(65536000),
			}
			wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
			assertResourceCaps(t, wire, 2, 4096)
		})
	}
}

func TestEnvironmentInfoRequiresValidResolvedCgroupMembership(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"/proc/self/mountinfo":          "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/cpu.max":        "max 100000\n",
		"/sys/fs/cgroup/memory.max":     "max\n",
		"/sys/fs/cgroup/one/cpu.max":    "max 100000\n",
		"/sys/fs/cgroup/one/memory.max": "max\n",
		"/proc/meminfo":                 fixtureHostMemInfo(65536000),
	}
	tests := []struct {
		name       string
		membership string
		readable   bool
	}{
		{name: "unreadable"},
		{name: "malformed", membership: "not-a-cgroup-record\n", readable: true},
		{name: "partially malformed", membership: "0::/one\ninvalid\n", readable: true},
		{name: "ambiguous unified hierarchy", membership: "0::/one\n0::/two\n", readable: true},
		{name: "ambiguous v1 controller", membership: "2:cpu:/one\n3:cpu:/two\n", readable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := maps.Clone(base)
			if tt.readable {
				files["/proc/self/cgroup"] = tt.membership
			}
			assertResourceCapsUnknown(t, resourceCapsJSON(t, newResourceFixtureEnv(t, files)))
		})
	}
}

func TestEnvironmentInfoRecognizesPositivelyResolvedRootMembership(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"/proc/self/cgroup":         "0::/\n",
		"/proc/self/mountinfo":      "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/cpu.max":    "max 100000\n",
		"/sys/fs/cgroup/memory.max": "max\n",
		"/proc/meminfo":             fixtureHostMemInfo(65536000),
	}
	assertResourceCaps(t, resourceCapsJSON(t, newResourceFixtureEnv(t, files)), float64(runtime.NumCPU()), 64000)
}

func TestEnvironmentInfoSelectsHybridHierarchyByControllerMembership(t *testing.T) {
	t.Parallel()
	for _, reverseMountOrder := range []bool{false, true} {
		name := "v2 mount first"
		if reverseMountOrder {
			name = "v1 mounts first"
		}
		t.Run(name, func(t *testing.T) {
			v2Mount := "40 29 0:40 / /sys/fs/cgroup/unified rw,relatime - cgroup2 cgroup rw\n"
			v1Mounts := "41 29 0:41 / /sys/fs/cgroup/cpu rw,relatime - cgroup cgroup rw,cpu\n" +
				"42 29 0:42 / /sys/fs/cgroup/cpuset rw,relatime - cgroup cgroup rw,cpuset\n" +
				"43 29 0:43 / /sys/fs/cgroup/memory rw,relatime - cgroup cgroup rw,memory\n"
			mounts := v2Mount + v1Mounts
			if reverseMountOrder {
				mounts = v1Mounts + v2Mount
			}
			files := map[string]string{
				"/proc/self/cgroup":                                    "0::/unrelated\n10:cpu:/workload\n9:cpuset:/workload\n8:memory:/workload\n",
				"/proc/self/mountinfo":                                 mounts,
				"/sys/fs/cgroup/cpu/workload/cpu.cfs_quota_us":         "150000\n",
				"/sys/fs/cgroup/cpu/workload/cpu.cfs_period_us":        "100000\n",
				"/sys/fs/cgroup/cpuset/workload/cpuset.cpus":           "0-3\n",
				"/sys/fs/cgroup/memory/workload/memory.limit_in_bytes": "2147483648\n",
				"/proc/meminfo":                                        fixtureHostMemInfo(65536000),
			}
			assertResourceCaps(t, resourceCapsJSON(t, newResourceFixtureEnv(t, files)), 1.5, 2048)
		})
	}
}

func TestEnvironmentInfoCombinesCoMountedCgroupV1CPUControllers(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"/proc/self/cgroup": "10:cpu,cpuset:/workload\n9:memory:/workload\n",
		"/proc/self/mountinfo": "40 29 0:40 / /sys/fs/cgroup/cpu-set rw,relatime - cgroup cgroup rw,cpu,cpuset\n" +
			"41 29 0:41 / /sys/fs/cgroup/memory rw,relatime - cgroup cgroup rw,memory\n",
		"/sys/fs/cgroup/cpu-set/workload/cpu.cfs_quota_us":     "400000\n",
		"/sys/fs/cgroup/cpu-set/workload/cpu.cfs_period_us":    "100000\n",
		"/sys/fs/cgroup/cpu-set/workload/cpuset.cpus":          "2-3\n",
		"/sys/fs/cgroup/memory/workload/memory.limit_in_bytes": "2147483648\n",
		"/proc/meminfo": fixtureHostMemInfo(65536000),
	}
	assertResourceCaps(t, resourceCapsJSON(t, newResourceFixtureEnv(t, files)), 2, 2048)
}

func TestEnvironmentInfoCombinesControllersAcrossHybridHierarchies(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"/proc/self/cgroup": "0::/workload\n9:cpuset:/workload\n",
		"/proc/self/mountinfo": "40 29 0:40 / /sys/fs/cgroup/unified rw,relatime - cgroup2 cgroup rw\n" +
			"41 29 0:41 / /sys/fs/cgroup/cpuset rw,relatime - cgroup cgroup rw,cpuset\n",
		"/sys/fs/cgroup/unified/workload/cpu.max":    "400000 100000\n",
		"/sys/fs/cgroup/unified/workload/memory.max": "2147483648\n",
		"/sys/fs/cgroup/cpuset/workload/cpuset.cpus": "2-3\n",
		"/proc/meminfo": fixtureHostMemInfo(65536000),
	}
	assertResourceCaps(t, resourceCapsJSON(t, newResourceFixtureEnv(t, files)), 2, 2048)
}

func TestEnvironmentInfoDoesNotTreatUnreadableOwnedV1CPUSetAsAbsent(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"/proc/self/cgroup": "10:cpu,cpuset:/workload\n9:memory:/workload\n",
		"/proc/self/mountinfo": "40 29 0:40 / /sys/fs/cgroup/cpu-set rw,relatime - cgroup cgroup rw,cpu,cpuset\n" +
			"41 29 0:41 / /sys/fs/cgroup/memory rw,relatime - cgroup cgroup rw,memory\n",
		"/sys/fs/cgroup/cpu-set/workload/cpu.cfs_quota_us":     "400000\n",
		"/sys/fs/cgroup/cpu-set/workload/cpu.cfs_period_us":    "100000\n",
		"/sys/fs/cgroup/memory/workload/memory.limit_in_bytes": "2147483648\n",
		"/proc/meminfo": fixtureHostMemInfo(65536000),
	}
	wire := resourceCapsJSON(t, newResourceFixtureEnv(t, files))
	resources, ok := wire["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources = %#v, want structured resource object", wire["resources"])
	}
	if _, ok := resources["cpus"]; ok {
		t.Errorf("resources.cpus = %#v, want unknown/omitted", resources["cpus"])
	}
	if got := resources["memory_mb"]; got != float64(2048) {
		t.Errorf("resources.memory_mb = %#v, want 2048", got)
	}
}
