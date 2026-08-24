package agent

import (
	"encoding/json"
	"io/fs"
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
