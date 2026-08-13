//go:build !uni_chat_test_keychain

package keychain

import (
	"errors"
	"testing"
)

func TestGetToken(t *testing.T) {
	orig := platformGetToken
	defer func() { platformGetToken = orig }()
	platformGetToken = func(service, account string) (string, error) {
		if service != Service || account != "mattermost-token" {
			t.Fatalf("boundary = (%q, %q)", service, account)
		}
		return "s3cr3t\n", nil
	}

	tok, err := GetToken("mattermost-token")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "s3cr3t" {
		t.Fatalf("token = %q, want trimmed s3cr3t", tok)
	}
}

func TestGetTokenMissing(t *testing.T) {
	orig := platformGetToken
	defer func() { platformGetToken = orig }()
	platformGetToken = func(string, string) (string, error) { return "", errors.New("not found") }

	if _, err := GetToken("mattermost-token"); err == nil {
		t.Fatal("want error when the token is absent")
	}
}

func TestSetToken(t *testing.T) {
	orig := platformSetToken
	defer func() { platformSetToken = orig }()
	var gotService, gotAccount, gotToken string
	platformSetToken = func(service, account, token string) error {
		gotService, gotAccount, gotToken = service, account, token
		return nil
	}

	if err := SetToken("mattermost-token", "abc123"); err != nil {
		t.Fatal(err)
	}
	if gotService != Service || gotAccount != "mattermost-token" || gotToken != "abc123" {
		t.Fatalf("boundary = (%q, %q, %q)", gotService, gotAccount, gotToken)
	}
}
