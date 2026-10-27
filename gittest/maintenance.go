package gittest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Runner runs git in dir and returns its output.
type Runner func(dir string, args ...string) (string, error)

// MaintenanceProbe wraps a Runner so a fixture builder's post-commit
// `git maintenance` is replaced by a deterministic stand-in: after each commit
// it holds .git/objects/maintenance.lock until Published is called, which lets a
// test prove the fixture is not copied while a maintenance process is still
// writing into it.
type MaintenanceProbe struct {
	Ready                chan struct{}
	Release              chan struct{}
	Exited               chan struct{}
	PublishedWhileActive bool
	MutatorErr           error
}

// NewMaintenanceProbe returns an idle probe.
func NewMaintenanceProbe() *MaintenanceProbe {
	return &MaintenanceProbe{
		Ready:   make(chan struct{}),
		Release: make(chan struct{}),
		Exited:  make(chan struct{}),
	}
}

// MaintenanceAutoEnabled reports whether the last maintenance.auto setting in
// args' -c options leaves auto maintenance on (the default).
func MaintenanceAutoEnabled(args []string) bool {
	enabled := true
	for i := 0; i < len(args); i++ {
		var config string
		switch {
		case args[i] == "-c" && i+1 < len(args):
			config = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-c"):
			config = args[i][2:]
		default:
			continue
		}
		key, value, ok := strings.Cut(config, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "maintenance.auto") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "false", "0", "no", "off":
			enabled = false
		default:
			enabled = true
		}
	}
	return enabled
}

// QuiescentCommitArgs inserts -c maintenance.auto=false before the commit
// subcommand so real git does not run its own maintenance.
func QuiescentCommitArgs(args []string) []string {
	commit := slices.Index(args, "commit")
	if commit < 0 {
		return args
	}
	quiescent := make([]string, 0, len(args)+2)
	quiescent = append(quiescent, args[:commit]...)
	quiescent = append(quiescent, "-c", "maintenance.auto=false")
	return append(quiescent, args[commit:]...)
}

// Run returns a Runner that forwards to underlying and, after every commit
// that would have run auto maintenance, starts the lock-holding stand-in and
// waits until it holds the lock.
func (p *MaintenanceProbe) Run(underlying Runner) Runner {
	return func(dir string, args ...string) (string, error) {
		commit := slices.Contains(args, "commit")
		maintenanceAutoEnabled := commit && MaintenanceAutoEnabled(args)
		realArgs := args
		if maintenanceAutoEnabled {
			realArgs = QuiescentCommitArgs(args)
		}
		out, err := underlying(dir, realArgs...)
		if err != nil {
			return out, err
		}
		if maintenanceAutoEnabled {
			go func() {
				lock := filepath.Join(dir, ".git", "objects", "maintenance.lock")
				p.MutatorErr = os.WriteFile(lock, []byte("maintenance\n"), 0o644)
				close(p.Ready)
				if p.MutatorErr != nil {
					close(p.Exited)
					return
				}
				<-p.Release
				p.MutatorErr = os.Remove(lock)
				close(p.Exited)
			}()
			<-p.Ready
		}
		return out, nil
	}
}

// Published records the moment the fixture was copied out: if the stand-in
// still holds the lock, the copy overlapped it. It then releases the stand-in.
func (p *MaintenanceProbe) Published() {
	select {
	case <-p.Ready:
		select {
		case <-p.Exited:
		default:
			p.PublishedWhileActive = true
		}
		close(p.Release)
	default:
	}
}
