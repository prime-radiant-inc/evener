package execenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// These benchmarks price one spawn's environment derivation against the spawn
// itself. The derived parts are launchEnv (commandEnvironment plus the virtualenv
// PATH injection) and findExecutable; the comparison points are whole spawns
// through the same environment — a bare one, and a ripgrep search.
//
// The comparison is the point: a derivation cost is only worth caching, with the
// invalidation that cache needs, if it is a material share of the spawn it
// precedes. A cached derivation is bounded below by copying the cached entries,
// so BenchmarkLaunchEnvCacheFloor prices the whole prize.
//
// The derived benchmarks run against the PRODUCTION environment source — no seam
// supplies the inherited entries — so the process environment is itself the
// input. Each of them therefore fixes and restores the process environment first
// (benchFixProcessEnv), which keeps the measured width at benchEnvWidth instead
// of whatever ambient width the host happens to have. The PATH-level lookups in
// the search benchmarks are real: PATH names directories this benchmark owns.

// benchEnvWidth and benchPathWidth are the sizes every benchmark here fixes:
// this machine's process environment carries 63 entries and PATH 20 directories.
const (
	benchEnvWidth  = 63
	benchPathWidth = 20
)

// benchFixProcessEnv replaces the process environment with exactly benchEnvWidth
// entries — PATH set to pathValue, HOME set to a directory this benchmark owns,
// then synthetic names — and restores the original on cleanup. It is what makes
// commandEnvironment's os.Environ() read a controlled input rather than an
// ambient one, and it is the only reason these benchmarks do not depend on the
// host's environment width.
//
// It manipulates the process environment directly instead of through b.Setenv
// because b.Setenv can only add entries, never fix the total width.
func benchFixProcessEnv(b *testing.B, pathValue string) {
	b.Helper()
	saved := os.Environ()
	// HOME points inside this benchmark's scratch so anything that consults it
	// stays out of the machine's real home.
	home := b.TempDir()
	controlled := make([]string, 0, benchEnvWidth)
	controlled = append(controlled, "PATH="+pathValue, "HOME="+home)
	for i := 2; i < benchEnvWidth; i++ {
		controlled = append(controlled, fmt.Sprintf("EVENER_BENCH_VAR_%02d=value-%02d", i, i))
	}
	applyProcessEnv(controlled)
	b.Cleanup(func() { applyProcessEnv(saved) })
}

// applyProcessEnv makes env exactly the process environment.
func applyProcessEnv(env []string) {
	os.Clearenv()
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			_ = os.Setenv(k, v)
		}
	}
}

