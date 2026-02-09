package agent

import (
	"context"
	"strings"
	"time"
)

func snapshotGit(env ExecutionEnvironment, cwd string) (inRepo bool, branch string, modifiedFiles int, untrackedFiles int, recentCommitTitles []string) {
	if env == nil {
		return false, "", 0, 0, nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}

	run := func(cmd string) (ExecResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return env.ExecCommand(ctx, cmd, 2_000, cwd, nil)
	}

	inside, err := run("git rev-parse --is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Stdout) != "true" {
		return false, "", 0, 0, nil
	}
	inRepo = true

	if br, err := run("git rev-parse --abbrev-ref HEAD"); err == nil && br.ExitCode == 0 {
		branch = strings.TrimSpace(br.Stdout)
	}

	if st, err := run("git status --porcelain"); err == nil && st.ExitCode == 0 {
		for _, line := range strings.Split(strings.ReplaceAll(st.Stdout, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "?? ") {
				untrackedFiles++
				continue
			}
			modifiedFiles++
		}
	}

	// Use %x20 for a literal space so the shell doesn't split the format across args.
	if lg, err := run("git log -n 10 --pretty=format:%h%x20%s"); err == nil && lg.ExitCode == 0 {
		for _, line := range strings.Split(strings.ReplaceAll(lg.Stdout, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			recentCommitTitles = append(recentCommitTitles, line)
		}
	}

	return inRepo, branch, modifiedFiles, untrackedFiles, recentCommitTitles
}
