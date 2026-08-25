package agent

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
)

// hostResourceFileReader is an optional seam for environments that can expose
// process-host files through a virtual filesystem. LocalExecutionEnvironment
// does not implement it and therefore uses the real host filesystem below.
// Keeping it outside ExecutionEnvironment prevents a host-only probe from
// becoming part of every sandbox/test environment implementation.
type hostResourceFileReader interface {
	ReadHostFile(path string) ([]byte, error)
}

type resourceFileReader func(string) ([]byte, error)

func newResourceFileReader(env execenv.ExecutionEnvironment) resourceFileReader {
	if reader, ok := env.(hostResourceFileReader); ok {
		return reader.ReadHostFile
	}
	return os.ReadFile
}

type resourceState uint8

const (
	resourceAbsent resourceState = iota
	resourceLimited
	resourceUnlimited
	resourceUnknown
)

type resourceReading struct {
	state resourceState
	value float64
}

type cgroupMount struct {
	v2          bool
	root        string
	mountPoint  string
	controllers map[string]bool
}

type cgroupMembership struct {
	path        string
	controllers map[string]bool
	v2          bool
}

// resourceCapsFromEnv reads only process-visible resource metadata. A Linux
// cgroup mount is authoritative for that resource: a missing or malformed
// controller file remains unknown instead of falling back to /proc host data.
// Host fallbacks are used only when no cgroup hierarchy is visible, or when a
// visible controller explicitly says it is unlimited.
func resourceCapsFromEnv(env execenv.ExecutionEnvironment) *schema.ResourceCaps {
	if env == nil || strings.ToLower(strings.TrimSpace(env.Platform())) != "linux" {
		return nil
	}

	read := newResourceFileReader(env)
	cgroupText, cgroupErr := read("/proc/self/cgroup")
	mountInfoText, _ := read("/proc/self/mountinfo")
	mounts := parseCgroupMounts(string(mountInfoText))
	if cgroupErr != nil {
		return &schema.ResourceCaps{}
	}
	memberships, validMemberships := parseCgroupMemberships(string(cgroupText))
	if !validMemberships {
		return &schema.ResourceCaps{}
	}

	// A readable root membership without a visible cgroup mount is the normal
	// bare-host shape on systems where mountinfo is unavailable to the process.
	// Missing cgroup metadata is instead unknown. A non-root membership is
	// evidence of a container/cgroup even if its mount metadata cannot be read,
	// so it must not be represented by host facts.
	if len(mounts) == 0 {
		if cgroupMembershipIsRootOnly(memberships) {
			return hostResourceCaps(read)
		}
		// Some restricted proc views omit mountinfo even though the standard
		// cgroup paths remain readable. Only synthesize a mount for a non-root
		// membership; a root-only membership with no mount is the host fallback
		// above.
		mounts = addStandardCgroupMounts(mounts, memberships)
		if len(mounts) == 0 {
			return &schema.ResourceCaps{}
		}
	}

	caps := &schema.ResourceCaps{}
	cpu := effectiveCPU(read, mounts, memberships)
	memory := effectiveMemory(read, mounts, memberships)
	if cpu.state == resourceLimited || cpu.state == resourceUnlimited {
		caps.CPUs = cpu.value
	}
	if memory.state == resourceLimited || memory.state == resourceUnlimited {
		caps.MemoryMB = int64(memory.value)
	}
	return caps
}

func hostResourceCaps(read resourceFileReader) *schema.ResourceCaps {
	caps := &schema.ResourceCaps{}
	if cpus := runtime.NumCPU(); cpus > 0 {
		caps.CPUs = float64(cpus)
	}
	if b, err := read("/proc/meminfo"); err == nil {
		if total, ok := parseMemTotalMB(string(b)); ok {
			caps.MemoryMB = total
		}
	}
	return caps
}

