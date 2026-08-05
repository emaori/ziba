BINARY  := ziba
PKG     := github.com/emaori/ziba
VERSION ?= $(shell date +%Y%m%d)-dev

# Which Docker engine to talk to. There are two, and they are not
# interchangeable:
#
#   desktop-linux  local Docker Desktop — development. Published ports land on
#                  this machine, so nothing else is needed to reach them.
#   homeserver     the deployment target, reached over SSH. Published ports land
#                  on the homeserver's loopback, so reaching them from here
#                  needs `make db-tunnel`.
#
# Development defaults to local: a stray `make down` should never take out the
# real deployment. Target the homeserver explicitly, e.g.
#   make up DOCKER_CONTEXT=homeserver
DOCKER_CONTEXT ?= desktop-linux
COMPOSE        := docker --context $(DOCKER_CONTEXT) compose

SSH_HOST ?= homeserver
DB_PORT  ?= 5432

.PHONY: build run test fmt vet tidy check clean \
        dev up down restart ps logs db-psql db-tunnel migrate collect process

## build: compile the binary into bin/
build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/$(BINARY)

## run: build and run, e.g. make run ARGS=version
run:
	go run ./cmd/$(BINARY) $(ARGS)

## test: run the test suite
test:
	go test ./...

## fmt: format all Go files
fmt:
	go fmt ./...

## vet: report suspicious constructs
vet:
	go vet ./...

## tidy: sync go.mod with the imports actually used
tidy:
	go mod tidy

## check: what should pass before a commit
check: fmt vet test

## clean: remove build output
clean:
	rm -rf bin/

## dev: bring the whole local stack up and migrate — one command to start working
dev: up migrate

## up: start every service and wait until each reports healthy
up: .env
	$(COMPOSE) up -d --wait

## down: stop every service, keeping the data volumes
down:
	$(COMPOSE) down

## restart: recreate the services, picking up compose.yaml changes
restart: down up

## ps: show what is running
ps:
	$(COMPOSE) ps

## logs: follow the logs of every service
logs:
	$(COMPOSE) logs -f

## db-psql: open a psql shell inside the database container
db-psql:
	$(COMPOSE) exec ziba-db psql -U $${POSTGRES_USER:-ziba} -d ziba

# Compose refuses to start without the variables in .env, and its own error
# does not say where they come from. Fail here instead, with the fix.
.env:
	@echo "no .env found — copy it and set a password:" >&2
	@echo "    cp .env.example .env" >&2
	@exit 1

## db-tunnel: forward the homeserver database to localhost (run in its own terminal)
db-tunnel:
	@echo "forwarding localhost:$(DB_PORT) -> $(SSH_HOST):$(DB_PORT) — Ctrl-C to stop"
	ssh -N -o ExitOnForwardFailure=yes -L $(DB_PORT):127.0.0.1:$(DB_PORT) $(SSH_HOST)

## migrate: apply pending migrations, reading credentials from .env
migrate: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) migrate

## process: run the AI pipeline, e.g. make process ARGS=-offline
process: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) process $(ARGS)

## collect: read every enabled source, e.g. make collect ARGS=-no-fetch
collect: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) collect $(ARGS)
