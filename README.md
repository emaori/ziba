# Ziba

Self-hosted personal content aggregator: collects articles from configured
sources, processes them with AI, and presents them as a personalized daily
magazine.

This repository holds the implementation. **Product requirements live in the
sibling `ziba-docs` repository** (`functional/`); this README documents how the
code works, not what the product should do.

## Hosting your own instance

You need Docker and nothing else — no Go toolchain, no clone of this repository.
Everything is configured through environment variables, except your interests
and your sources, which are hand-edited YAML files mounted into the container.

You need `deploy/compose.yaml`, `deploy/.env.example`, and the two starter
configs. In a directory of their own:

```sh
mkdir ziba && cd ziba
curl -L https://github.com/emaori/ziba/archive/refs/heads/main.tar.gz \
  | tar -xz --strip-components=1 ziba-main/deploy ziba-main/config
mv deploy/.env.example deploy/compose.yaml . && rmdir deploy
cp .env.example .env
cp config/interests.example.yaml config/interests.yaml
cp config/sources.example.yaml   config/sources.yaml
```

Then edit two things.

**`.env`** — a database password, your AI provider and its key, and the two
model names. There are no default model names on purpose: a name written into a
program is a claim about what exists, and a stale one fails at the first article
instead of at startup. Take the current ones from your provider's model list.
Every variable is documented in the file.

**`config/interests.yaml`** — what you want to read. This is the file that makes
Ziba yours; the starter is somebody else's taste. **`config/sources.yaml`** —
where to read it from. The starter has three public feeds so a fresh instance
has something to collect.

```sh
docker compose up -d
```

Both YAML files are required — the image contains neither, and a container
without them stops and says so. The schema is created and migrated on startup,
the scheduler collects on a timer, and the interface is on port 8080 — on this machine at
<http://localhost:8080>, and from your phone or laptop at the host's own address.

### Things worth knowing

**There is no login.** Ziba is single-user by design and has no accounts, no
password and no session. Whoever reaches the port is the reader, and can mark
articles read as well as read them.

By default the port is published on every interface, so the instance works from
the other devices on your network without configuring anything. On a home
network behind a router that is usually what you want, but two things are worth
knowing before you leave it that way. A published Docker port is **not** covered
by `ufw` — Docker writes its own firewall rules and they are consulted before
the chain `ufw` manages, so enabling a host firewall does not close this. And if
your router forwards the port, the instance is on the internet.

Set `ZIBA_BIND=127.0.0.1` in `.env` to allow only the machine it runs on, and
reach it from elsewhere through a reverse proxy that terminates TLS and asks for
a password, or through a VPN. With Tailscale, putting the machine's own `100.x`
address in `ZIBA_BIND` is the middle ground: your devices, nothing else.

**It costs money to run.** Every collected article is assessed by the fast
model, and everything above the threshold is summarised by the capable one. On
one real archive of 485 articles that came to about $1.50 with OpenAI, or
roughly $4–5 a month at fifty articles a day. Two settings move that figure more
than anything else: the `threshold` in `interests.yaml`, which decides how much
reaches the expensive model, and `ZIBA_FAST_EFFORT` / `ZIBA_CAPABLE_EFFORT`,
because reasoning tokens are billed as output and output costs several times
input. The Statistics page reports exactly what has been spent, per interest and
per day.

**It runs without a key.** With no API key configured, collection, retrieval and
the archive all work and only the analysis is skipped — the server says so and
starts anyway. `ziba process -offline` uses a keyword matcher instead, which is
enough to see the shape of the thing before paying for it.

**Credentials are never written into the YAML.** A mailbox source names the
server and the folder; `ZIBA_IMAP_USER` and `ZIBA_IMAP_PASSWORD` say who you
are. A source address carrying a password is refused at startup. With Gmail the
password must be an App Password, pasted without the spaces the interface shows.

### Debugging what the model was asked

Set `ZIBA_MODEL_JOURNAL=true` and every request to a model, with its reply, is
appended to `log/modelJournal.txt` — bind-mounted by both compose files, so it
is readable from the host.