func effectiveCPU(read resourceFileReader, mounts []cgroupMount, memberships []cgroupMembership) resourceReading {
	quota := effectiveCPUQuota(read, mounts, memberships)
	cpuset := effectiveCPUSet(read, mounts, memberships)
	return combineCPUReadings(quota, cpuset)
}

func effectiveMemory(read resourceFileReader, mounts []cgroupMount, memberships []cgroupMembership) resourceReading {
	membership, ownedByV1 := membershipForV1Controller(memberships, "memory")
	if ownedByV1 {
		reading := resourceReading{state: resourceAbsent}
		found := false
		for _, mount := range mounts {
			if mount.v2 || !mount.controllers["memory"] {
				continue
			}
			found = true
			path, ok := cgroupPathForMount(mount, membership.path)
			if !ok {
				return resourceReading{state: resourceUnknown}
			}
			candidate := readMemoryLimit(read, filepath.Join(path, "memory.limit_in_bytes"), false)
			reading = moreRestrictiveReading(reading, candidate)
		}
		if !found {
			return resourceReading{state: resourceUnknown}
		}
		return reading
	}

	membership, ok := v2Membership(memberships)
	if !ok {
		return resourceReading{state: resourceUnknown}
	}
	reading := resourceReading{state: resourceAbsent}
	found := false
	for _, mount := range mounts {
		if !mount.v2 {
			continue
		}
		found = true
		candidate := readV2MemoryHierarchy(read, mount, membership.path)
		reading = moreRestrictiveReading(reading, candidate)
	}
	if !found {
		return resourceReading{state: resourceUnknown}
	}
	return reading
}

func effectiveCPUQuota(read resourceFileReader, mounts []cgroupMount, memberships []cgroupMembership) resourceReading {
	membership, ownedByV1 := membershipForV1Controller(memberships, "cpu")
	if ownedByV1 {
		reading := resourceReading{state: resourceAbsent}
		found := false
		for _, mount := range mounts {
			if mount.v2 || !mount.controllers["cpu"] {
				continue
			}
			found = true
			path, ok := cgroupPathForMount(mount, membership.path)
			if !ok {
				return resourceReading{state: resourceUnknown}
			}
			candidate := readV1CPU(read, filepath.Join(path, "cpu.cfs_quota_us"), filepath.Join(path, "cpu.cfs_period_us"))
			reading = moreRestrictiveReading(reading, candidate)
		}
		if !found {
			return resourceReading{state: resourceUnknown}
		}
		return reading
	}

	membership, ok := v2Membership(memberships)
	if !ok {
		return resourceReading{state: resourceAbsent}
	}
	reading := resourceReading{state: resourceAbsent}
	found := false
	for _, mount := range mounts {
		if !mount.v2 {
			continue
		}
		found = true
		candidate := readV2CPUHierarchy(read, mount, membership.path)
		reading = moreRestrictiveReading(reading, candidate)
	}
	if !found {
		return resourceReading{state: resourceUnknown}
	}
	return reading
}

func effectiveCPUSet(read resourceFileReader, mounts []cgroupMount, memberships []cgroupMembership) resourceReading {
	membership, ownedByV1 := membershipForV1Controller(memberships, "cpuset")
	if ownedByV1 {
		reading := resourceReading{state: resourceAbsent}
		found := false
		for _, mount := range mounts {
			if mount.v2 || !mount.controllers["cpuset"] {
				continue
			}
			found = true
			path, ok := cgroupPathForMount(mount, membership.path)
			if !ok {
				return resourceReading{state: resourceUnknown}
			}
			candidate := readCPUSet(read, filepath.Join(path, "cpuset.cpus"))
			if candidate.state == resourceAbsent {
				candidate.state = resourceUnknown
			}
			reading = moreRestrictiveReading(reading, candidate)
		}
		if !found {
			return resourceReading{state: resourceUnknown}
		}
		return reading
	}

	membership, ok := v2Membership(memberships)
	if !ok {
		return resourceReading{state: resourceAbsent}
	}
	reading := resourceReading{state: resourceAbsent}
	found := false
	for _, mount := range mounts {
		if !mount.v2 {
			continue
		}
		found = true
		path, pathOK := cgroupPathForMount(mount, membership.path)
		if !pathOK {
			return resourceReading{state: resourceUnknown}
		}
		candidate := readCPUSet(read, filepath.Join(path, "cpuset.cpus.effective"))
		reading = moreRestrictiveReading(reading, candidate)
	}
	if !found {
		return resourceReading{state: resourceUnknown}
	}
	return reading
}

