//go:build uni_chat_test_keychain

package keychain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestKeychainRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keychain.json")
	t.Setenv("UNI_CHAT_TEST_KEYCHAIN", path)
	if err := SetToken("mattermost-token", "abc123"); err != nil {
		t.Fatal(err)
	}
	tok, err := GetToken("mattermost-token")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "abc123" {
		t.Fatalf("token = %q, want abc123", tok)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestTestKeychainFailsClosedWithoutPath(t *testing.T) {
	t.Setenv("UNI_CHAT_TEST_KEYCHAIN", "")
	getCalled := false
	setCalled := false
	origGet, origSet := platformGetToken, platformSetToken
	defer func() { platformGetToken, platformSetToken = origGet, origSet }()
	platformGetToken = func(string, string) (string, error) { getCalled = true; return "", nil }
	platformSetToken = func(string, string, string) error { setCalled = true; return nil }

	_, getErr := GetToken("mattermost-token")
	setErr := SetToken("mattermost-token", "abc123")
	if getErr == nil || !strings.Contains(getErr.Error(), "UNI_CHAT_TEST_KEYCHAIN") {
		t.Fatalf("GetToken error = %v, want controlled missing-path error", getErr)
	}
	if setErr == nil || !strings.Contains(setErr.Error(), "UNI_CHAT_TEST_KEYCHAIN") {
		t.Fatalf("SetToken error = %v, want controlled missing-path error", setErr)
	}
	if getCalled || setCalled {
		t.Fatalf("native backend called: get=%v set=%v", getCalled, setCalled)
	}
}
