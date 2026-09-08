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
	t.Parallel()
	// 10s, not 1s: this re-exec's a fresh copy of the test binary itself, and
	// under `make race` (a -race-instrumented binary, heavier to spawn than a
	// plain build) with all ~25 acceptance tests now running in parallel and
	// contending for CPU, a 1s budget consistently timed out on this exact
	// call before the child even finished starting — confirmed live, 100% of
	// `make race` runs, not a rare flake. This test's purpose is proving the
	// round trip completes without hitting CallStdio's timeout path at all,
	// not proving a specific latency bound; matches the 10s budget
	// TestCallStdioRoundTrip already uses for the equivalent normal-path
	// round trip via the other (TestEngineHelperProcess) helper.
	resp, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "normal"}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Error != "" {
		t.Fatalf("response=%+v, want successful response without an error", resp)
	}
}

func TestCallStdioTimeoutReportsSignalSequenceAndIsBounded(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "descendant.pid")
	sibling := startLifecycleHelper(t, "sibling", filepath.Join(dir, "sibling.pid"))
	defer stopLifecycleHelper(t, sibling)
	waitForFile(t, filepath.Join(dir, "sibling.pid"))
	// 300ms, not 50ms: this path is a *double* process spawn — CallStdio's
	// top-level helper, which itself then exec's the "descendant" — and the
	// descendant must reach its own os.WriteFile(marker) before the parent's
	// timeout fires, or readPID below finds nothing. Confirmed live: 50ms
	// was reliable sequentially but consistently lost that race under
	// `make race` (a -race-instrumented binary is markedly slower to spawn)
	// combined with ~25 other acceptance tests contending for CPU in
	// parallel. 300ms still keeps the whole test well under the 2s-class
	// bounds its sibling tests assert, while giving the double spawn real
	// headroom.
	_, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "spawn-descendant", Args: mustJSON(t, marker)}, 300*time.Millisecond)
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
	t.Parallel()
	// 10s for the same reason as TestCallStdioReturnsNormallyBeforeDeadline:
	// this assertion explicitly requires the error NOT to mention "engine
	// timeout", so a too-tight budget racing slow -race/parallel subprocess
	// startup would corrupt this test the same way it did that one.
	_, err := CallStdio(os.Args[0], []string{"-test.run=TestCallStdioHelperProcess", "--"}, Request{Cmd: "self-kill"}, 10*time.Second)
	if err == nil || strings.Contains(err.Error(), "engine timeout") || !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("err=%v, want child SIGKILL without timeout attribution", err)
	}
}

// isCallStdioHelperInvocation reports whether this process was re-exec'd as
// the TestCallStdioHelperProcess helper (as opposed to being the outer/main
// `go test` run, which also links this same binary).
func isCallStdioHelperInvocation(args []string) bool {
	return strings.Contains(strings.Join(args, " "), "-test.run=TestCallStdioHelperProcess")
}

// helperSigTermCh is registered in init() — before flag.Parse() or any test
// dispatch — specifically so that TestCallStdioTimeoutReportsSignalSequenceAndIsBounded's
// and TestCallStdioTimeoutKillsDescendantOnlyWithinOwnGroup's 50ms parent
// timeout cannot race this process's default SIGTERM disposition
// (terminate) against however long `go test`'s own startup and test-dispatch
// overhead takes to reach the actual test function body. Confirmed live:
// registering inside the test function body (i.e. after dispatch) was
// racy — a `-count=5` repeat run flaked with "sent SIGTERM: signal:
// terminated" (no SIGKILL) roughly one run in five, because the default
// SIGTERM disposition (still armed until Notify is registered) sometimes won
// the race against go test's own dispatch overhead under parallel load.
// Registering in init() moves that race window from "however long test
// dispatch takes" down to just OS fork/exec + Go runtime bootstrap, which is
// consistently well under the 50ms budget. This is scoped to actual helper
// invocations only (checked directly against os.Args, fully populated
// before init() runs) — never for the outer/main test binary run, so it
// does not disable a CI's own graceful SIGTERM-based stop of the real test
// suite.
var helperSigTermCh chan os.Signal

