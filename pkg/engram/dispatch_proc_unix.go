//go:build unix

package engram

import (
	"errors"
	"log"
	"os/exec"
	"syscall"
	"time"
)

// Every provider CLI spawns children of its own (git, ripgrep, node, python), so
// the unit of control is the process group, never the pid.

// configureProcessGroup puts the child in its own process group and keeps the
// pgid reachable for a group-wide signal.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessGroup signals the whole group, then escalates after grace.
// exec.CommandContext kills only the direct child, so a timeout would otherwise
// leave the grandchildren running -- and it fails silently, as leaked processes
// rather than as an error, which is exactly the kind of bug nobody reports.
func terminateProcessGroup(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		log.Printf("engram dispatch: SIGTERM process group %d: %v", pid, err)
	}
	time.AfterFunc(grace, func() {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			log.Printf("engram dispatch: SIGKILL process group %d: %v", pid, err)
		}
	})
}
