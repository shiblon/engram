//go:build unix

package engram

import (
	"errors"
	"log"
	"os"
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

// terminateProcessGroup signals the whole group, then escalates after grace. It
// returns a stop function that cancels the pending escalation.
//
// exec.CommandContext kills only the direct child, so a timeout would otherwise
// leave the grandchildren running -- and it fails silently, as leaked processes
// rather than as an error, which is exactly the kind of bug nobody reports.
//
// The escalation timer MUST be cancellable. In the common case SIGTERM works and
// the process is gone long before grace elapses, but an uncancelled timer still
// fires and signals -pid -- and by then the OS may have recycled that pid for an
// unrelated process group. The ESRCH check guards "pid is gone", not "pid belongs
// to someone else now". An untracked timer also outlives the batch that made it.
func terminateProcessGroup(cmd *exec.Cmd, grace time.Duration) (stop func()) {
	if cmd == nil || cmd.Process == nil {
		return func() {}
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		log.Printf("engram dispatch: SIGTERM process group %d: %v", pid, err)
	}
	timer := time.AfterFunc(grace, func() {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			log.Printf("engram dispatch: SIGKILL process group %d: %v", pid, err)
		}
	})
	return func() { timer.Stop() }
}

// openNoFollow opens path for reading and refuses if it is a symlink, so a child
// cannot redirect its own result file at something else the dispatch user can read.
// O_NOFOLLOW makes the kernel enforce this, with no window between check and open.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// processAlive reports whether pid names a live process. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
