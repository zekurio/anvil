NIX_DEVELOP ?= nix develop --no-pure-eval --command

# npm rewrites this file on every install, so it doubles as the stamp that
# says node_modules matches the lockfile.
WEB_DEPS := web/node_modules/.package-lock.json

.PHONY: fmt build lint lint-fix all web web-check web-dev web-mock clean

fmt:
	go fmt ./...

# Needs no Node. Without a frontend build the daemon embeds only the
# web/dist placeholder and serves the API alone.
build:
	go build -o bin/anvild ./cmd/anvild
	go build -o bin/anvilctl ./cmd/anvilctl

lint:
	$(NIX_DEVELOP) golangci-lint run ./...

lint-fix:
	$(NIX_DEVELOP) golangci-lint run --fix ./...

# Binaries with the web UI embedded. build runs as a sub-make so that it
# cannot start before the frontend is in web/dist, even under -j.
all: web
	$(MAKE) build

$(WEB_DEPS): web/package-lock.json
	cd web && npm ci
	touch $@

web: $(WEB_DEPS)
	cd web && npm run build

web-check: $(WEB_DEPS)
	cd web && npm run typecheck
	cd web && npm run build

# Vite dev server that proxies /api to a running daemon. ANVIL_API overrides
# the default http://127.0.0.1:8484.
web-dev: $(WEB_DEPS)
	cd web && npm run dev

# Vite dev server with the in-memory mock API. No daemon needed.
web-mock: $(WEB_DEPS)
	cd web && npm run dev:mock

clean:
	rm -rf bin
	find web/dist -mindepth 1 ! -name .gitkeep -delete
