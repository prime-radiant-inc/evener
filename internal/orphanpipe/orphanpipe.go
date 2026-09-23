// Package orphanpipe handles a subprocess that exits while a descendant it
// left behind still holds its captured output pipe.
//
// exec.Cmd reads a captured stdout or stderr until EOF, and EOF arrives only
// once every process holding the pipe's write end has closed it. A child that
// backgrounds something (a wrapper script, a shell rc file, a hook command,
// ssh's ProxyCommand) hands that end to the grandchild, so Wait lasts as long
// as the grandchild does. Killing the child when its context ends does not
// help, because the grandchild is not killed with it: a context timeout on
// such a call bounds nothing.
//
// Setting cmd.WaitDelay is the bound. Once the context ends or the child
// exits, whichever comes first, exec waits at most WaitDelay for the pipes to
// close, then closes them itself and returns. When the child had exited 0 that
// return is exec.ErrWaitDelay over the output the child finished writing, and
// ChildErr reads it as the success it was.
package orphanpipe

import (
	"errors"
	"os/exec"
)

// ChildErr returns the error a Wait-backed call on cmd (Run, Output,
// CombinedOutput, Wait) reported, except that exec.ErrWaitDelay after a
// successful exit becomes nil: the child answered, and only a descendant it
// left behind kept the pipe open past cmd.WaitDelay.
func ChildErr(cmd *exec.Cmd, err error) error {
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	return err
}
