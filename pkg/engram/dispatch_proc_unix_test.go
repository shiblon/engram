//go:build unix

package engram

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunBatchTearsDownTheWholeProcessGroup(t *testing.T) {
	// The previous version of this test proved nothing: its fake only slept, never
	// spawning a descendant, so killing the direct child was enough to pass -- while
	// the code under test exists specifically because a provider CLI spawns git,
	// ripgrep, and node, and exec.CommandContext would orphan all of them.
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("needs /bin/sleep to spawn a real descendant")
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	spec := fakeSpec(t, "hang-with-child")
	spec.Env[fakeGrandchildPidFile] = pidFile

	var stream bytes.Buffer
	config := BatchConfig{
		V:               DispatchConfigVersion,
		DeadlineSeconds: 30,
		Tasks:           []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake", DeadlineSeconds: 2}},
	}
	if _, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream)); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the fake never reported a grandchild pid, so this test would prove nothing: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}

	// Signal 0 probes for existence. Allow a moment for the SIGKILL escalation.
	alive := func() bool { return syscall.Kill(pid, 0) == nil }
	for i := 0; i < 40 && alive(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if alive() {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			t.Logf("cleanup kill: %v", err)
		}
		t.Fatal("the grandchild survived batch teardown: the process group was not torn down, only the direct child")
	}
}
