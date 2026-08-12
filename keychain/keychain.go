// Package keychain stores and reads a per-engine personal access token in the
// macOS Keychain (service "uni-chat", account "<engine>-token", passed by caller).
package keychain

import (
	"fmt"
	"strings"
)

// Service is the Keychain generic-password service name shared by all engines;
// the account is per-engine ("<engine>-token"), passed by the caller.
const Service = "uni-chat"

var platformGetToken = platformGetTokenImpl
var platformSetToken = platformSetTokenImpl

// GetToken reads the stored token for account (e.g. "mattermost-token").
func GetToken(account string) (string, error) {
	if token, ok, err := testToken(account); ok {
		return token, err
	}
	out, err := platformGetToken(Service, account)
	if err != nil {
		return "", fmt.Errorf("no token in Keychain (service=%s account=%s): %w", Service, account, err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// SetToken stores (or overwrites, -U) the token for account.
func SetToken(account, token string) error {
	if ok, err := setTestToken(account, token); ok {
		return err
	}
	if err := platformSetToken(Service, account, token); err != nil {
		return fmt.Errorf("keychain write (service=%s account=%s): %w", Service, account, err)
	}
	return nil
}