func combineCPUReadings(quota, cpuset resourceReading) resourceReading {
	switch {
	case quota.state == resourceUnknown || cpuset.state == resourceUnknown:
		return resourceReading{state: resourceUnknown}
	case quota.state == resourceLimited && cpuset.state == resourceLimited:
		return resourceReading{state: resourceLimited, value: minPositive(quota.value, cpuset.value)}
	case quota.state == resourceLimited:
		return quota
	case cpuset.state == resourceLimited:
		return cpuset
	case quota.state == resourceUnlimited && cpuset.state == resourceUnlimited:
		return resourceReading{state: resourceUnlimited, value: float64(runtime.NumCPU())}
	case quota.state == resourceUnlimited && cpuset.state == resourceAbsent:
		return resourceReading{state: resourceUnlimited, value: float64(runtime.NumCPU())}
	case quota.state == resourceAbsent && cpuset.state == resourceLimited:
		return cpuset
	default:
		return resourceReading{state: resourceUnknown}
	}
}

func moreRestrictiveReading(current, candidate resourceReading) resourceReading {
	if current.state == resourceAbsent {
		return candidate
	}
	if candidate.state == resourceAbsent {
		return current
	}
	if current.state == resourceUnknown || candidate.state == resourceUnknown {
		return resourceReading{state: resourceUnknown}
	}
	if current.state == resourceUnlimited {
		return candidate
	}
	if candidate.state == resourceUnlimited {
		return current
	}
	return resourceReading{state: resourceLimited, value: minPositive(current.value, candidate.value)}
}

func readV2CPUHierarchy(read resourceFileReader, mount cgroupMount, membership string) resourceReading {
	paths, ok := cgroupPathsToMountRoot(mount, membership)
	if !ok {
		return resourceReading{state: resourceUnknown}
	}
	if mount.root == "/" && len(paths) > 0 {
		paths = paths[:len(paths)-1]
	}
	if len(paths) == 0 {
		return resourceReading{state: resourceUnlimited, value: float64(runtime.NumCPU())}
	}
	reading := readV2CPU(read, filepath.Join(paths[0], "cpu.max"))
	if reading.state == resourceAbsent {
		return reading
	}
	for _, path := range paths[1:] {
		candidate := readV2CPU(read, filepath.Join(path, "cpu.max"))
		if candidate.state == resourceAbsent {
			return resourceReading{state: resourceUnknown}
		}
		reading = moreRestrictiveReading(reading, candidate)
	}
	return reading
}

func readV2MemoryHierarchy(read resourceFileReader, mount cgroupMount, membership string) resourceReading {
	paths, ok := cgroupPathsToMountRoot(mount, membership)
	if !ok {
		return resourceReading{state: resourceUnknown}
	}
	if mount.root == "/" && len(paths) > 0 {
		paths = paths[:len(paths)-1]
	}
	if len(paths) == 0 {
		return resourceReading{state: resourceUnlimited, value: float64(hostMemoryMB(read))}
	}
	reading := resourceReading{state: resourceAbsent}
	for _, path := range paths {
		candidate := readMemoryLimit(read, filepath.Join(path, "memory.max"), true)
		reading = moreRestrictiveReading(reading, candidate)
	}
	return reading
}

