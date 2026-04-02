//go:build !windows

package providers

import (
	"os/exec"
	"syscall"
)

// qwenSetProcessGroup puts the child into its own process group so that
// qwenKillProcessGroup can reap it and any grandchildren it spawns.
func qwenSetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// qwenKillProcessGroup sends SIGKILL to the entire process group.
func qwenKillProcessGroup(cmd *exec.Cmd) {
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck
}
