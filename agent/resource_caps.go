package agent

import (
	"context"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

const resourceCapsProbeTimeout = time.Second

// resourceCaps is the effective CPU and memory budget exposed by the session's
// execution environment. A zero value means the corresponding cap was not
// available or was unlimited.
type resourceCaps struct {
	CPUs     float64
	MemoryMB int64
}

// probeEffectiveResourceCaps measures cgroup limits through the same command
// boundary that the session uses for all other environment probing. Host-level
// runtime and /proc values are not reliable in a capped container, so an
// unavailable or unlimited cgroup value is left unset rather than guessed.
func probeEffectiveResourceCaps(env execenv.ExecutionEnvironment, cwd string) resourceCaps {
	if env == nil || env.Platform() != "linux" {
		return resourceCaps{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), resourceCapsProbeTimeout)
	defer cancel()
	result, err := env.ExecCommand(ctx, resourceCapsProbeScript(), int(resourceCapsProbeTimeout/time.Millisecond), cwd, nil)
	if err != nil || result.ExitCode != 0 {
		return resourceCaps{}
	}
	return parseResourceCaps(result.Stdout)
}

func parseResourceCaps(out string) resourceCaps {
	values := make(map[string]string)
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = value
		}
	}

	var caps resourceCaps
	quota, quotaOK := positiveResourceInt(values["cpu_quota"])
	period, periodOK := positiveResourceInt(values["cpu_period"])
	if quotaOK && periodOK {
		caps.CPUs = float64(quota) / float64(period)
	}

	if memoryBytes, ok := positiveResourceInt(values["memory_bytes"]); ok && memoryBytes < 1<<60 {
		caps.MemoryMB = memoryBytes / (1024 * 1024)
	}
	return caps
}

func positiveResourceInt(value string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return n, err == nil && n > 0
}

// resourceCapsProbeScript reads cgroup v2 first and falls back to the v1
// controller paths. It emits raw values for the Go parser so the unit under
// test owns validation and conversion rather than shell arithmetic.
func resourceCapsProbeScript() string {
	return `cpu_quota=
cpu_period=
if [ -r /sys/fs/cgroup/cpu.max ]; then
  set -- $(cat /sys/fs/cgroup/cpu.max 2>/dev/null)
  cpu_quota=$1
  cpu_period=$2
else
  cpu_quota=$(cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us 2>/dev/null)
  cpu_period=$(cat /sys/fs/cgroup/cpu/cpu.cfs_period_us 2>/dev/null)
fi
case "$cpu_quota" in
  ''|max|-1|*[!0-9]*) ;;
  *)
    case "$cpu_period" in
      ''|0|*[!0-9]*) ;;
      *) printf 'cpu_quota=%s\ncpu_period=%s\n' "$cpu_quota" "$cpu_period" ;;
    esac
    ;;
esac

if [ -r /sys/fs/cgroup/memory.max ]; then
  memory_bytes=$(cat /sys/fs/cgroup/memory.max 2>/dev/null)
else
  memory_bytes=$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null)
fi
case "$memory_bytes" in
  ''|max|-1|*[!0-9]*) ;;
  *) printf 'memory_bytes=%s\n' "$memory_bytes" ;;
esac
`
}