func readV2CPU(read resourceFileReader, path string) resourceReading {
	b, err := read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return resourceReading{state: resourceAbsent}
		}
		return resourceReading{state: resourceUnknown}
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		return resourceReading{state: resourceUnknown}
	}
	if fields[0] == "max" {
		period, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || period == 0 {
			return resourceReading{state: resourceUnknown}
		}
		return resourceReading{state: resourceUnlimited, value: float64(runtime.NumCPU())}
	}
	quota, errQuota := strconv.ParseUint(fields[0], 10, 64)
	period, errPeriod := strconv.ParseUint(fields[1], 10, 64)
	if errQuota != nil || errPeriod != nil || quota == 0 || period == 0 {
		return resourceReading{state: resourceUnknown}
	}
	return resourceReading{state: resourceLimited, value: minPositive(float64(quota)/float64(period), float64(runtime.NumCPU()))}
}

func readV1CPU(read resourceFileReader, quotaPath, periodPath string) resourceReading {
	quotaBytes, err := read(quotaPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return resourceReading{state: resourceUnknown}
		}
		return resourceReading{state: resourceUnknown}
	}
	quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaBytes)), 10, 64)
	if err != nil {
		return resourceReading{state: resourceUnknown}
	}
	if quota == -1 {
		return resourceReading{state: resourceUnlimited, value: float64(runtime.NumCPU())}
	}
	periodBytes, err := read(periodPath)
	if err != nil {
		return resourceReading{state: resourceUnknown}
	}
	period, err := strconv.ParseInt(strings.TrimSpace(string(periodBytes)), 10, 64)
	if err != nil || quota <= 0 || period <= 0 {
		return resourceReading{state: resourceUnknown}
	}
	return resourceReading{state: resourceLimited, value: minPositive(float64(quota)/float64(period), float64(runtime.NumCPU()))}
}

func readCPUSet(read resourceFileReader, path string) resourceReading {
	b, err := read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if strings.HasSuffix(path, "/cpuset.cpus.effective") {
				fallback, fallbackErr := read(strings.TrimSuffix(path, ".effective"))
				if fallbackErr == nil {
					b = fallback
				} else if !errors.Is(fallbackErr, fs.ErrNotExist) {
					return resourceReading{state: resourceUnknown}
				} else {
					return resourceReading{state: resourceAbsent}
				}
			} else {
				return resourceReading{state: resourceAbsent}
			}
		} else {
			return resourceReading{state: resourceUnknown}
		}
	}
	count, ok := countCPUSet(strings.TrimSpace(string(b)))
	if !ok || count <= 0 {
		return resourceReading{state: resourceUnknown}
	}
	return resourceReading{state: resourceLimited, value: minPositive(float64(count), float64(runtime.NumCPU()))}
}

func readMemoryLimit(read resourceFileReader, path string, v2 bool) resourceReading {
	b, err := read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return resourceReading{state: resourceUnknown}
		}
		return resourceReading{state: resourceUnknown}
	}
	text := strings.TrimSpace(string(b))
	if v2 && text == "max" {
		return resourceReading{state: resourceUnlimited, value: float64(hostMemoryMB(read))}
	}
	bytes, err := strconv.ParseUint(text, 10, 64)
	if err != nil || bytes == 0 {
		if !v2 && isV1MemoryUnlimited(text) {
			return resourceReading{state: resourceUnlimited, value: float64(hostMemoryMB(read))}
		}
		return resourceReading{state: resourceUnknown}
	}
	if !v2 && bytes >= uint64(math.MaxInt64)/2 {
		return resourceReading{state: resourceUnlimited, value: float64(hostMemoryMB(read))}
	}
	return resourceReading{state: resourceLimited, value: float64(bytesToMB(bytes))}
}

func hostMemoryMB(read resourceFileReader) int64 {
	b, err := read("/proc/meminfo")
	if err != nil {
		return 0
	}
	value, ok := parseMemTotalMB(string(b))
	if !ok {
		return 0
	}
	return value
}

