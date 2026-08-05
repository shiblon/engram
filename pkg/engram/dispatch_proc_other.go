//go:build !unix

package engram

import (
	"log"
	"os/exec"
	"time"
)

// Windows and other non-unix platforms have no process groups in the POSIX sense,
// so teardown is best-effort on the direct child. A provider CLI's grandchildren
// may survive here; that is a platform limitation worth stating rather than
// papering over, and it is why the argv array (which is genuinely OS-neutral) is
// the part of the design that carries across platform classes.

func configureProcessGroup(cmd *exec.Cmd) {}

func terminateProcessGroup(cmd *exec.Cmd, grace time.Duration) (stop func()) {
	if cmd == nil || cmd.Process == nil {
		return func() {}
	}
	if err := cmd.Process.Kill(); err != nil {
		log.Printf("engram dispatch: kill child %d: %v", cmd.Process.Pid, err)
	}
	// Nothing is scheduled here, so there is nothing to cancel.
	return func() {}
}
