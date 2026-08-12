//go:build !darwin || !cgo

package keychain

import "errors"

func platformGetTokenImpl(string, string) (string, error) {
	return "", errors.New("macOS Security.framework is required")
}

func platformSetTokenImpl(string, string, string) error {
	return errors.New("macOS Security.framework is required")
}