func parseMemTotalMB(text string) (int64, bool) {
	for line := range strings.SplitSeq(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value == 0 {
			return 0, false
		}
		multiplier := uint64(1024)
		if len(fields) >= 3 {
			switch strings.ToLower(fields[2]) {
			case "kb", "kib":
				multiplier = 1024
			case "mb", "mib":
				multiplier = 1024 * 1024
			case "gb", "gib":
				multiplier = 1024 * 1024 * 1024
			case "b":
				multiplier = 1
			default:
				return 0, false
			}
		}
		if value > math.MaxUint64/multiplier {
			return 0, false
		}
		return bytesToMB(value * multiplier), true
	}
	return 0, false
}

func bytesToMB(bytes uint64) int64 {
	const mb = uint64(1024 * 1024)
	if bytes > uint64(math.MaxInt64) {
		return math.MaxInt64 / int64(mb)
	}
	return int64(bytes / mb)
}

func isV1MemoryUnlimited(text string) bool {
	return text == "-1" || text == "9223372036854771712" || text == "9223372036854775807"
}

func minPositive(a, b float64) float64 {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func parseCgroupMemberships(text string) ([]cgroupMembership, bool) {
	var memberships []cgroupMembership
	seenHierarchy := make(map[uint64]bool)
	seenControllers := make(map[string]bool)
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			return nil, false
		}
		hierarchy, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || seenHierarchy[hierarchy] || fields[2] == "" || !strings.HasPrefix(fields[2], "/") {
			return nil, false
		}
		seenHierarchy[hierarchy] = true
		controllers := make(map[string]bool)
		if hierarchy == 0 {
			if fields[1] != "" {
				return nil, false
			}
		} else {
			if fields[1] == "" {
				return nil, false
			}
			for controller := range strings.SplitSeq(fields[1], ",") {
				controller = strings.TrimSpace(controller)
				if controller == "" || seenControllers[controller] {
					return nil, false
				}
				seenControllers[controller] = true
				if strings.HasPrefix(controller, "name=") {
					continue
				}
				controllers[controller] = true
			}
		}
		memberships = append(memberships, cgroupMembership{
			path:        cleanAbsolute(fields[2]),
			controllers: controllers,
			v2:          hierarchy == 0,
		})
	}
	return memberships, len(memberships) > 0
}

func parseCgroupMounts(text string) []cgroupMount {
	var mounts []cgroupMount
	for line := range strings.SplitSeq(text, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		pre := strings.Fields(parts[0])
		post := strings.Fields(parts[1])
		if len(pre) < 6 || len(post) < 3 {
			continue
		}
		fsType := post[0]
		if fsType != "cgroup" && fsType != "cgroup2" {
			continue
		}
		controllers := make(map[string]bool)
		if fsType == "cgroup" {
			for option := range strings.SplitSeq(post[2], ",") {
				if option == "cpu" || option == "cpuacct" || option == "cpuset" || option == "memory" {
					controllers[option] = true
				}
			}
			for option := range strings.SplitSeq(pre[5], ",") {
				if option == "cpu" || option == "cpuacct" || option == "cpuset" || option == "memory" {
					controllers[option] = true
				}
			}
		}
		mounts = append(mounts, cgroupMount{
			v2:          fsType == "cgroup2",
			root:        cleanAbsolute(unescapeMountInfo(pre[3])),
			mountPoint:  cleanAbsolute(unescapeMountInfo(pre[4])),
			controllers: controllers,
		})
	}
	return mounts
}

func addStandardCgroupMounts(mounts []cgroupMount, memberships []cgroupMembership) []cgroupMount {
	if hasV2Mount(mounts) || hasV1Mount(mounts) {
		return mounts
	}
	for _, membership := range memberships {
		if membership.v2 && membership.path != "/" {
			mounts = append(mounts, cgroupMount{v2: true, root: "/", mountPoint: "/sys/fs/cgroup"})
			break
		}
		if membership.path == "/" {
			continue
		}
		for controller := range membership.controllers {
			if controller != "cpu" && controller != "cpuset" && controller != "memory" {
				continue
			}
			mountPoint := "/sys/fs/cgroup/" + controller
			if controller == "cpu" && membership.controllers["cpuacct"] {
				mountPoint = "/sys/fs/cgroup/cpu,cpuacct"
			}
			mounts = append(mounts, cgroupMount{
				root:        "/",
				mountPoint:  mountPoint,
				controllers: map[string]bool{controller: true},
			})
		}
	}
	return mounts
}

