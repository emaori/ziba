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

Use the ```compose.yaml``` file available [here](compose.yaml) in the repository.

The image it pulls is [`ghcr.io/emaori/ziba`](https://github.com/emaori/ziba/pkgs/container/ziba) and the
database is Postgres.

Fill in the variables of the compose according to the documentation in this table (and in the compose file iteself).

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


On the first launch, open Ziba in a
browser. The setup wizard has three steps: interests, sources, then the
collection schedule. Add or edit one item on its
own page. You can also add a preconfigured
interest or RSS source, then edit it if needed. The Schedule step proposes the
defaults `6h` and `04:00`:
- **Run every** is the interval between runs
- **Start the daily cycle at** anchors those runs to the local time set by `TZ`.
For example, `6h` starting at `04:00` runs at 04:00, 10:00, 16:00,
and 22:00. Enter `0` as the interval to stop scheduled collection. Changes take
effect without restarting Ziba.

It also proposes starting the first collection as
soon as setup finishes; clear that checkbox to wait for the next scheduled time.
Collection does not start until setup is complete.

Newsletter credentials can be entered or changed in Settings. Stored usernames
and passwords are never displayed again.

Turning off collection for a source stops future collection. It does not remove
articles that Ziba already collected.

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

Without it, startup fails with a message. It only matters while the journal is switched on — nothing
writes there otherwise.
