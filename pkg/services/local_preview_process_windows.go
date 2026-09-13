//go:build windows

package services

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// localPreviewProcessTree is a Windows Job Object. Closing the job kills all
// remaining processes assigned to it, including package-manager wrappers and
// generator children.
type localPreviewProcessTree struct {
	mu     sync.Mutex
	job    windows.Handle
	closed bool
}

func configureLocalPreviewCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Keep the process suspended until it is assigned to the job. This closes
	// the race where a wrapper can spawn a child before job assignment.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.WaitDelay = localPreviewProcessWaitDelay
}

func attachLocalPreviewProcessTree(cmd *exec.Cmd) (*localPreviewProcessTree, error) {
	if cmd == nil || cmd.Process == nil {
		return &localPreviewProcessTree{}, nil
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	tree := &localPreviewProcessTree{job: job}
	closeOnError := func(err error) (*localPreviewProcessTree, error) {
		_ = closeLocalPreviewProcessTree(tree)
		return nil, err
	}

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return closeOnError(fmt.Errorf("configure job object: %w", err))
	}

	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return closeOnError(fmt.Errorf("open preview process for job assignment: %w", err))
	}
	assignErr := windows.AssignProcessToJobObject(job, processHandle)
	_ = windows.CloseHandle(processHandle)
	if assignErr != nil {
		return closeOnError(fmt.Errorf("assign preview process to job: %w", assignErr))
	}
	if err := resumeLocalPreviewProcess(cmd.Process.Pid); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		return closeOnError(fmt.Errorf("resume preview process after job assignment: %w", err))
	}
	return tree, nil
}

func resumeLocalPreviewProcess(processID int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot process threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("enumerate process threads: %w", err)
	}
	resumed := false
	for {
		if entry.OwnerProcessID == uint32(processID) {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return fmt.Errorf("open suspended preview thread: %w", err)
			}
			previousCount, resumeErr := windows.ResumeThread(thread)
			_ = windows.CloseHandle(thread)
			if resumeErr != nil {
				return fmt.Errorf("resume preview thread: %w", resumeErr)
			}
			if previousCount == ^uint32(0) {
				return errors.New("resume preview thread failed")
			}
			resumed = true
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			break
		}
	}
	if !resumed {
		return errors.New("preview process has no resumable thread")
	}
	return nil
}

func closeLocalPreviewProcessTree(tree *localPreviewProcessTree) error {
	if tree == nil {
		return nil
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.closed {
		return nil
	}
	tree.closed = true
	if tree.job == 0 {
		return nil
	}
	err := windows.CloseHandle(tree.job)
	tree.job = 0
	return err
}

func signalLocalPreviewProcess(cmd *exec.Cmd, tree *localPreviewProcessTree, force bool) error {
	if force && tree != nil {
		tree.mu.Lock()
		job := tree.job
		closed := tree.closed
		tree.mu.Unlock()
		if !closed && job != 0 {
			return windows.TerminateJobObject(job, 1)
		}
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func localPreviewProcessTreeAlive(cmd *exec.Cmd, tree *localPreviewProcessTree) bool {
	if tree == nil {
		return cmd != nil && cmd.Process != nil && (cmd.ProcessState == nil || !cmd.ProcessState.Exited())
	}
	tree.mu.Lock()
	job := tree.job
	closed := tree.closed
	tree.mu.Unlock()
	if closed || job == 0 {
		return false
	}
	var info windowsJobBasicAccountingInformation
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err == nil && info.ActiveProcesses > 0 {
		return true
	}
	// A newly assigned job can briefly report zero active processes while the
	// suspended wrapper is being resumed. The parent state is a safe fallback;
	// once it exits, a surviving child is still detected by the job accounting.
	return cmd != nil && cmd.Process != nil && (cmd.ProcessState == nil || !cmd.ProcessState.Exited())
}

func localPreviewProcessDescription(cmd *exec.Cmd, tree *localPreviewProcessTree) string {
	if cmd == nil || cmd.Process == nil {
		return "pid=unknown job=unknown"
	}
	job := "closed"
	if tree != nil {
		tree.mu.Lock()
		if !tree.closed && tree.job != 0 {
			job = fmt.Sprintf("%d", tree.job)
		}
		tree.mu.Unlock()
	}
	return fmt.Sprintf("pid=%d job=%s", cmd.Process.Pid, job)
}

// The x/sys/windows package does not expose this small accounting structure.
// Its layout is fixed by the Windows API and is valid on 32- and 64-bit builds.
type windowsJobBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}