func hasV2Mount(mounts []cgroupMount) bool {
	for _, mount := range mounts {
		if mount.v2 {
			return true
		}
	}
	return false
}

func hasV1Mount(mounts []cgroupMount) bool {
	for _, mount := range mounts {
		if !mount.v2 {
			return true
		}
	}
	return false
}

func cgroupMembershipIsRootOnly(memberships []cgroupMembership) bool {
	if len(memberships) == 0 {
		return false
	}
	for _, membership := range memberships {
		if membership.path != "/" {
			return false
		}
	}
	return true
}

func v2Membership(memberships []cgroupMembership) (cgroupMembership, bool) {
	for _, membership := range memberships {
		if membership.v2 {
			return membership, true
		}
	}
	return cgroupMembership{}, false
}

func membershipForV1Controller(memberships []cgroupMembership, controller string) (cgroupMembership, bool) {
	for _, membership := range memberships {
		if !membership.v2 && membership.controllers[controller] {
			return membership, true
		}
	}
	return cgroupMembership{}, false
}

func cgroupPathForMount(mount cgroupMount, membershipPath string) (string, bool) {
	membershipPath = cleanAbsolute(membershipPath)
	root := cleanAbsolute(mount.root)
	relativeMembership := strings.TrimPrefix(membershipPath, "/")
	if root != "/" {
		switch {
		case membershipPath == root:
			relativeMembership = ""
		case strings.HasPrefix(membershipPath, root+"/"):
			relativeMembership = strings.TrimPrefix(membershipPath, root+"/")
		}
	}
	mountPoint := cleanAbsolute(mount.mountPoint)
	path := filepath.Join(mountPoint, relativeMembership)
	rel, err := filepath.Rel(mountPoint, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return path, true
}

func cgroupPathsToMountRoot(mount cgroupMount, membershipPath string) ([]string, bool) {
	leaf, ok := cgroupPathForMount(mount, membershipPath)
	if !ok {
		return nil, false
	}
	mountPoint := cleanAbsolute(mount.mountPoint)
	paths := make([]string, 0, 4)
	for current := leaf; ; current = filepath.Dir(current) {
		paths = append(paths, current)
		if current == mountPoint {
			return paths, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, false
		}
		rel, err := filepath.Rel(mountPoint, parent)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, false
		}
	}
}

func cleanAbsolute(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return filepath.Clean(path)
}

func unescapeMountInfo(path string) string {
	for _, replacement := range []struct{ escaped, value string }{
		{`\040`, " "},
		{`\011`, "\t"},
		{`\012`, "\n"},
		{`\134`, `\`},
	} {
		path = strings.ReplaceAll(path, replacement.escaped, replacement.value)
	}
	return path
}

func countCPUSet(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	count := int64(0)
	for item := range strings.SplitSeq(text, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return 0, false
		}
		bounds := strings.Split(item, "-")
		if len(bounds) > 2 {
			return 0, false
		}
		first, err := strconv.ParseInt(bounds[0], 10, 64)
		if err != nil || first < 0 {
			return 0, false
		}
		last := first
		if len(bounds) == 2 {
			last, err = strconv.ParseInt(bounds[1], 10, 64)
			if err != nil || last < first {
				return 0, false
			}
		}
		width := uint64(last - first)
		if width == math.MaxUint64 {
			return 0, false
		}
		width++
		if width > uint64(math.MaxInt64-count) {
			return 0, false
		}
		count += int64(width)
	}
	if count > int64(math.MaxInt) {
		return 0, false
	}
	return int(count), true
}
