package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	localPreviewProcessGracePeriod = 2 * time.Second
	localPreviewProcessKillWait    = 2 * time.Second
	localPreviewProcessWaitDelay   = 500 * time.Millisecond
)

// terminateManagedLocalPreviewProcess stops the generator process tree before
// callers release the lifecycle slot. The command context is cancelled only
// after the tree has been signalled and Wait has observed termination; doing
// it earlier would kill only the package-manager parent and strand children.
func terminateManagedLocalPreviewProcess(ctx context.Context, siteID string, process *managedLocalPreviewProcess) error {
	if process == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if process.exited() && !localPreviewProcessTreeAlive(process.cmd, process.tree) {
		cancelProcessContext(process)
		return nil
	}

	// Tests and startup races may provide a synthetic process without an OS
	// process. Preserve the existing cancellation contract for that case.
	if process.cmd == nil || process.cmd.Process == nil {
		cancelProcessContext(process)
		select {
		case <-process.done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("stopping local preview for site %q: %w", siteID, ctx.Err())
		}
	}

	gracefulErr := signalLocalPreviewProcess(process.cmd, process.tree, false)
	if gracefulErr != nil && !process.exited() {
		slog.Warn("Failed to send graceful Local Live Preview stop signal",
			"site", siteID,
			"process", localPreviewProcessDescription(process.cmd, process.tree),
			"error", gracefulErr,
		)
	}

	if waitForLocalPreviewProcessTree(ctx, process, localPreviewProcessGracePeriod) {
		cancelProcessContext(process)
		return nil
	}

	forceErr := signalLocalPreviewProcess(process.cmd, process.tree, true)
	if forceErr != nil && !process.exited() {
		slog.Warn("Failed to send forced Local Live Preview stop signal",
			"site", siteID,
			"process", localPreviewProcessDescription(process.cmd, process.tree),
			"error", forceErr,
		)
	}

	if waitForLocalPreviewProcessTree(context.Background(), process, localPreviewProcessKillWait) {
		cancelProcessContext(process)
		return nil
	}

	cancelProcessContext(process)
	description := localPreviewProcessDescription(process.cmd, process.tree)
	if forceErr != nil {
		err := fmt.Errorf("local preview process tree did not terminate for site %q (%s): %w", siteID, description, forceErr)
		slog.Error("Local preview process tree termination timed out", "site", siteID, "process", description, "error", err)
		return err
	}
	err := fmt.Errorf("local preview process tree did not terminate for site %q (%s)", siteID, description)
	slog.Error("Local preview process tree termination timed out", "site", siteID, "process", description, "error", err)
	return err
}

func waitForLocalPreviewProcessTree(ctx context.Context, process *managedLocalPreviewProcess, timeout time.Duration) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return process.exited() && !localPreviewProcessTreeAlive(process.cmd, process.tree)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if process.exited() && !localPreviewProcessTreeAlive(process.cmd, process.tree) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return process.exited() && !localPreviewProcessTreeAlive(process.cmd, process.tree)
		case <-ticker.C:
		}
	}
}

func cancelProcessContext(process *managedLocalPreviewProcess) {
	if process != nil && process.cancel != nil {
		process.cancel()
	}
}
