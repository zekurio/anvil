NIX_DEVELOP ?= nix develop --no-pure-eval --command

.PHONY: fmt build lint lint-fix

fmt:
	go fmt ./...

build:
	go build -o bin/anvild ./cmd/anvild
	go build -o bin/anvilctl ./cmd/anvilctl

lint:
	$(NIX_DEVELOP) golangci-lint run ./...

lint-fix:
	$(NIX_DEVELOP) golangci-lint run --fix ./...