It answers the question the database cannot: not what the model said, but what
exactly it was asked. A prompt is assembled from your interests file, the
article, a JSON schema and several rules about declared categories, and when an
answer is surprising the assembled text is the thing to read.

It is written at the HTTP layer, below both providers, so nothing can be added
later that escapes it. Headers are never recorded, which is how the API key
stays out of a file people paste into bug reports. It holds the full text of
every article sent and is never rotated, so turn it off again.

On Linux the container runs as uid 65534 and a `log/` directory created by root
on the host will not be writable by it. Startup fails with a message saying so
rather than quietly carrying on: `chown 65534 log`.

### Updating

```sh
docker compose pull && docker compose up -d
```

`latest` follows released tags rather than the main branch. Pin a specific one
with `ZIBA_VERSION=v0.1.0` in `.env` if you would rather choose when to move.
Migrations run automatically on startup and are safe to re-run.

## Your configuration

Two YAML files decide what Ziba does, and they work exactly like `.env`:

```
config/interests.yaml          yours, git-ignored
config/interests.example.yaml  committed, the starter
config/sources.yaml            yours, git-ignored
config/sources.example.yaml    committed, the starter
```

A fresh clone starts the same way it does for secrets:

```sh
cp config/interests.example.yaml config/interests.yaml
cp config/sources.example.yaml   config/sources.yaml
```

There is no build step and no second copy — the file you edit is the file the
program reads.

**The image ships no configuration at all.** Nothing is baked in, so a container
with nothing mounted stops on its first run and says which file it wanted and
where to put it. That is deliberate: an instance quietly collecting somebody
else's interests looks like it is working, and nobody reads the log of something
that appears to work.

The arrangement exists so the repository can be public without publishing a
reading list: `sources.yaml` names the newsletters somebody subscribes to and
the folders in their mailbox. Not a secret, but not everyone's business.

## Requirements

To develop Ziba, rather than run it:

- Go 1.26 or newer
- Docker with Compose (PostgreSQL)

## Getting started

```sh
make build        # compile into bin/ziba
make run ARGS=version
make check        # fmt + vet + test — run before committing
```

## Running the stack

Everything the application needs runs in Docker: the application and
PostgreSQL. Caddy joins the same file when remote access is set up, and the
commands below do not change.

The `compose.yaml` at the root is the development stack: it builds the image
from source. `deploy/compose.yaml` is the one other people use, and it pulls a
published image instead — see [Hosting your own instance](#hosting-your-own-instance).

```sh
cp .env.example .env                                  # then set a real password
cp config/interests.example.yaml config/interests.yaml
cp config/sources.example.yaml   config/sources.yaml
make dev                                              # start everything and migrate
```

`make dev` is `up` followed by `migrate`. The rest: `make down` stops the stack
and keeps the data, `make restart` recreates it after editing `compose.yaml`,
`make ps` and `make logs` show what is happening, `make db-psql` opens a psql
shell inside the database container.

### Deploying to another machine

Every Compose command names a Docker context explicitly, and the default is
this machine. That is a safety choice: a stray `make down` should never take
out something you are actually running. Deploying elsewhere means saying so:

```sh
docker context create my-server --docker host=ssh://user@example
make up DOCKER_CONTEXT=my-server
```

The database is published on loopback only, which on a remote machine means
*its* loopback, so reaching it from here needs a tunnel:

```sh
make db-tunnel SSH_HOST=user@example    # second terminal
make migrate
```

Both ends use 5432, so the tunnel and a local stack cannot run at once; stop
one before the other. `DB_PORT` overrides the port.

## Collecting

Sources are listed in `config/sources.yaml`, hand-edited. Adding one is adding
three lines; `enabled: false` stops reading a source without losing the articles
already collected from it. Removing a source from the file disables it rather
than deleting it, for the same reason.

Two source types work today: **`rss`** reads a feed, and **`newsletter`** reads
a mailbox.

> Scraping was removed. It needed a bespoke selector for every site and broke
> whenever one was redesigned, while almost every worthwhile source publishes a
> feed or a newsletter. A `type: website` source is now rejected with a message
> saying so.

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
target here. Deploying elsewhere is `DOCKER_CONTEXT=<your context>`.

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
