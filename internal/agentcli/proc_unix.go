//go:build !windows

package agentcli

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// prepareTree puts the child in its own process group so the whole tree it
// spawns (MCP servers, shells…) can be stopped together.
func prepareTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillTree force-stops the child and everything it started. A process group
// is not enough: agents such as Claude Code start their shells in new process
// groups, so descendants are found through parent links and killed too.
func KillTree(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	tree := descendants(pid) // collect before killing: orphans get re-parented
	err := syscall.Kill(-pid, syscall.SIGKILL)
	for _, p := range tree {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	return err
}

// StopGracefully asks the child to exit cleanly (SIGTERM).
func StopGracefully(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}

// descendants lists every process below root, using `ps` (macOS and Linux).
func descendants(root int) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var all []int
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			all = append(all, c)
			queue = append(queue, c)
		}
	}
	return all
}
