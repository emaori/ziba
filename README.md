# Ziba

Ziba is a self-hosted personal content aggregator: collects articles from configured
sources, processes them with AI, and presents them as a personalized daily
magazine.

Different sources and different interests can be configured: the articles will be collected from sources and will be shown in Ziba Web UI daily digest only if they match the configured interests with a threeshold (score) above a configured value. The articles that are selected and pass the threshold will be then summariezed.

So far the supported type of sources are:
- Feed RSS
- E-mail Newsletter

In Ziba Web UI the articles will be groupd by inteests and ordered according to the score they got.

Score analysis and summarization are performed using LLM set by the user: a fast model to analyze and score the article and a more capable model to generate the summary.

Currently the supported LLM are the ones from OpenAI and Anthropic.

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

An offline analyzer is availbale but not very useful since it just tries to match according to a vocabulary defined in ```interests.jaml``` (the subtopics). This is usually mainly used for debug reason.


## Deploy

Before starting the deploy, three files must be provided: the env file and two configs file, ```interests.yaml``` and ```sources.yaml```.

### interests.yaml

`config/interests.yaml` describes what is worth reading, and sets the threshold
an article must reach to be summarized and to appear in the digest.

An example template is available in ```config/interests.example.yaml```.

For instance:

```yaml
threshold: 60

interests:
  - topic: "AI"
    priority: 1
    subtopics: ["LLMs", "AI agents", "machine learning", "ML infrastructure"]
    note: "Practical applications and how things actually work. Less interested in funding rounds and industry gossip."
```

```threshold: 60``` means the the relevance score (0-100) an article must reach to be summarized and to appear in the daily digest is 60. Then the AI interest is defined with its subtopic.

In ```config/interests.example.yaml``` is also available the documentation of each sections.

### sources.jaml

Sources are listed in `config/sources.yaml`, hand-edited. Adding one is adding
three lines; `enabled: false` stops reading a source without losing the articles
already collected from it. Removing a source from the file disables it rather
than deleting it, for the same reason.

So far the only supported type of sources are feed RSS and newsletter (e-mail) but in the near future other type of sources will be added.

Every sources can be configured to read not too much back catalouge and the newsletter can be configured to read only the e-mail of a specific folder. 

A source can also be configured to a specific interest: for instance it can be already known that a newsletter is about a specific interest (eg: AI) so that source can be labeld for that interest and the Ziba analyzer will sikp the classification and it will claculate just the score and the summary. In this case the articles are always shown in Ziba Web UI but are added to the daily digest only if the score is above the threeshold.

```yaml
- name: "The Go Blog"
  type: rss
  collect_from: 14d
  url: "https://go.dev/blog/feed.atom"

- name: "AI newsletters"
  type: newsletter
  url: "imaps://imap.gmail.com:993/"
  categories: ["AI"]
  newsletter:
    folder: "AI"
    username_env: ZIBA_IMAP_USER
    password_env: ZIBA_IMAP_PASSWORD
    days: 1
    max_messages: 50
```

In this example there are two sources: a RSS feed (The Go Blog) configured to collect from the last 14 days the first time the sources is used, and a newsltter configured to read the e-mails from a gmail account (configured in the env file, see below). The articles extracted from the newsletter are categorized as AI so the analyzer will not try to classify them among the defiend interests.

More details on how configure the sources can be read in the ```config/sources.example.yaml``` file.

### .env file

The ```.env``` can be created using ```.env.example``` as template and placed in the same folder of the ```compose.yaml``` file, since it is used for the deploy of the containers.

```.env.example``` contains the documentation for each parameters.

## Deploy

When the ```.env``` file and both yaml files are ready, run docker compose to deploy:

```sh
docker compose up -d
```

### Things worth knowing

**There is no login.** Ziba is single-user by design and has no accounts, no
password and no session. Whoever reaches the port is the reader, and can mark
articles read as well as read them.

Set `ZIBA_BIND=127.0.0.1` in `.env` to allow only the machine it runs on, and
reach it from elsewhere through a reverse proxy that terminates TLS and asks for
a password, or through a VPN.

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
the archive all work and only the analysis is skipped. Obviously this mode is 
pretty dumb — it's meant for debugging only.

**Credentials are never written into the YAML.** A mailbox source names the
server and the folder; `ZIBA_IMAP_USER` and `ZIBA_IMAP_PASSWORD` say who you
are. A source address carrying a password is refused at startup. With Gmail the
password must be an App Password, pasted without the spaces the interface shows.

### Debugging what the model was asked

Set `ZIBA_MODEL_JOURNAL=true` and every request to a model, with its reply, is
appended to `log/modelJournal.txt` — bind-mounted by both compose files, so it
is readable from the host.

Headers are never recorded, so API key is never written in the log.

On Linux the container runs as uid 65534 and a `log/` directory created by root
on the host will not be writable by it. Startup fails with a message saying so
rather than quietly carrying on: `chown 65534 log`.

