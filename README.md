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

Everything the application needs runs in Docker: PostgreSQL and the
`ziba-playwright` browser sidecar. `ziba-api` and Caddy join the same file as
they arrive, and the commands below do not change.

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

Two source types work today. **`rss`** reads a feed. **`website`** scrapes a page
for article links, which is how sites that publish no feed are read:

```yaml
  - name: "ANSA — Tecnologia"
    type: website
    url: "https://www.ansa.it/sito/notizie/tecnologia/tecnologia.shtml"
    website:
      link_pattern: "/[0-9]{4}/[0-9]{2}/[0-9]{2}/"   # articles, not navigation
      max_links: 25
      render: false                                   # see below
```

`link_pattern` is the setting that matters: without it you collect a site's menu
alongside its articles, and most sites put a date or a section in article
addresses. A scraped source yields links only — each one then goes through the
same full-text retrieval as everything else, so a scraped article and a feed
article are identical downstream.

**`newsletter`** reads a mailbox over IMAP. Newsletters are treated as link
aggregators, which is what they are: the email is a list of links with blurbs,
and the value is in the articles it points at.

```yaml
  - name: "Newsletters"
    type: newsletter
    url: "imaps://imap.example.com:993/"
    newsletter:
      folder: "INBOX"
      username_env: ZIBA_IMAP_USER      # names the variable, never the value
      password_env: ZIBA_IMAP_PASSWORD
      unread_only: true
```

Credentials are named, not written: the file holds the *name* of an environment
variable, so the source list stays safe to commit, and an address containing
credentials is rejected outright. An unset variable fails at startup naming the
variable, rather than surfacing later as an authentication error.

Each email yields the editorial links it carries — social buttons, unsubscribe
footers, "view in browser" headers and sponsor slots are filtered out — plus the
email itself stored as **provenance**: kept so a link has an origin, never shown
as an article. The filtering is rule-based today; the functional documentation
calls for AI to do this eventually, and `editorialLinks` is where that would go.

`render: true` fetches the page through the **`ziba-playwright`** sidecar, a
headless browser, for sites that build their markup in the browser. It is much
slower and needs the sidecar running (`make up` starts it), so leave it off
unless a site genuinely needs it. A source that asks for rendering with no
sidecar configured fails with a message saying so, rather than quietly
collecting an empty page.

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

## Running it unattended

`ziba serve` runs the web interface **and the schedule** in one process. All the
timing lives in the binary — there is no cron entry and no systemd timer to
install.

```sh
make deploy      # build the image and bring the whole stack up
make app-logs    # watch it work
make run-once    # do the whole chain now, without waiting for a timer
```

`make deploy` targets the **local** Docker engine, like every other Compose
target here. Deploying to the homeserver is `DOCKER_CONTEXT=homeserver`, and has
not been done yet.

Two things the container needs that are easy to forget: `ca-certificates`,
without which every HTTPS source fails, and `tzdata` plus a `TZ` setting —
the digest is scheduled in *local* time, and a container that only knows UTC
would build it at the wrong hour. Both are in the image; `TZ` is in `.env`.

The container migrates the schema on startup. Migrations are idempotent and
protected by a lock, and there is no opportunity to run them by hand in a
container, so that is both safe and the only sensible moment.

```sh
make serve                       # interface + schedule
make serve ARGS=-no-schedule     # interface only
make run-once                    # the whole chain now, then exit
```

Two clocks, on purpose:

| Setting | Default | What it does |
|---|---|---|
| `ZIBA_COLLECT_EVERY` | `6h` | Collect, retrieve full text, analyze. `0` disables the schedule entirely. |
| `ZIBA_DIGEST_AT` | `06:30` | Build the day's selection, local time. |

Feeds move through the day, so a front page collected once is a front page
mostly missed — that wants an interval. The selection is a morning thing: it
should be waiting when you arrive, not rebuilt under you while you read. The
digest time is wall-clock, so it survives the clocks changing.

Both values are parsed at startup, so a typo fails immediately rather than at
half past six some morning. A scheduled run that fails is logged and the
schedule continues; nothing is watching, and one bad night must not stop the
next one. Without an API key the server still starts and still collects — it
just skips analysis and says so.

If the process was down at the appointed time, it builds that day's selection
when it comes back — the one failure a reader would actually notice. It checks
first, so an ordinary restart later in the day leaves a selection you may
already be reading alone.

## Reading

```sh
make digest    # build today's selection by hand
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

## Integration tests

```sh
make test-integration      # real sources, real database, ~10 minutes
```

These run against the actual configured sources over the real network, and
against a separate `ziba_integration` database — never the application's own,
because they truncate it. They are behind a build tag, so `make check` stays
fast and hermetic.

They fail when a site goes down or restructures its feed. That is not flakiness
to be engineered away; it is what the tests are for.

Set `ANTHROPIC_API_KEY` to exercise the real model. Without it the deterministic
analyzer stands in and every test says so in its output — a run that silently
used keyword matching and reported success would be worse than no test at all.
Assertions about *curation quality* only bind when a real model ran.

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
internal/job/       the scheduled work, and the scheduler that drives it
internal/web/       HTTP handlers, templates and stylesheet
```

`internal/job` exists so the commands and the schedule run exactly the same
code. A nightly run that differs from what you get by typing the command is a
bug waiting for a quiet night to happen.

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
