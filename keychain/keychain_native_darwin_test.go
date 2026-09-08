//go:build darwin && cgo && !uni_chat_test_keychain

package keychain

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// nativeSelftestService is a dedicated Keychain service, distinct from the
// real Service ("uni-chat" — keychain.go's Service constant), so a bug in
// this test can never read, overwrite, or delete a real user's stored token.
const nativeSelftestService = "uni-chat-selftest"

// TestNativeKeychainRoundTrip is the only test in this package — and in the
// repository — that exercises the real Security.framework cgo path
// (keychain_darwin.go's platformGetTokenImpl/platformSetTokenImpl/
// platformDeleteTokenImpl, i.e. SecItemCopyMatching/SecItemUpdate/
// SecItemAdd/SecItemDelete) directly. Every other keychain test replaces the
// platformGetToken/platformSetToken function variables with a seam and so
// never reaches this code at all — see 12-test-contract.md:32-33 ("mock
// theater запрещён") and the fix plan item this test closes.
//
// Prompt-avoidance, and its real limit (confirmed empirically, not just
// reasoned about): SecItemAdd without an explicit kSecAttrAccess grants
// access to the *creating application*, and this test avoids the specific
// failure mode the plan called out — reading back an item a *different*
// binary (an earlier run) created — by (1) using an account name unique to
// this process invocation (pid + nanosecond timestamp), so it can never
// collide with, or read back, anything a previous run left behind, and (2)
// performing set -> get -> delete entirely inside this one process before it
// exits.
//
// That does NOT make this test prompt-free on an interactive macOS session.
// `go test` links an ad-hoc-signed binary with no stable code identity
// (Team ID/certificate), so macOS cannot recognize "the same trusted app"
// across even the SAME process's own SecItemAdd — it was observed live,
// on a real logged-in session, popping the system "<binary> wants to use
// your confidential information stored in your keychain" dialog and
// blocking indefinitely until dismissed. This is why `make test-keychain-native`
// (Makefile) is HOST-ONLY, excluded from `make check`, bounded with
// `-timeout 20s` so an unattended run fails loudly instead of hanging, and
// documented as something a maintainer runs by hand, attentively, expecting
// a possible prompt — not a gate safe to run unwatched. A genuinely headless
// session (no Security Agent at all) instead gets errSecInteractionNotAllowed
// synchronously, which failLoudlyIfKeychainUnavailable below turns into a
// clear failure rather than a hang or a silent skip.
func TestNativeKeychainRoundTrip(t *testing.T) {
	account := fmt.Sprintf("selftest-%d-%d", os.Getpid(), time.Now().UnixNano())
	token := fmt.Sprintf("native-selftest-token-%d", time.Now().UnixNano())

	t.Cleanup(func() {
		if err := platformDeleteTokenImpl(nativeSelftestService, account); err != nil {
			t.Errorf("cleanup: platformDeleteTokenImpl(%q, %q) = %v", nativeSelftestService, account, err)
		}
	})

	if err := platformSetTokenImpl(nativeSelftestService, account, token); err != nil {
		failLoudlyIfKeychainUnavailable(t, err)
		t.Fatalf("platformSetTokenImpl: %v", err)
	}

	got, err := platformGetTokenImpl(nativeSelftestService, account)
	if err != nil {
		failLoudlyIfKeychainUnavailable(t, err)
		t.Fatalf("platformGetTokenImpl: %v", err)
	}
	if got != token {
		t.Fatalf("round trip token = %q, want %q", got, token)
	}

	if err := platformDeleteTokenImpl(nativeSelftestService, account); err != nil {
		t.Fatalf("platformDeleteTokenImpl: %v", err)
	}

	if _, err := platformGetTokenImpl(nativeSelftestService, account); err == nil {
		t.Fatal("platformGetTokenImpl succeeded after delete, want errSecItemNotFound")
	}
}

// failLoudlyIfKeychainUnavailable enforces 14-cross-platform-ci.md:179-183:
// "Тест не может молча пропустить платформенную ветку <...> процесс MUST
// вернуть non-zero и сообщить точную причину; skip допустим только для
// заранее проверенного N/A с rationale." A locked login Keychain or a
// headless session (no Security Agent to arbitrate access) returns
// errSecInteractionNotAllowed (OSStatus -25308) from platformGetTokenImpl/
// platformSetTokenImpl's "security.framework status %d" error text. This
// helper does not turn that into t.Skip — it still fails the test, just with
// a diagnosis of *why*, which is the "точную причину" the rule requires,
// before falling through to the caller's own generic t.Fatalf for every
// other status.
func failLoudlyIfKeychainUnavailable(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), "status -25308") {
		t.Fatalf("errSecInteractionNotAllowed (-25308): the login Keychain is locked or this is a headless/non-interactive session with no Security Agent — this is a real failure of the native macOS gate, not a skip, per 14-cross-platform-ci.md:179-183: %v", err)
	}
}
