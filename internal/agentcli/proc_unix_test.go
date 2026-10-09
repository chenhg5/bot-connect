//go:build !windows

package agentcli

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A grandchild that moved to its own process group (as Claude Code's shells
// do) must still be killed.
func TestKillTreeReachesOtherProcessGroups(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not available")
	}
	cmd := exec.Command("sh", "-c", `perl -e 'setpgrp(0,0); print "$$\n"; $|=1; sleep 300' & wait`)
	prepareTree(cmd)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	gpid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("grandchild pid: %q", buf[:n])
	}
	if pg, _ := syscall.Getpgid(gpid); pg == cmd.Process.Pid {
		t.Fatal("test setup: grandchild should be in its own process group")
	}
	_ = KillTree(cmd)
	_ = cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(gpid, 0) != nil {
			return // gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(gpid, syscall.SIGKILL)
	t.Fatal("grandchild in another process group survived KillTree")
}
