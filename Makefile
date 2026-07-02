NIX_DEVELOP ?= nix develop --no-pure-eval --command

.PHONY: fmt test build lint lint-fix mock-library mock-config-check mock-smoke

fmt:
	go fmt ./...

test:
	go test ./...

build:
	go build -o bin/anvild ./cmd/anvild

lint:
	$(NIX_DEVELOP) golangci-lint run ./...

lint-fix:
	$(NIX_DEVELOP) golangci-lint run --fix ./...

mock-library:
	scripts/mock-library.sh setup

mock-config-check:
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	scripts/mock-library.sh config "$$tmp" >/dev/null; \
	go run ./cmd/anvild check-config --config "$$tmp/config.toml"

mock-smoke:
	scripts/mock-library.sh run
