//go:build !windows

package agentcli

import (
	"os/exec"
	"syscall"
)

// prepareTree puts the child in its own process group so the whole tree it
// spawns (MCP servers, shells…) can be stopped together.
func prepareTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillTree force-stops the child and everything it started.
func KillTree(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// StopGracefully asks the child to exit cleanly (SIGTERM).
func StopGracefully(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
