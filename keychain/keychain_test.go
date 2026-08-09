package keychain

import (
	"errors"
	"strings"
	"testing"
)

func TestGetToken(t *testing.T) {
	var gotArgs []string
	orig := run
	defer func() { run = orig }()
	run = func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("s3cr3t\n"), nil
	}

	tok, err := GetToken("mattermost-token")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "s3cr3t" {
		t.Fatalf("token = %q, want trimmed s3cr3t", tok)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "find-generic-password") || !strings.Contains(joined, Service) || !strings.Contains(joined, "mattermost-token") {
		t.Fatalf("args = %v", gotArgs)
	}
}

func TestGetTokenMissing(t *testing.T) {
	orig := run
	defer func() { run = orig }()
	run = func(args ...string) ([]byte, error) { return nil, errors.New("SecKeychainSearchCopyNext: not found") }

	if _, err := GetToken("mattermost-token"); err == nil {
		t.Fatal("want error when the token is absent")
	}
}

func TestSetToken(t *testing.T) {
	var gotArgs []string
	var gotInput string
	orig := run
	origInput := runWithInput
	defer func() { run = orig }()
	defer func() { runWithInput = origInput }()
	runWithInput = func(input string, args ...string) ([]byte, error) {
		gotArgs = args
		gotInput = input
		return nil, nil
	}

	if err := SetToken("mattermost-token", "abc123"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "add-generic-password") || !strings.Contains(joined, "-U") || !strings.Contains(joined, "-w") || strings.Contains(joined, "abc123") || !strings.Contains(joined, "mattermost-token") {
		t.Fatalf("args = %v", gotArgs)
	}
	if gotInput != "abc123\n" {
		t.Fatalf("stdin = %q, want token followed by newline", gotInput)
	}
}
