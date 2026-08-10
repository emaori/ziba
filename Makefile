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

.PHONY: build run test test-integration fmt vet tidy check clean \
        capture dryrun dev image deploy up down restart ps logs app-logs db-psql db-tunnel migrate collect process digest run-once serve

## build: compile the binary into bin/
build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/$(BINARY)

## run: build and run, e.g. make run ARGS=version
run:
	go run ./cmd/$(BINARY) $(ARGS)

## test: run the test suite
## Store and job tests need PostgreSQL; they skip themselves when it is absent,
## so .env is sourced to give them the address when there is one.
test:
	set -a; [ -f ./.env ] && . ./.env; set +a; go test ./...

## fmt: format all Go files
fmt:
	go fmt ./...

## vet: report suspicious constructs
vet:
	go vet ./...
# Tagged tests are invisible to the line above, so they rot unnoticed: the
# integration suite had been failing to compile for a day before anyone looked.
# This compiles them without running them — running needs a database and the
# network, which `make test-integration` is for.
	go vet -tags=integration ./...
	go vet -tags=trace ./...
	go vet -tags=dryrun ./...
	go vet -tags=realrun ./...
	go vet -tags=capture ./...

## tidy: sync go.mod with the imports actually used
tidy:
	go mod tidy

## capture: refresh the fixture corpus from the real sources and mailbox.
## Reaches the network and writes into the repository. Everything is scrubbed on
## the way in, and a file that still looks identifying is refused rather than
## written.
capture: .env
	set -a; . ./.env; set +a; go test -tags=capture -count=1 -v -timeout 5m -run TestCaptureFixtures ./internal/fixtures/

## dryrun: print the API calls one article would make, without making them.
## Nothing reaches the provider and nothing is billed. Pick the article with
## ZIBA_ARTICLE_ID=775 make dryrun; the default is the newest with enough text.
## A test runs in its own package directory, so the configuration is named
## absolutely rather than relative to the repository root.
dryrun: .env
	set -a; . ./.env; ZIBA_INTERESTS_FILE=$(CURDIR)/config/interests.yaml; set +a; \
	go test -tags=dryrun -count=1 -v -run TestDryRun ./internal/pipeline/

## realrun: analyze ONE article for real, against the configured provider.
## This costs money. It reports the model's answer and the tokens it billed,
## and writes nothing back to the database. Defaults to the longest article;
## choose another with ZIBA_ARTICLE_ID=762 make realrun
realrun: .env
	set -a; . ./.env; ZIBA_INTERESTS_FILE=$(CURDIR)/config/interests.yaml; set +a; \
	go test -tags=realrun -count=1 -v -timeout 10m -run TestRealRun ./internal/pipeline/

## test-integration: run the real-data tests — real sources, real database
## Uses a separate `ziba_integration` database, never the application's own.
## Set ANTHROPIC_API_KEY to exercise the real model (this costs money).
test-integration: .env
	set -a; . ./.env; set +a; go test -tags=integration -count=1 -v -timeout 20m ./internal/integration/

## check: what should pass before a commit
check: fmt vet test

## clean: remove build output
clean:
	rm -rf bin/

## dev: bring the whole local stack up and migrate — one command to start working
dev: up migrate

## image: build the application image
image:
	$(COMPOSE) build ziba-api

## deploy: rebuild the image and bring the stack up (local by default)
deploy: image up

## app-logs: follow the application's log only
app-logs:
	$(COMPOSE) logs -f ziba-api

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

## digest: build today's selection, e.g. make digest ARGS="-date 2026-08-04"
digest: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) digest $(ARGS)

## run-once: do the whole chain once — collect, retrieve, analyze, select
run-once: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) run $(ARGS)

## serve: run the web interface and the schedule on http://localhost:8080
serve: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) serve $(ARGS)

## collect: read every enabled source, e.g. make collect ARGS=-no-fetch
collect: .env
	set -a; . ./.env; set +a; go run ./cmd/$(BINARY) collect $(ARGS)
