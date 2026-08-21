import assert from "node:assert/strict";
import test from "node:test";

import { requirePublicURL } from "./public-url.mjs";

test("accepts a public web address", async () => {
  const lookup = async () => [{ address: "93.184.216.34", family: 4 }];
  const got = await requirePublicURL("https://example.com/feed", lookup);
  assert.equal(got.href, "https://example.com/feed");
});

test("rejects credentials and non-web schemes", async () => {
  await assert.rejects(() => requirePublicURL("file:///etc/passwd"), /HTTP or HTTPS/);
  await assert.rejects(() => requirePublicURL("https://user:secret@example.com/feed"), /credentials/);
});

test("rejects private and reserved addresses", async () => {
  for (const url of [
    "http://127.0.0.1/feed",
    "http://10.0.0.1/feed",
    "http://192.168.1.2/feed",
    "http://169.254.169.254/feed",
    "http://[::1]/feed",
    "http://[fc00::1]/feed",
  ]) {
    await assert.rejects(() => requirePublicURL(url), /non-public/);
  }
});

test("rejects a hostname when any answer is non-public", async () => {
  const lookup = async () => [
    { address: "93.184.216.34", family: 4 },
    { address: "192.168.1.20", family: 4 },
  ];
  await assert.rejects(() => requirePublicURL("https://mixed.example/feed", lookup), /non-public/);
});
