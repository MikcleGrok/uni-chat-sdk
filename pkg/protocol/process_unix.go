//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package protocol

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const childTerminationGrace = 500 * time.Millisecond

func configureProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

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
		if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("engine timeout after %s; failed to send SIGTERM: %w", timeout, err)
		}
		timer := time.NewTimer(childTerminationGrace)
		defer timer.Stop()
		select {
		case err := <-done:
			return fmt.Errorf("engine timeout after %s; sent SIGTERM: %w", timeout, err)
		case <-timer.C:
			if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("engine timeout after %s; failed to send SIGKILL: %w", timeout, err)
			}
			return fmt.Errorf("engine timeout after %s; sent SIGTERM, then SIGKILL after %s: %w", timeout, childTerminationGrace, <-done)
		}
	}
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, signal)
}
