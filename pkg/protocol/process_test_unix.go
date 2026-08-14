//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCallStdioReturnsNormallyBeforeDeadline(t *testing.T) {
	resp, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "normal"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Error != "" {
		t.Fatalf("response=%+v, want successful response without an error", resp)
	}
}

func TestCallStdioTimeoutReportsSignalSequenceAndIsBounded(t *testing.T) {
	started := time.Now()
	_, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "ignore-term"}, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "engine timeout") || !strings.Contains(err.Error(), "SIGTERM") || !strings.Contains(err.Error(), "SIGKILL") {
		t.Fatalf("err=%v, want timeout with SIGTERM then SIGKILL diagnostics", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout cleanup took %v", elapsed)
	}
}

func TestCallStdioTimeoutKillsDescendantOnlyWithinOwnGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "descendant.pid")
	sibling := startLifecycleHelper(t, "sibling", filepath.Join(dir, "sibling.pid"))
	defer stopLifecycleHelper(t, sibling)
	waitForFile(t, filepath.Join(dir, "sibling.pid"))
	_, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "spawn-descendant", Args: mustJSON(t, marker)}, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "SIGKILL") {
		t.Fatalf("err=%v, want bounded group kill", err)
	}
	pid := readPID(t, marker)
	waitForProcessExit(t, pid)
	if err := syscall.Kill(sibling.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated sibling was killed: %v", err)
	}
}

func TestCallStdioChildSIGKILLIsNotReportedAsParentTimeout(t *testing.T) {
	_, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "self-kill"}, time.Second)
	if err == nil || strings.Contains(err.Error(), "engine timeout") || !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("err=%v, want child SIGKILL without timeout attribution", err)
	}
}

func TestCallStdioHelperProcess(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "-test.run=TestCallStdioHelperProcess") {
		return
	}
	if mode := os.Getenv("UC_HELPER_MODE"); mode == "descendant" || mode == "sibling" {
		if err := os.WriteFile(os.Getenv("UC_HELPER_MARKER"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
			return
		}
		signal.Ignore(syscall.SIGTERM)
		select {}
	}
	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		return
	}
	switch req.Cmd {
	case "normal":
		_, _ = fmt.Fprintln(os.Stdout, `{"ok":true}`)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		select {}
	case "spawn-descendant":
		var marker string
		if err := json.Unmarshal(req.Args, &marker); err != nil {
			return
		}
		child := exec.Command(os.Args[0], "-test.run=TestCallStdioHelperProcess", "--")
		child.Env = append(os.Environ(), "UC_HELPER_MODE=descendant", "UC_HELPER_MARKER="+marker)
		_ = child.Start()
		select {}
	case "self-kill":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
}

func startLifecycleHelper(t *testing.T, mode, marker string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestCallStdioHelperProcess", "--")
	cmd.Env = append(os.Environ(), "UC_HELPER_MODE="+mode, "UC_HELPER_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func stopLifecycleHelper(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		<-ticker.C
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		<-ticker.C
	}
	t.Fatalf("process %d is still running", pid)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(bytes.TrimSpace(data)), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
