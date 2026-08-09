//go:build !uni_chat_test_keychain

package keychain

func testToken(string) (string, bool, error) { return "", false, nil }

func setTestToken(string, string) (bool, error) { return false, nil }
