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

## Collecting

Sources are listed in `config/sources.yaml`, hand-edited. Adding one is adding
three lines; `enabled: false` stops reading a source without losing the articles
already collected from it. Removing a source from the file disables it rather
than deleting it, for the same reason.

```sh
make collect                    # read every enabled source, then fetch full text
make collect ARGS=-no-fetch     # only collect raw items
make collect ARGS="-batch 20"   # process fewer items in one run
```

A run does two things. First it reads every enabled source in parallel and
stores what it finds as raw items, skipping anything already collected — feeds
republish their whole window on every poll, so this is what makes re-running
harmless. Then it takes unprocessed raw items, follows each link, extracts the
readable body of the page and stores it as an article.

Neither stage is all-or-nothing. A source that is down is logged and the others
continue; a page that cannot be fetched still produces an article, carrying the
excerpt the feed provided, and can be improved later.

## Processing

`config/interests.yaml` describes what is worth reading, and sets the threshold
an article must reach to be summarized and to appear in the digest.

```sh
make process                    # analyze articles not yet seen
make process ARGS=-offline      # no model, no network, no cost
make process ARGS="-batch 10"   # analyze fewer articles in one run
```

Each article goes through two stages. **Assessment** says what the article is
about and rates it against the interests, in a single call on the fast model.
**Summarization** writes a summary aimed at this reader, on the capable model,
and only for articles above the threshold.

Both halves of that split exist for cost. Input tokens are almost the entire
bill, and the article text dominates them — so assessment sends it once rather
than once per question, and the expensive model never sees an article that was
not worth reading.

Articles below the threshold keep their score and stay browsable in the archive.
They are simply not summarized and not promoted: the AI curates, it does not
censor.

`-offline` swaps the model for a deterministic keyword matcher. It is a poor
curator and says so in its output, but it makes the whole flow runnable while
developing everything downstream.

## Reading

```sh
make digest    # build today's selection from what cleared the threshold
make serve     # http://localhost:8080
```

Four screens: the daily digest, browsing by category, the archive, and the
article reader. Pages are rendered on the server with `html/template` and a
single stylesheet — no build step, no JavaScript toolchain, everything embedded
in the binary.

The digest is **stored, not recomputed**: `ziba digest` selects what cleared the
threshold on a given day and freezes the ranking, so a past day keeps the shape
it had when it was read even after interests or thresholds change. Re-running it
for the same day replaces that day's selection.

The archive holds everything, including articles that never cleared the
threshold. That is the point: the AI curates, it does not censor.

Article text is stored as plain text with one paragraph per line, and the reader
escapes it when rendering. Nothing a collected page contains can inject markup
into the interface — worth preserving if the templates are changed.

## Migrations are plain numbered `.sql` files under `internal/store/migrations/`,
embedded in the binary. `migrate` applies whatever has not been applied yet and
is safe to re-run; each migration runs in a transaction, and an advisory lock
prevents two processes from migrating at once. To add one, create the next
`NNNN_label.sql` — never edit a file that has already been applied.

## Layout

```
cmd/ziba/           main entry point; subcommand dispatch only
config/             hand-edited YAML: sources (interests to come)
internal/domain/    core types: Source, Collector, RawItem, Article, Digest
internal/config/    runtime configuration and YAML loading
internal/collect/   one Collector per source type, plus full-text retrieval
internal/pipeline/  AI stages: assessment and summarization
internal/store/     PostgreSQL pool, migrations, queries
internal/web/       HTTP handlers, templates and stylesheet
```

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
