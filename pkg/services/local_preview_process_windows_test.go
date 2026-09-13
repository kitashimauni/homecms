//go:build windows

package services

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"
)

func startWindowsPreviewCommand(cmd *exec.Cmd) (*exec.Cmd, *localPreviewProcessTree, *managedLocalPreviewProcess, error) {
	configureLocalPreviewCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	tree, err := attachLocalPreviewProcessTree(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, nil, err
	}
	process := &managedLocalPreviewProcess{
		cmd:  cmd,
		tree: tree,
		done: make(chan struct{}),
	}
	go func() {
		process.setWaitErr(cmd.Wait())
		close(process.done)
	}()
	return cmd, tree, process, nil
}

func startWindowsPreviewProcess(command string) (*exec.Cmd, *localPreviewProcessTree, *managedLocalPreviewProcess, error) {
	return startWindowsPreviewCommand(exec.Command("cmd.exe", "/c", command))
}

func stopWindowsPreviewProcess(t *testing.T, cmd *exec.Cmd, tree *localPreviewProcessTree, process *managedLocalPreviewProcess) {
	t.Helper()
	defer closeLocalPreviewProcessTree(tree)
	if err := signalLocalPreviewProcess(cmd, tree, true); err != nil {
		t.Fatalf("terminate preview job: %v", err)
	}
	if !waitForLocalPreviewProcessTree(context.Background(), process, time.Second) {
		t.Fatal("preview process tree remained alive after forced termination")
	}
}

func TestWindowsJobAccountingInformationLayout(t *testing.T) {
	if size := unsafe.Sizeof(windowsJobBasicAccountingInformation{}); size != 48 {
		t.Fatalf("JOBOBJECT_BASIC_ACCOUNTING_INFORMATION size = %d, want 48", size)
	}
}

func TestWindowsLocalPreviewProcessTreeTerminatesWrapperChildren(t *testing.T) {
	cmd, tree, process, err := startWindowsPreviewProcess("ping 127.0.0.1 -n 30 > nul")
	if err != nil {
		t.Fatalf("start preview wrapper: %v", err)
	}
	if !localPreviewProcessTreeAlive(cmd, tree) {
		stopWindowsPreviewProcess(t, cmd, tree, process)
		t.Fatal("preview process tree was not alive after job assignment")
	}
	stopWindowsPreviewProcess(t, cmd, tree, process)
}

func TestWindowsLocalPreviewProcessTreeKeepsChildAliveAfterWrapperExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestWindowsLocalPreviewProcessTreeParentHelper")
	cmd.Env = append(os.Environ(), "HOMECMS_WINDOWS_PROCESS_PARENT=1")
	cmd, tree, process, err := startWindowsPreviewCommand(cmd)
	if err != nil {
		t.Fatalf("start preview wrapper: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		stopWindowsPreviewProcess(t, cmd, tree, process)
		t.Fatal("preview wrapper did not exit before timeout")
	}
	if !localPreviewProcessTreeAlive(cmd, tree) {
		stopWindowsPreviewProcess(t, cmd, tree, process)
		t.Fatal("child process was not observed after wrapper exit")
	}
	stopWindowsPreviewProcess(t, cmd, tree, process)
}

func TestWindowsLocalPreviewProcessTreeParentHelper(t *testing.T) {
	if os.Getenv("HOMECMS_WINDOWS_PROCESS_PARENT") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=TestWindowsLocalPreviewProcessTreeChildHelper")
	child.Env = append(os.Environ(), "HOMECMS_WINDOWS_PROCESS_CHILD=1")
	if err := child.Start(); err != nil {
		t.Fatalf("start child helper: %v", err)
	}
}

func TestWindowsLocalPreviewProcessTreeChildHelper(t *testing.T) {
	if os.Getenv("HOMECMS_WINDOWS_PROCESS_CHILD") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}
