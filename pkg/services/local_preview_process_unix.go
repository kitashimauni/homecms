//go:build !windows

package services

import (
	"fmt"
	"os/exec"
	"syscall"
)

type localPreviewProcessTree struct{}

func attachLocalPreviewProcessTree(_ *exec.Cmd) (*localPreviewProcessTree, error) {
	return &localPreviewProcessTree{}, nil
}

func closeLocalPreviewProcessTree(_ *localPreviewProcessTree) error {
	return nil
}

func configureLocalPreviewCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.WaitDelay = localPreviewProcessWaitDelay
}

func signalLocalPreviewProcess(cmd *exec.Cmd, _ *localPreviewProcessTree, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func localPreviewProcessTreeAlive(cmd *exec.Cmd, _ *localPreviewProcessTree) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	return err == nil || err == syscall.EPERM
}

func localPreviewProcessDescription(cmd *exec.Cmd, _ *localPreviewProcessTree) string {
	if cmd == nil || cmd.Process == nil {
		return "pid=unknown process_group=unknown"
	}
	return fmt.Sprintf("pid=%d process_group=%d", cmd.Process.Pid, cmd.Process.Pid)
}
