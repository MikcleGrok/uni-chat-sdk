//go:build uni_chat_test_keychain

package keychain

import (
	"encoding/json"
	"fmt"
	"os"
)

func testToken(account string) (string, bool, error) {
	path := os.Getenv("UNI_CHAT_TEST_KEYCHAIN")
	if path == "" {
		return "", true, fmt.Errorf("test keychain is disabled: UNI_CHAT_TEST_KEYCHAIN must point to a disposable file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", true, err
	}
	var tokens map[string]string
	if err := json.Unmarshal(b, &tokens); err != nil {
		return "", true, fmt.Errorf("test keychain decode: %w", err)
	}
	token, ok := tokens[account]
	if !ok {
		return "", true, fmt.Errorf("test keychain has no account %s", account)
	}
	return token, true, nil
}

func setTestToken(account, token string) (bool, error) {
	path := os.Getenv("UNI_CHAT_TEST_KEYCHAIN")
	if path == "" {
		return true, fmt.Errorf("test keychain is disabled: UNI_CHAT_TEST_KEYCHAIN must point to a disposable file")
	}
	tokens := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &tokens); err != nil {
			return true, fmt.Errorf("test keychain decode: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return true, err
	}
	tokens[account] = token
	b, err := json.Marshal(tokens)
	if err != nil {
		return true, err
	}
	return true, os.WriteFile(path, b, 0o600)
}
