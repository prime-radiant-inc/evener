//go:build linux || darwin

package execenv

import (
	"fmt"
	"os"
	"syscall"
)

func processSignalName(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	return signalName(ws.Signal())
}

func signalName(signal syscall.Signal) string {
	known := map[syscall.Signal]string{
		syscall.SIGHUP:    "SIGHUP",
		syscall.SIGINT:    "SIGINT",
		syscall.SIGQUIT:   "SIGQUIT",
		syscall.SIGILL:    "SIGILL",
		syscall.SIGTRAP:   "SIGTRAP",
		syscall.SIGABRT:   "SIGABRT",
		syscall.SIGFPE:    "SIGFPE",
		syscall.SIGKILL:   "SIGKILL",
		syscall.SIGBUS:    "SIGBUS",
		syscall.SIGSEGV:   "SIGSEGV",
		syscall.SIGSYS:    "SIGSYS",
		syscall.SIGPIPE:   "SIGPIPE",
		syscall.SIGALRM:   "SIGALRM",
		syscall.SIGTERM:   "SIGTERM",
		syscall.SIGURG:    "SIGURG",
		syscall.SIGSTOP:   "SIGSTOP",
		syscall.SIGTSTP:   "SIGTSTP",
		syscall.SIGCONT:   "SIGCONT",
		syscall.SIGCHLD:   "SIGCHLD",
		syscall.SIGTTIN:   "SIGTTIN",
		syscall.SIGTTOU:   "SIGTTOU",
		syscall.SIGIO:     "SIGIO",
		syscall.SIGXCPU:   "SIGXCPU",
		syscall.SIGXFSZ:   "SIGXFSZ",
		syscall.SIGVTALRM: "SIGVTALRM",
		syscall.SIGPROF:   "SIGPROF",
		syscall.SIGWINCH:  "SIGWINCH",
		syscall.SIGUSR1:   "SIGUSR1",
		syscall.SIGUSR2:   "SIGUSR2",
	}
	if name, ok := known[signal]; ok {
		return name
	}
	return fmt.Sprintf("SIG%d", signal)
}
