NIX_DEVELOP ?= nix develop --no-pure-eval --command

.PHONY: fmt test build lint lint-fix mock-library mock-smoke

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

mock-smoke:
	scripts/mock-library.sh run
