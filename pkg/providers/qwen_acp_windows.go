//go:build windows

package providers

import "os/exec"

// qwenSetProcessGroup is a no-op on Windows; process group signalling via
// negative PIDs is not supported.
func qwenSetProcessGroup(cmd *exec.Cmd) {}

// qwenKillProcessGroup falls back to killing only the direct process on Windows.
func qwenKillProcessGroup(cmd *exec.Cmd) {
	cmd.Process.Kill() //nolint:errcheck
}
