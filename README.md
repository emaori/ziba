# Ziba

[![Docker build](https://github.com/emaori/ziba/actions/workflows/image.yml/badge.svg)](https://github.com/emaori/ziba/actions/workflows/image.yml)
[![Latest release](https://img.shields.io/github/v/release/emaori/ziba?sort=semver)](https://github.com/emaori/ziba/releases)
[![License: MIT](https://img.shields.io/github/license/emaori/ziba)](LICENSE)
[![ghcr.io](https://img.shields.io/badge/ghcr.io-ziba-blue?logo=docker)](https://github.com/emaori/ziba/pkgs/container/ziba)

Ziba is a self-hosted personal content aggregator. It collects articles from
configured sources, processes them with AI, and presents them as a personalized
magazine.

Sources and interests are both configured by hand. An article is collected from
its source and appears in the last-24-hours digest only if it matches an interest and
scores above a configured threshold. Articles that pass are then summarized.

The supported source types are:
- RSS feeds
- Email newsletters

In the web interface, articles are grouped by interest and ordered by score.

Scoring and summarization use models chosen by the operator: a fast one to
analyze and score each article, and a more capable one to write the summary.
Models from OpenAI and Anthropic are supported.

Each article goes through two stages. **Assessment** says what the article is
about and rates it against the interests, in a single call on the fast model.
**Summarization** writes a summary aimed at those interests, on the capable
model, and only for articles above the threshold.

Articles below the threshold keep their score and stay browsable in the archive.
They are simply not summarized and not promoted: the AI curates, it does not
censor.

An offline analyzer is also available. It only matches against the configured
interest vocabulary, so it is crude — it exists for
debugging, not for daily use.


## Deploy

Deploying does not need this repository. Every release carries a bundle with
everything required:

Linux and macOS:

```sh
curl -L https://github.com/emaori/ziba/releases/latest/download/ziba-deploy.tar.gz | tar -xz
cd ziba
```

Windows, in PowerShell:

```powershell
curl.exe -L -O https://github.com/emaori/ziba/releases/latest/download/ziba-deploy.zip
Expand-Archive ziba-deploy.zip -DestinationPath .
cd ziba
```

It holds `compose.yaml`, already arranged the way Compose expects. The image it pulls is
[`ghcr.io/emaori/ziba`](https://github.com/emaori/ziba/pkgs/container/ziba).

Fill in `compose.yaml`, then start Ziba. On the first launch, open Ziba in a
browser. The setup wizard has two steps: interests, then sources. Each step has
a table. Add or edit one item on its own page. You can also add a preconfigured
interest or RSS source, then edit it if needed. RSS and newsletter forms show
only the fields that apply to the selected type. Collection does not start until
setup is complete.

After setup, use **Settings** in the web interface. It has separate Interests
and Sources tables and the same add, edit, and preconfigured options. Changes
apply without restarting Ziba. Interest changes affect new articles only.
Existing articles are not re-analyzed.

Newsletter credentials can be entered or changed in Settings. Stored usernames
and passwords are never displayed again.

Upgrades from an older version are automatic. If the database is not configured
yet and the old YAML files are present, Ziba imports them once. Existing source
IDs and articles are preserved. After the import, the YAML files are no longer
read.

### Settings

Everything else is in `compose.yaml`, each setting beside a note on what it
does. Optional ones are commented out with their defaults written down; the
ones already uncommented are these:

| Setting | What it decides | What to put |
|---|---|---|
| `POSTGRES_PASSWORD` | the database password | **change it** — anything |
| `ZIBA_DATABASE_URL` | how Ziba reaches the database | the same password, after `ziba:` — it ships blank |
| `ZIBA_AI_PROVIDER` | which company answers | `openai` or `anthropic` |
| `OPENAI_API_KEY` | pays for it | **your key** — or `ANTHROPIC_API_KEY`, uncommented, if you chose anthropic |
| `ZIBA_FAST_MODEL` | scores every article | **required**, e.g. `gpt-5.6-luna` |
| `ZIBA_CAPABLE_MODEL` | writes the summaries | **required**, e.g. `gpt-5.6-terra` |
| `ZIBA_FAST_EFFORT` | how hard the fast model thinks | `low` — raising it costs more than the article does |
| `ZIBA_CAPABLE_EFFORT` | the same, for summaries | `low` |
| `TZ` | which local clock the schedule follows | your zone, e.g. `Europe/Rome`; otherwise UTC |
| `ZIBA_COLLECT_EVERY` | how often Ziba runs | `6h`; `0` stops the schedule |
| `ZIBA_COLLECT_AT` | when the daily cycle starts | `"04:00"`, in `TZ` above |

These two settings define one schedule. `ZIBA_COLLECT_AT` sets the first run of
each day. `ZIBA_COLLECT_EVERY` sets the interval between runs. For example,
`ZIBA_COLLECT_AT=04:00` and `ZIBA_COLLECT_EVERY=6h` run Ziba at 04:00, 10:00,
16:00, and 22:00. Each run collects articles, processes them, and refreshes the
last-24-hours digest. Restarting Ziba does not change these times. Set
`ZIBA_COLLECT_EVERY=0` to disable all scheduled runs.

Four need a value before anything works properly: the database password in both
places it appears, the API key, and the two model names. Everything else has a
value that already makes sense.

There are no model defaults on purpose — a name written into a program is a
claim about what exists, and a stale one fails at the first article instead of
at startup. Take the current names from your provider.

### Running it

With `compose.yaml` filled in, start the stack:

```sh
docker compose up -d
```

The schedule is tied to the clock. With the defaults, Ziba runs at 04:00, 10:00,
16:00, and 22:00. Restarting Ziba does not move those times.

Each run collects and processes articles. It then rebuilds the home page from
the last 24 hours. A 04:00 run gives the digest time to be ready for the morning.

If Ziba missed the latest run, it runs once at startup. It does not replay every
missed run. To run it now:

```sh
docker compose exec ziba-api ziba run
```

That collects, retrieves, analyzes, and refreshes the digest. The normal schedule
then continues.

### Things worth knowing

**There is no login.** Ziba is single-user by design and has no accounts, no
password and no session. Whoever can reach the port is the reader, and can mark
articles read as well as read them.

**It costs money to run.** Every collected article is assessed by the fast
model, and everything above the threshold is summarized by the capable one. On
one real archive of 485 articles that came to about $1.50 with OpenAI, or
roughly $4–5 a month at fifty articles a day. Two settings move that figure more
than anything else: the threshold in Settings, which decides how much
reaches the expensive model, and `ZIBA_FAST_EFFORT` / `ZIBA_CAPABLE_EFFORT`,
because reasoning tokens are billed as output and output costs several times
input. The Statistics page reports exactly what has been spent, per interest and
per day.

The same page shows whether retrieval or analysis is falling behind. Temporary
processing failures retry automatically; repeatedly failing items are reported
there instead of blocking newer work.

**It runs without a key.** With no API key configured, collection, retrieval
and the archive all work, and only the analysis is skipped. That mode is crude
and meant for debugging.

**Newsletter credentials are write-only in Settings.** Ziba never displays a
stored username or password. Leave those fields blank while editing to keep the
stored values. With Gmail, use an App Password and paste it without spaces.

**Web collection only connects to public addresses.** RSS feeds, roundup pages,
article pages and every redirect are refused if their hostname resolves to a
loopback, private, link-local or otherwise non-public address. This prevents an
external feed or newsletter from using Ziba to reach services on the machine or
home network. IMAP servers are explicitly configured by the operator and are
not subject to this restriction.

### Debugging what the model was asked

Set `ZIBA_MODEL_JOURNAL=true` and every request to a model, with its reply, is
appended to `log/modelJournal.txt`, which the compose file bind-mounts so it can
be read from the host.

Headers are never recorded, so the API key never reaches the file.

The container runs as `nobody`, uid 65534, so the `log/` directory on the host
has to belong to it. Docker creates that directory as root on the first `up`,
and root's is not writable by `nobody`:

```sh
chown 65534:65534 log
```

Without it, startup fails with a message saying exactly this rather than
quietly carrying on. It only matters while the journal is switched on — nothing
writes there otherwise.

## License

MIT. See [LICENSE](LICENSE).
