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

## Layout

```
cmd/ziba/           main entry point; subcommand dispatch only
internal/domain/    core types: Source, Collector, RawItem, Article, Digest
```

Packages are added as they gain real content. The planned shape:

| Package | Responsibility |
|---|---|
| `internal/config` | Loading the hand-edited YAML files (sources, interests) |
| `internal/collect` | One `Collector` implementation per source type |
| `internal/pipeline` | AI stages: extraction, scoring, summarization |
| `internal/store` | PostgreSQL persistence and migrations |
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