// benchPathDirs makes benchPathWidth empty directories this benchmark owns and
// returns them joined as a PATH value. None holds an executable, so a lookup
// against it MISSES for real, through the production resolver.
func benchPathDirs(b *testing.B) (dirs []string, pathValue string) {
	b.Helper()
	base := b.TempDir()
	dirs = make([]string, 0, benchPathWidth)
	for i := range benchPathWidth {
		dir := filepath.Join(base, fmt.Sprintf("path%02d", i))
		if err := os.Mkdir(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	return dirs, strings.Join(dirs, string(os.PathListSeparator))
}

// benchSpawnEnv returns an environment rooted at a fresh temp dir whose PATH is
// benchPathWidth empty directories, with the process environment fixed to
// benchEnvWidth entries and the scratch and temp container confined to this
// benchmark's tmpdir. It returns the env and those PATH directories so a caller
// can drop a fixture executable among them.
func benchSpawnEnv(b *testing.B) (*LocalExecutionEnvironment, []string) {
	b.Helper()
	dirs, pathValue := benchPathDirs(b)
	benchFixProcessEnv(b, pathValue)

	root := b.TempDir()
	env := NewLocalExecutionEnvironment(root)
	benchConfineScratch(b, env)
	// Provision the lazy per-session scratch and temp container before the loop:
	// both are minted once per environment, so measuring them would price first
	// use rather than a spawn.
	_ = env.commandEnvironment(nil)
	return env, dirs
}

// benchConfineScratch confines everything env mints below the benchmark's own
// tmpdir: its sandbox scratch, and the world-usable temp base that the session
// temp container is created under. A benchmark may not read or litter the
// machine's real temp, and a spawn or search that mints a container would
// otherwise leave one there. Every benchmark that constructs an environment
// calls this, not only the ones routed through benchSpawnEnv.
func benchConfineScratch(b *testing.B, env *LocalExecutionEnvironment) {
	b.Helper()
	env.sandboxTmpBase = b.TempDir()
	worldBase := filepath.Join(b.TempDir(), "host-temp")
	if err := os.Mkdir(worldBase, 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.Chmod(worldBase, 0o777|os.ModeSticky); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(sandbox.SetWorldTempBasesForTesting([]string{worldBase}))
	b.Cleanup(env.Cleanup)
}

// BenchmarkLaunchEnv prices the whole environment a spawn hands its child: the
// snapshot of the process environment, the filtered base, the caller's overlay,
// and the virtualenv PATH injection. Compare with BenchmarkExecArgvSpawn, which
// pays this cost.
func BenchmarkLaunchEnv(b *testing.B) {
	env, _ := benchSpawnEnv(b)
	root := env.RootDir
	b.ReportAllocs()
	for b.Loop() {
		_ = env.launchEnv(root, map[string]string{"EVENER_CALLER": "1"})
	}
}

// BenchmarkCommandEnvironment prices the filtered environment alone, without
// the virtualenv PATH injection launchEnv adds. The snapshot IS included:
// commandEnvironment calls os.Environ itself, so subtracting this from
// BenchmarkLaunchEnv isolates the injection rather than the snapshot.
// BenchmarkOsEnviron prices the snapshot on its own.
func BenchmarkCommandEnvironment(b *testing.B) {
	env, _ := benchSpawnEnv(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = env.commandEnvironment(map[string]string{"EVENER_CALLER": "1"})
	}
}

// BenchmarkOsEnviron prices the ambient environment snapshot alone, at the fixed
// benchEnvWidth: the read commandEnvironment performs before filtering. It is
// the part of BenchmarkLaunchEnv that a cache of the derived base would also
// avoid, so it sets the floor under any saving.
func BenchmarkOsEnviron(b *testing.B) {
	_, pathValue := benchPathDirs(b)
	benchFixProcessEnv(b, pathValue)
	b.ReportAllocs()
	for b.Loop() {
		_ = os.Environ()
	}
}

// BenchmarkFindExecutable prices ripgrep's PATH resolution on both branches: a
// hit on the first PATH directory, and a miss that stats every directory — the
// shapes a search hits depending on whether ripgrep is installed. These divide
// into BenchmarkRipgrepSearch, a search that spawns, and
// BenchmarkNativeWalkSearch, a search that does not.
func BenchmarkFindExecutable(b *testing.B) {
	b.Run("present", func(b *testing.B) {
		env, pathDirs := benchSpawnEnv(b)
		if err := os.WriteFile(filepath.Join(pathDirs[0], "rg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := env.findExecutable("rg"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("absent", func(b *testing.B) {
		env, _ := benchSpawnEnv(b)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := env.findExecutable("rg"); err == nil {
				b.Fatal("rg resolved although no PATH directory holds it")
			}
		}
	})
}

// BenchmarkExecArgvSpawn prices a whole spawn whose environment is rebuilt from
// scratch. It is the comparison point for every derivation benchmark here: the
// difference between it and one of them is what a cache could buy.
func BenchmarkExecArgvSpawn(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("the fixture is a POSIX shell script")
	}
	env, pathDirs := benchSpawnEnv(b)
	root := env.RootDir
	if err := os.WriteFile(filepath.Join(pathDirs[0], "bench-true"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		res, err := env.ExecArgv(ctx, "bench-true", nil, 10_000, root, nil)
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("ExecArgv: err=%v res=%+v", err, res)
		}
	}
}

// benchSearchTree writes a small Go tree with one match and returns its root, so
// a search benchmark prices the walk and the lookup against the directory it
// actually searches.
func benchSearchTree(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	for i := range 20 {
		body := "func placeholder() {}\n"
		if i == 0 {
			body = "func benchmarkneedle() {}\n"
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file%02d.go", i)), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

// BenchmarkRipgrepSearch prices the ripgrep path end to end: a grep that
// resolves ripgrep and then spawns it twice (the search and the oversize
// listing). Resolution happens inside the timed call, so this is the number that
// sets how much of a real ripgrep search derivation is.
//
// The benchmark skips when ripgrep is absent: Grep falls back to the native Go
// walk in that case, and reporting that duration as a two-spawn search would
// price the wrong code path.
func BenchmarkRipgrepSearch(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("the fixture is a POSIX tree searched by ripgrep")
	}
	// The controlled environment pins PATH to directories this benchmark owns, so
	// spot the real ripgrep first and keep its directory on a PATH of the same
	// width: the search resolves a REAL ripgrep, and the environment stays fixed.
	resolved, err := execLookPath("rg")
	if err != nil {
		b.Skip("ripgrep is not installed, so Grep would take the native walk this name does not price")
	}
	dirs, pathValue := benchPathDirs(b)
	if err := os.Symlink(resolved, filepath.Join(dirs[0], "rg")); err != nil {
		b.Fatal(err)
	}
	benchFixProcessEnv(b, pathValue)

	root := benchSearchTree(b)
	env := NewLocalExecutionEnvironment(root)
	benchConfineScratch(b, env)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := env.Grep(ctx, "benchmarkneedle", root, "", false, 10, "content"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNativeWalkSearch prices the one shape where findExecutable's PATH
// miss is not dwarfed by a spawn: an rg-absent host falls back to the native Go
// walk, so the whole call is the failed lookup plus an in-process scan. It is
// the strongest case for caching a resolved path, so the comparison against
// BenchmarkExecArgvSpawn is made here rather than assumed.
//
// The miss is a REAL one: PATH names empty directories this benchmark owns and no
// lookup seam is set, so Grep runs the production resolver and pays its stat over
// every directory inside the timed call.
func BenchmarkNativeWalkSearch(b *testing.B) {
	root := benchSearchTree(b)
	_, pathValue := benchPathDirs(b)
	benchFixProcessEnv(b, pathValue)
	env := NewLocalExecutionEnvironment(root)
	benchConfineScratch(b, env)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := env.Grep(ctx, "benchmarkneedle", root, "", false, 10, "content"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLaunchEnvCacheFloor is the absolute lower bound a per-environment
// cache could reach: clone the already-derived environment and append one
// caller entry, with no snapshot, no filtering, no caller-override pass, and no
// virtualenv scan at all. No real cache can beat this shape, because the
// caller's overrides and the virtualenv additions change per invocation and must
// stay per call, so it prices the whole prize; comparing it with
// BenchmarkLaunchEnv prices the ceiling on what a cache could save.
func BenchmarkLaunchEnvCacheFloor(b *testing.B) {
	env, _ := benchSpawnEnv(b)
	base := env.commandEnvironment(nil)
	extra := map[string]string{"EVENER_CALLER": "1"}
	b.ReportAllocs()
	for b.Loop() {
		out := slices.Clone(base)
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
	}
}
