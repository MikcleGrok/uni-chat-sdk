//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package protocol

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const childTerminationGrace = 500 * time.Millisecond

func configureProcessGroup(_ *exec.Cmd) {}

func runBoundedCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Signal(os.Interrupt)
		timer := time.NewTimer(childTerminationGrace)
		defer timer.Stop()
		select {
		case err := <-done:
			return fmt.Errorf("engine timeout after %s; sent interrupt: %w", timeout, err)
		case <-timer.C:
			_ = cmd.Process.Kill()
			return fmt.Errorf("engine timeout after %s; sent interrupt, then kill after %s: %w", timeout, childTerminationGrace, <-done)
		}
	}
}
