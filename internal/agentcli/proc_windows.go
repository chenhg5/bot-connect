//go:build windows

package agentcli

import (
	"os/exec"
	"strconv"
	"syscall"
)

// prepareTree starts the child in a new process group so taskkill /T can
// stop the whole tree it spawns (MCP servers, shells…).
func prepareTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// KillTree force-stops the child and everything it started.
func KillTree(cmd *exec.Cmd) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// StopGracefully asks the child tree to close (taskkill without /F),
// falling back to a hard kill.
func StopGracefully(cmd *exec.Cmd) error {
	if err := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return KillTree(cmd)
	}
	return nil
}
