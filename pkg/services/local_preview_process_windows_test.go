//go:build windows

package services

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWindowsLocalPreviewProcessTreeTerminatesWrapperChildren(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping 127.0.0.1 -n 30 > nul")
	configureLocalPreviewCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start suspended preview wrapper: %v", err)
	}
	tree, err := attachLocalPreviewProcessTree(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("attach preview wrapper to job: %v", err)
	}
	defer closeLocalPreviewProcessTree(tree)

	process := &managedLocalPreviewProcess{
		cmd:  cmd,
		tree: tree,
		done: make(chan struct{}),
	}
	go func() {
		process.setWaitErr(cmd.Wait())
		close(process.done)
	}()

	if !localPreviewProcessTreeAlive(cmd, tree) {
		t.Fatal("preview process tree was not alive after job assignment")
	}
	if err := signalLocalPreviewProcess(cmd, tree, true); err != nil {
		t.Fatalf("terminate preview job: %v", err)
	}
	if !waitForLocalPreviewProcessTree(context.Background(), process, time.Second) {
		t.Fatal("preview process tree remained alive after forced termination")
	}
}
