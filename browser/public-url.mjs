import dns from "node:dns/promises";
import { BlockList, isIP } from "node:net";

const blockedIPv4 = new BlockList();
const blockedIPv6 = new BlockList();

for (const [address, prefix] of [
  ["0.0.0.0", 8],
  ["10.0.0.0", 8],
  ["100.64.0.0", 10],
  ["127.0.0.0", 8],
  ["169.254.0.0", 16],
  ["172.16.0.0", 12],
  ["192.0.0.0", 24],
  ["192.0.2.0", 24],
  ["192.168.0.0", 16],
  ["198.18.0.0", 15],
  ["198.51.100.0", 24],
  ["203.0.113.0", 24],
  ["224.0.0.0", 4],
  ["240.0.0.0", 4],
]) {
  blockedIPv4.addSubnet(address, prefix, "ipv4");
}

for (const [address, prefix] of [
  ["::", 128],
  ["::1", 128],
  ["::ffff:0:0", 96],
  ["100::", 64],
  ["2001:db8::", 32],
  ["fc00::", 7],
  ["fe80::", 10],
  ["ff00::", 8],
]) {
  blockedIPv6.addSubnet(address, prefix, "ipv6");
}

function addressType(address) {
  const version = isIP(address);
  if (version === 4) return "ipv4";
  if (version === 6) return "ipv6";
  return "";
}

export async function resolvePublicHost(hostname, lookup = dns.lookup) {
  hostname = hostname.replace(/^\[|\]$/g, "");
  const literalType = addressType(hostname);
  const addresses = literalType
    ? [{ address: hostname, family: literalType === "ipv4" ? 4 : 6 }]
    : await lookup(hostname, { all: true, verbatim: true });
  if (addresses.length === 0) {
    throw new Error(`host ${hostname} has no addresses`);
  }
  for (const entry of addresses) {
    const type = entry.family === 4 ? "ipv4" : "ipv6";
    const list = type === "ipv4" ? blockedIPv4 : blockedIPv6;
    if (list.check(entry.address, type)) {
      throw new Error(`host ${hostname} resolves to a non-public address`);
    }
  }
  return addresses;
}

export async function requirePublicURL(raw, lookup = dns.lookup) {
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error("URL is invalid");
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error("URL must use HTTP or HTTPS");
  }
  if (parsed.username || parsed.password) {
    throw new Error("URL must not contain credentials");
  }

  await resolvePublicHost(parsed.hostname, lookup);
  return parsed;
}
