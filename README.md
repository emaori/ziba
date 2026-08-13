# Ziba

[![Docker build](https://github.com/emaori/ziba/actions/workflows/image.yml/badge.svg)](https://github.com/emaori/ziba/actions/workflows/image.yml)
[![Latest release](https://img.shields.io/github/v/release/emaori/ziba?sort=semver)](https://github.com/emaori/ziba/releases)
[![License: MIT](https://img.shields.io/github/license/emaori/ziba)](LICENSE)
[![ghcr.io](https://img.shields.io/badge/ghcr.io-ziba-blue?logo=docker)](https://github.com/emaori/ziba/pkgs/container/ziba)

Ziba is a self-hosted personal content aggregator. It collects articles from
configured sources, processes them with AI, and presents them as a personalized
daily magazine.

Sources and interests are both configured by hand. An article is collected from
its source and appears in the daily digest only if it matches an interest and
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

An offline analyzer is also available. It only matches against the vocabulary
defined in `interests.yaml` (the subtopics), so it is crude — it exists for
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

It holds `compose.yaml` and three examples — `.env.example`,
`config/interests.example.yaml` and `config/sources.example.yaml` — already
arranged the way Compose expects. The image it pulls is
[`ghcr.io/emaori/ziba`](https://github.com/emaori/ziba/pkgs/container/ziba).

Copy each example to its real name and edit it: `.env`,
`config/interests.yaml`, `config/sources.yaml`. The three sections below say
what goes in them.

### interests.yaml

`config/interests.yaml` describes what is worth reading, and sets the threshold
an article must reach to be summarized and to appear in the digest.

An example template is available in `config/interests.example.yaml`.

For instance:

```yaml
threshold: 60

interests:
  - topic: "AI"
    priority: 1
    subtopics: ["LLMs", "AI agents", "machine learning", "ML infrastructure"]
    note: "Practical applications and how things actually work. Less interested in funding rounds and industry gossip."
```

`threshold: 60` means an article must score 60 or more, on a scale of 0 to 100,
to be summarized and to appear in the daily digest. Below it, one interest is
defined: AI, with its subtopics.

`config/interests.example.yaml` documents every field.

### sources.yaml

Sources are listed in `config/sources.yaml`, hand-edited. Adding one is adding
three lines; `enabled: false` stops reading a source without losing the articles
already collected from it. Removing a source from the file disables it rather
than deleting it, for the same reason.

Every source can bound how much back catalogue it brings with it, and a
newsletter source can read a single mail folder.

A source can also declare which interest it belongs to. When a newsletter is
already known to be about AI, for example, labelling it that way makes the
analyzer skip classification and work out only the score and the summary. Such
articles are always shown in the web interface, and reach the daily digest only
if their score clears the threshold.

```yaml
- name: "The Go Blog"
  type: rss
  collect_from: 14d
  url: "https://go.dev/blog/feed.atom"

- name: "AI newsletters"
  type: newsletter
  url: "imaps://imap.gmail.com"
  categories: ["AI"]
  newsletter:
    folder: "AI"
    username_env: ZIBA_IMAP_USER
    password_env: ZIBA_IMAP_PASSWORD
    days: 1
    max_messages: 50
```

Two sources here: an RSS feed (The Go Blog), which collects the last 14 days
the first time it is read, and a newsletter read from a Gmail account whose
credentials are named in the env file. Articles from that newsletter are
categorized as AI, so the analyzer does not try to classify them.

`config/sources.example.yaml` documents every field.

### .env file

`.env` sits beside `compose.yaml`, which is where Docker Compose reads it from.

On Windows, copy it from a terminal rather than renaming it in File Explorer,
which refuses a name that is only an extension.

`.env.example` documents every setting.

### Running it

With the `.env` file and both YAML files in place, start the stack:

```sh
docker compose up -d
```

### Things worth knowing

**There is no login.** Ziba is single-user by design and has no accounts, no
password and no session. Whoever can reach the port is the reader, and can mark
articles read as well as read them.

Set `ZIBA_BIND=127.0.0.1` in `.env` to allow only the machine it runs on, and
reach it from elsewhere through a reverse proxy that terminates TLS and asks for
a password, or through a VPN.

**It costs money to run.** Every collected article is assessed by the fast
model, and everything above the threshold is summarized by the capable one. On
one real archive of 485 articles that came to about $1.50 with OpenAI, or
roughly $4–5 a month at fifty articles a day. Two settings move that figure more
than anything else: the `threshold` in `interests.yaml`, which decides how much
reaches the expensive model, and `ZIBA_FAST_EFFORT` / `ZIBA_CAPABLE_EFFORT`,
because reasoning tokens are billed as output and output costs several times
input. The Statistics page reports exactly what has been spent, per interest and
per day.

**It runs without a key.** With no API key configured, collection, retrieval
and the archive all work, and only the analysis is skipped. That mode is crude
and meant for debugging.

**Credentials are never written into the YAML.** A mailbox source names the
server and the folder; `ZIBA_IMAP_USER` and `ZIBA_IMAP_PASSWORD` name the
account. A source address carrying a password is refused at startup. With Gmail the
password must be an App Password, pasted without the spaces the interface shows.

### Debugging what the model was asked

Set `ZIBA_MODEL_JOURNAL=true` and every request to a model, with its reply, is
appended to `log/modelJournal.txt`, which the compose file bind-mounts so it can
be read from the host.

Headers are never recorded, so the API key never reaches the file.

On Linux the container runs as uid 65534 and a `log/` directory created by root
on the host will not be writable by it. Startup fails with a message saying so
rather than quietly carrying on: `chown 65534 log`.

## License

MIT. See [LICENSE](LICENSE).
