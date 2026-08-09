# The fixture corpus

Real inputs, frozen, so that tests can assert exact numbers.

Every file here was captured from a live source and **scrubbed** on the way in:
the reader's address, the address they forward from, subscriber identifiers,
tracking parameters and unsubscribe tokens are all replaced with stable
stand-ins. Distinct values stay distinct — "two trackers pointing at one
article" is a case under test — but none of them is anybody's.

Refresh with `make capture`, which re-fetches and re-scrubs. It refuses to
write anything the scrubber's own check still finds identifying.

| Directory | Holds |
|---|---|
| `mail/` | newsletters as delivered, one `.eml` each |
| `web/`  | feeds, article pages, and recorded redirects |

A file in `web/` beginning `ziba-fixture-redirect:` stands for a redirect to the
address that follows, which is how a newsletter's click tracker is recorded.

Addresses are rewritten to the `fixture.test` host, so the corpus refers only to
itself and a careless run cannot reach the real network.
