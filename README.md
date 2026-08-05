# Ziba

Self-hosted personal content aggregator: collects articles from configured
sources, processes them with AI, and presents them as a personalized daily
magazine.

This repository holds the implementation. **Product requirements live in the
sibling `ziba-docs` repository** (`functional/`); this README documents how the
code works, not what the product should do.

## Requirements

- Go 1.26 or newer
- Docker with Compose (PostgreSQL and the Playwright sidecar)

## Getting started

```sh
make build        # compile into bin/ziba
make run ARGS=version
make check        # fmt + vet + test — run before committing
```

## Running the stack

Everything the application needs runs in Docker. Today that is PostgreSQL;
`ziba-api`, the Playwright sidecar and Caddy join the same file as they arrive,
and the commands below do not change.

```sh
cp .env.example .env    # then set a real password
make dev                # start everything locally and migrate
```

`make dev` is `up` followed by `migrate`. The rest: `make down` stops the stack
and keeps the data, `make restart` recreates it after editing `compose.yaml`,
`make ps` and `make logs` show what is happening, `make db-psql` opens a psql
shell inside the database container.

### Local engine vs homeserver

There are two Docker engines and they are not interchangeable, so every
Compose command names one explicitly:

| Context | Use | Reaching published ports |
|---|---|---|
| `desktop-linux` (default) | development on this machine | directly on `localhost` |
| `homeserver` | the deployment target, over SSH | needs `make db-tunnel` |

The default is local on purpose: a stray `make down` should never take out the
real deployment. Target the homeserver by saying so:

```sh
make up DOCKER_CONTEXT=homeserver
make db-tunnel            # second terminal: localhost:5432 -> homeserver
make migrate
```

Ports are published on loopback only, which on the homeserver means *its*
loopback — hence the tunnel. The two setups both use port 5432 locally, so the
tunnel and the local stack cannot run at the same time; stop one before the
other. Override with `DB_PORT` or `SSH_HOST` if either changes.

## Migrations are plain numbered `.sql` files under `internal/store/migrations/`,
embedded in the binary. `migrate` applies whatever has not been applied yet and
is safe to re-run; each migration runs in a transaction, and an advisory lock
prevents two processes from migrating at once. To add one, create the next
`NNNN_label.sql` — never edit a file that has already been applied.

## Layout

```
cmd/ziba/           main entry point; subcommand dispatch only
internal/domain/    core types: Source, Collector, RawItem, Article, Digest
internal/config/    runtime configuration
internal/store/     PostgreSQL pool, migrations, queries
```

Packages are added as they gain real content. Still to come:

| Package | Responsibility |
|---|---|
| `internal/collect` | One `Collector` implementation per source type |
| `internal/pipeline` | AI stages: extraction, scoring, summarization |
| `internal/web` | HTTP handlers and templates |

`internal/domain` is imported by all of them and imports none of them, which is
what keeps the dependencies acyclic.

## Architecture in one paragraph

A Go monolith with sharp internal seams. Sources are collected in parallel into
`RawItem`s, passed through an AI pipeline that extracts topics, scores relevance
against the configured interests and summarizes what clears the threshold, then
persisted as `Article`s with their full text. The daily `Digest` is the ranked
selection above threshold; everything below stays browsable in the archive.

The primary seam is the `Collector` interface: supporting a new kind of source
means implementing it, not modifying the pipeline.
