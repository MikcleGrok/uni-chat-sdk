// Package keychain stores and reads a per-engine personal access token in the
// macOS Keychain (service "uni-chat", account "<engine>-token", passed by caller).
package keychain

import (
	"fmt"
	"os/exec"
	"strings"
)

// Service is the Keychain generic-password service name shared by all engines;
// the account is per-engine ("<engine>-token"), passed by the caller.
const Service = "uni-chat"

// run is the exec seam over the `security` CLI, overridden in tests so they
// never touch the real Keychain.
var run = func(args ...string) ([]byte, error) {
	return exec.Command("security", args...).Output() // #nosec G204 -- the executable is fixed to Apple's Keychain CLI.
}

var runWithInput = func(input string, args ...string) ([]byte, error) {
	command := exec.Command("security", args...) // #nosec G204 -- the executable is fixed to Apple's Keychain CLI.
	command.Stdin = strings.NewReader(input)
	return command.Output()
}

// GetToken reads the stored token for account (e.g. "mattermost-token").
func GetToken(account string) (string, error) {
	if token, ok, err := testToken(account); ok {
		return token, err
	}
	out, err := run("find-generic-password", "-s", Service, "-a", account, "-w")
	if err != nil {
		msg := "no token in Keychain (service=%s account=%s) — run: uni-chatd configure <engine>: %w"
		return "", fmt.Errorf(msg, Service, account, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// SetToken stores (or overwrites, -U) the token for account.
//
// The final -w asks security to read the password from its input stream, keeping
// the token out of the child process argv and avoiding temporary files.
func SetToken(account, token string) error {
	if ok, err := setTestToken(account, token); ok {
		return err
	}
	if _, err := runWithInput(token+"\n"+token+"\n", "add-generic-password", "-s", Service, "-a", account, "-U", "-w"); err != nil {
		return fmt.Errorf("keychain write (service=%s account=%s): %w", Service, account, err)
	}
	return nil
}
