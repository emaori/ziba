import http from "node:http";
import { firefox } from "playwright";

import { requirePublicURL } from "./public-url.mjs";
import { createSafeProxy } from "./safe-proxy.mjs";

const port = 3000;
const requestBodyLimit = 16 * 1024;
const feedBodyLimit = 8 * 1024 * 1024;
const navigationTimeout = 45_000;
const challengeWait = 10_000;
const maxConcurrentPages = 2;

class Semaphore {
  constructor(limit) {
    this.available = limit;
    this.waiters = [];
  }

  async acquire() {
    if (this.available > 0) {
      this.available -= 1;
      return;
    }
    await new Promise((resolve) => this.waiters.push(resolve));
  }

  release() {
    const next = this.waiters.shift();
    if (next) {
      next();
      return;
    }
    this.available += 1;
  }
}

const pages = new Semaphore(maxConcurrentPages);
let browser;
let proxy;

function json(response, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  response.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": body.length,
  });
  response.end(body);
}

async function readJSON(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > requestBodyLimit) {
      throw new Error("request body is too large");
    }
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

function isFeed(body) {
  const head = body.subarray(0, Math.min(body.length, 8192)).toString("utf8");
  return /<rss(?:\s|>)/i.test(head) || /<feed(?:\s|>)/i.test(head);
}

async function fetchFeed(rawURL) {
  const target = await requirePublicURL(rawURL);
  await pages.acquire();
  const context = await browser.newContext({
    locale: "en-US",
    proxy: { server: proxy.url },
  });

  try {
    await context.route("**/*", async (route) => {
      try {
        await requirePublicURL(route.request().url());
        await route.continue();
      } catch {
        await route.abort("blockedbyclient");
      }
    });

    const page = await context.newPage();
    const navigations = [];
    page.on("response", (response) => {
      const request = response.request();
      if (request.isNavigationRequest() && request.frame() === page.mainFrame()) {
        navigations.push(response);
      }
    });

    try {
      await page.goto(target.href, {
        waitUntil: "domcontentloaded",
        timeout: navigationTimeout,
      });
    } catch (error) {
      // Firefox treats RSS as a download. The captured response below is the
      // authority; this navigation error alone is not a failed fetch.
      if (!String(error.message).includes("Download is starting")) {
        throw error;
      }
    }

    let response = navigations.at(-1);
    if (!response) {
      throw new Error("browser captured no navigation response");
    }

    await response.finished().catch(() => {});
    const declaredLength = Number(response.headers()["content-length"]);
    if (Number.isFinite(declaredLength) && declaredLength > feedBodyLimit) {
      throw new Error(`feed exceeds ${feedBodyLimit} bytes`);
    }
    let body = await response.body();

    // A managed challenge may navigate again after its JavaScript runs.
    if (response.status() !== 200 || !isFeed(body)) {
      await page.waitForTimeout(challengeWait);
      response = navigations.at(-1) ?? response;
      await response.finished().catch(() => {});
      body = await response.body();
    }

    if (body.length > feedBodyLimit) {
      throw new Error(`feed exceeds ${feedBodyLimit} bytes`);
    }
    if (response.status() !== 200) {
      throw new Error(`upstream returned ${response.status()} ${response.statusText()}`);
    }
    if (!isFeed(body)) {
      throw new Error("upstream response is not an RSS or Atom feed");
    }

    return {
      body,
      contentType: response.headers()["content-type"] ?? "application/xml",
      finalURL: response.url(),
    };
  } finally {
    await context.close();
    pages.release();
  }
}

async function handle(request, response) {
  if (request.method === "GET" && request.url === "/health") {
    json(response, browser?.isConnected() ? 200 : 503, {
      status: browser?.isConnected() ? "ok" : "unavailable",
    });
    return;
  }
  if (request.method !== "POST" || request.url !== "/fetch") {
    json(response, 404, { error: "not found" });
    return;
  }

  try {
    const input = await readJSON(request);
    if (typeof input.url !== "string" || input.url.trim() === "") {
      json(response, 400, { error: "url is required" });
      return;
    }
    const result = await fetchFeed(input.url);
    response.writeHead(200, {
      "Content-Type": result.contentType,
      "Content-Length": result.body.length,
      "X-Ziba-Final-URL": result.finalURL,
    });
    response.end(result.body);
  } catch (error) {
    console.error("feed fetch failed", { error: error.message });
    json(response, 502, { error: error.message });
  }
}

proxy = await createSafeProxy();
browser = await firefox.launch({ headless: true });
const server = http.createServer((request, response) => {
  handle(request, response).catch((error) => {
    console.error("request failed", { error: error.message });
    if (!response.headersSent) json(response, 500, { error: "internal error" });
    else response.destroy();
  });
});

server.listen(port, "0.0.0.0", () => {
  console.log(`ziba browser listening on ${port}`);
});

async function shutdown() {
  server.close();
  await browser.close();
  await proxy.close();
}

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
