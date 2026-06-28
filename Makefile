.PHONY: fmt test build mock-library mock-smoke

fmt:
	go fmt ./...

test:
	go test ./...

build:
	go build -o bin/anvild ./cmd/anvild

mock-library:
	scripts/mock-library.sh setup

mock-smoke:
	scripts/mock-library.sh run
