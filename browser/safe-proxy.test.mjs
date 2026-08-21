import assert from "node:assert/strict";
import net from "node:net";
import test from "node:test";

import { createSafeProxy } from "./safe-proxy.mjs";

test("refuses a CONNECT tunnel to a private address", async (t) => {
  const proxy = await createSafeProxy();
  t.after(() => proxy.close());
  const endpoint = new URL(proxy.url);

  const response = await new Promise((resolve, reject) => {
    const socket = net.connect(Number(endpoint.port), endpoint.hostname);
    const chunks = [];
    socket.once("connect", () => {
      socket.write("CONNECT 127.0.0.1:80 HTTP/1.1\r\nHost: 127.0.0.1:80\r\n\r\n");
    });
    socket.on("data", (chunk) => chunks.push(chunk));
    socket.once("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    socket.once("error", reject);
  });

  assert.match(response, /^HTTP\/1\.1 403 Forbidden/);
  assert.match(response, /non-public address/);
});