func init() {
	if isCallStdioHelperInvocation(os.Args) {
		helperSigTermCh = make(chan os.Signal, 1)
		signal.Notify(helperSigTermCh, syscall.SIGTERM)
	}
}

// blockIgnoringSIGTERM makes the calling process immune to SIGTERM — the
// same intent as signal.Ignore(syscall.SIGTERM) — while staying reachable by
// the Go runtime's own deadlock detector as "could still be woken by an
// external event". signal.Ignore alone strips the process of any
// runtime-visible pending wakeup source, so a subsequent `select {}` (this
// package's original pattern) is indistinguishable, to the runtime, from a
// genuine total deadlock: with nothing else running, the process crashes
// itself with "fatal error: all goroutines are asleep - deadlock!" (exit 2)
// before the real external SIGTERM/SIGKILL sequence these tests exist to
// exercise ever arrives. Routing the signal through signal.Notify instead
// keeps a live channel the runtime credits as a genuine wakeup source — the
// same reason an ordinary `signal.Notify(ch); <-ch` server main loop never
// trips the detector — so the process blocks exactly as intended until an
// unblockable SIGKILL ends it. (Confirmed live: every //go:build
// aix||darwin||... call site below crashed with exactly that fatal error
// before this fix, because process_test_unix.go's filename — now
// process_unix_test.go — did not end in _test.go, so `go test` never
// compiled any of these functions as tests at all; see the fix history for
// TestCallStdioHelperProcess and its three callers.)
func blockIgnoringSIGTERM() {
	if helperSigTermCh == nil {
		// Unreachable from the call sites below (all gated on
		// isCallStdioHelperInvocation already true in init()), but never
		// silently hang on a nil channel if that invariant ever breaks.
		helperSigTermCh = make(chan os.Signal, 1)
		signal.Notify(helperSigTermCh, syscall.SIGTERM)
	}
	for range helperSigTermCh {
	}
}

func TestCallStdioHelperProcess(t *testing.T) {
	t.Parallel()
	if !isCallStdioHelperInvocation(os.Args) {
		return
	}
	if mode := os.Getenv("UC_HELPER_MODE"); mode == "descendant" || mode == "sibling" {
		if err := os.WriteFile(os.Getenv("UC_HELPER_MARKER"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
			return
		}
		blockIgnoringSIGTERM()
	}
	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		return
	}
	switch req.Cmd {
	case "normal":
		_, _ = fmt.Fprintln(os.Stdout, `{"ok":true}`)
		// Exit now, bypassing `go test`'s own end-of-run summary (e.g.
		// "PASS\nok  \t...") that would otherwise print to this same stdout
		// right after the JSON line above and corrupt it from the parent
		// CallStdio's point of view (it decodes exactly one JSON value from
		// this process's stdout) — the same os.Exit(0) TestEngineHelperProcess
		// in protocol_test.go already uses for the identical reason.
		os.Exit(0)
	case "ignore-term":
		// Must actually survive the parent's SIGTERM so its timeout logic is
		// forced to escalate to SIGKILL — see TestCallStdioTimeoutReportsSignalSequenceAndIsBounded's
		// own assertion that the error names both signals.
		blockIgnoringSIGTERM()
	case "spawn-descendant":
		var marker string
		if err := json.Unmarshal(req.Args, &marker); err != nil {
			return
		}
		child := exec.Command(os.Args[0], "-test.run=TestCallStdioHelperProcess", "--")
		child.Env = append(os.Environ(), "UC_HELPER_MODE=descendant", "UC_HELPER_MARKER="+marker)
		_ = child.Start()
		// This top-level process (the one CallStdio's process-group signal
		// targets directly) must also survive SIGTERM so cmd.Wait() on it
		// does not return until the follow-up SIGKILL —
		// TestCallStdioTimeoutKillsDescendantOnlyWithinOwnGroup asserts the
		// error names SIGKILL, which only fires once the initial SIGTERM
		// alone failed to end the process within childTerminationGrace.
		blockIgnoringSIGTERM()
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
