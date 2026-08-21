import http from "node:http";
import net from "node:net";

import { requirePublicURL, resolvePublicHost } from "./public-url.mjs";

function writeProxyError(socket, status, message) {
  if (!socket.destroyed) {
    socket.end(
      `HTTP/1.1 ${status}\r\nConnection: close\r\nContent-Type: text/plain\r\nContent-Length: ${Buffer.byteLength(message)}\r\n\r\n${message}`,
    );
  }
}

async function connectPublic(hostname, port) {
  const addresses = await resolvePublicHost(hostname);
  let lastError;

  for (const entry of addresses) {
    try {
      return await new Promise((resolve, reject) => {
        const socket = net.connect({
          host: entry.address,
          family: entry.family,
          port,
        });
        socket.setTimeout(10_000, () => {
          socket.destroy(new Error(`connection to ${hostname}:${port} timed out`));
        });
        socket.once("connect", () => {
          socket.setTimeout(0);
          resolve(socket);
        });
        socket.once("error", reject);
      });
    } catch (error) {
      lastError = error;
    }
  }

  throw lastError ?? new Error(`host ${hostname} has no reachable addresses`);
}

function handleHTTP(request, response) {
  void (async () => {
    const target = await requirePublicURL(request.url);
    if (target.protocol !== "http:") {
      throw new Error("HTTPS proxy requests must use CONNECT");
    }

    const addresses = await resolvePublicHost(target.hostname);
    const pinned = addresses[0];
    const upstream = http.request(
      {
        hostname: target.hostname,
        port: target.port || 80,
        path: `${target.pathname}${target.search}`,
        method: request.method,
        headers: { ...request.headers, host: target.host },
        lookup: (_hostname, _options, callback) =>
          callback(null, pinned.address, pinned.family),
      },
      (upstreamResponse) => {
        response.writeHead(upstreamResponse.statusCode ?? 502, upstreamResponse.headers);
        upstreamResponse.pipe(response);
      },
    );
    upstream.once("error", (error) => {
      if (!response.headersSent) {
        response.writeHead(502, { "Content-Type": "text/plain; charset=utf-8" });
      }
      response.end(error.message);
    });
    request.pipe(upstream);
  })().catch((error) => {
    if (!response.headersSent) {
      response.writeHead(403, { "Content-Type": "text/plain; charset=utf-8" });
    }
    response.end(error.message);
  });
}

function handleConnect(request, clientSocket, head) {
  void (async () => {
    const target = await requirePublicURL(`https://${request.url}/`);
    const port = Number(target.port || 443);
    const upstream = await connectPublic(target.hostname, port);

    clientSocket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
    if (head.length > 0) upstream.write(head);
    upstream.pipe(clientSocket);
    clientSocket.pipe(upstream);
    upstream.once("error", () => clientSocket.destroy());
    clientSocket.once("error", () => upstream.destroy());
  })().catch((error) => writeProxyError(clientSocket, "403 Forbidden", error.message));
}

export async function createSafeProxy() {
  const server = http.createServer(handleHTTP);
  server.on("connect", handleConnect);
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();

  return {
    url: `http://127.0.0.1:${address.port}`,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}
