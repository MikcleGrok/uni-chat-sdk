.PHONY: format lint vet test race coverage secrets-check dependency-check check

format:
	@test -z "$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"

lint:
	@if command -v staticcheck >/dev/null; then staticcheck ./...; else echo 'staticcheck is required for lint' >&2; exit 1; fi

vet:
	@go vet ./...

test:
	@go test -tags uni_chat_test_keychain -count=1 ./...

race:
	@go test -tags uni_chat_test_keychain -race -count=1 ./...

coverage:
	@go test -tags uni_chat_test_keychain -coverprofile=coverage.out ./...

secrets-check:
	@! git grep -nE '(COSIGN_PRIVATE_KEY[[:space:]]*=|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|ghp_[A-Za-z0-9]+)' -- . ':!go.sum'

dependency-check:
	@if command -v govulncheck >/dev/null; then govulncheck ./...; else echo 'govulncheck unavailable' >&2; exit 1; fi

check: format lint vet test race coverage secrets-check dependency-check
