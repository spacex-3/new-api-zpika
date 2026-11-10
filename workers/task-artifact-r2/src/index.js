const PROTOCOL_VERSION = "v1";
const MAX_JOB_BYTES = 2 * 1024 * 1024;
const MAX_JOB_AGE_SECONDS = 300;
const MAX_OBJECT_LIFETIME_SECONDS = 7 * 24 * 60 * 60;
const MULTIPART_PART_BYTES = 8 * 1024 * 1024;
const MAX_REDIRECTS = 5;
const OBJECT_KEY_PATTERN = /^new-api\/v1\/\d{4}-\d{2}-\d{2}\/[A-Za-z0-9_-]{43}$/;
const SAFE_SOURCE_HEADERS = new Set(["accept", "authorization", "content-type", "api-key", "x-api-key", "x-goog-api-key"]);
const CREDENTIAL_HEADERS = new Set(["authorization", "api-key", "x-api-key", "x-goog-api-key"]);

const encoder = new TextEncoder();

function json(body, status = 200, extraHeaders = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", ...extraHeaders },
  });
}

function endpointPath(pathname) {
  for (const endpoint of ["/v1/status", "/v1/ingest", "/v1/media"]) {
    if (pathname === endpoint || pathname.endsWith(endpoint)) return endpoint;
  }
  return "";
}

function base64URL(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function decodeBase64URL(value) {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]+$/.test(value)) throw new Error("invalid base64url");
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((value.length + 3) % 4);
  const binary = atob(padded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

async function hmac(secret, message) {
  const key = await crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(message)));
}

async function sha256Hex(bytes) {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function timingSafeEqual(left, right) {
  if (left.length !== right.length) return false;
  let mismatch = 0;
  for (let index = 0; index < left.length; index++) mismatch |= left[index] ^ right[index];
  return mismatch === 0;
}

function ingestSecret(env, keyId) {
  if (keyId === env.INGEST_HMAC_KEY_ID) return env.INGEST_HMAC_SECRET;
  if (keyId === env.INGEST_HMAC_PREVIOUS_KEY_ID) return env.INGEST_HMAC_PREVIOUS_SECRET;
  return "";
}

async function authenticateJob(request, env, body) {
  const keyId = request.headers.get("X-Zpika-Artifact-Key-Id") || "";
  const timestampText = request.headers.get("X-Zpika-Artifact-Timestamp") || "";
  const nonce = request.headers.get("X-Zpika-Artifact-Nonce") || "";
  const signatureText = request.headers.get("X-Zpika-Artifact-Signature") || "";
  const secret = ingestSecret(env, keyId);
  const timestamp = Number(timestampText);
  const now = Math.floor(Date.now() / 1000);
  if (!secret || !Number.isInteger(timestamp) || Math.abs(now - timestamp) > MAX_JOB_AGE_SECONDS || !/^[A-Za-z0-9_-]{20,64}$/.test(nonce)) return false;
  let actual;
  try {
    actual = decodeBase64URL(signatureText);
  } catch {
    return false;
  }
  const digest = await sha256Hex(body);
  const message = [PROTOCOL_VERSION, request.method, new URL(request.url).pathname, timestampText, nonce, digest].join("\n");
  return timingSafeEqual(actual, await hmac(secret, message));
}

function validateObjectKey(value) {
  return typeof value === "string" && OBJECT_KEY_PATTERN.test(value);
}

function isBlockedHostname(hostname) {
  const host = String(hostname || "").toLowerCase().replace(/^\[|\]$/g, "");
  if (!host || host === "localhost" || host.endsWith(".localhost") || host.endsWith(".local") || host.includes(":")) return true;
  const parts = host.split(".");
  if (parts.length !== 4 || parts.some((part) => !/^\d+$/.test(part))) return false;
  const octets = parts.map(Number);
  if (octets.some((part) => part < 0 || part > 255)) return true;
  return octets[0] === 0 || octets[0] === 10 || octets[0] === 127 || octets[0] >= 224 ||
    (octets[0] === 169 && octets[1] === 254) || (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31) ||
    (octets[0] === 192 && octets[1] === 168) || (octets[0] === 100 && octets[1] >= 64 && octets[1] <= 127);
}

function parseSourceURL(value) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("invalid source URL");
  }
  if (!["http:", "https:"].includes(parsed.protocol) || parsed.username || parsed.password || parsed.hash || isBlockedHostname(parsed.hostname)) {
    throw new Error("rejected source URL");
  }
  return parsed;
}

function sourceHeaders(input) {
  if (input === undefined) return new Headers();
  if (!input || typeof input !== "object" || Array.isArray(input)) throw new Error("invalid source headers");
  const headers = new Headers();
  for (const [name, value] of Object.entries(input)) {
    const lower = name.toLowerCase();
    if (!SAFE_SOURCE_HEADERS.has(lower) || typeof value !== "string" || value.length > 8192 || /[\r\n\0]/.test(value)) {
      throw new Error("rejected source header");
    }
    headers.set(name, value);
  }
  return headers;
}

function sourceBody(value) {
  if (!value) return undefined;
  if (typeof value !== "string" || value.length > 2 * 1024 * 1024) throw new Error("invalid source body");
  const binary = atob(value);
  if (binary.length > 1024 * 1024) throw new Error("source body is too large");
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

function validateIngestJob(job, env) {
  const now = Math.floor(Date.now() / 1000);
  const maxBytes = Number(job && job.max_bytes);
  const configuredMax = Number(env.MAX_ARTIFACT_BYTES || 1024 * 1024 * 1024);
  if (!job || job.version !== PROTOCOL_VERSION || !validateObjectKey(job.object_key) ||
      !Number.isInteger(job.expires_at) || job.expires_at <= now || job.expires_at > now + MAX_OBJECT_LIFETIME_SECONDS ||
      !["image", "video", "audio", "file"].includes(job.artifact_type) ||
      !Number.isInteger(maxBytes) || maxBytes <= 0 || maxBytes > configuredMax ||
      !job.source || typeof job.source !== "object") {
    throw new Error("invalid ingest job");
  }
  if (job.mime_type !== undefined && (typeof job.mime_type !== "string" || job.mime_type.length > 255 || /[\r\n\0]/.test(job.mime_type))) {
    throw new Error("invalid artifact MIME type");
  }
  const url = parseSourceURL(job.source.url);
  const method = String(job.source.method || "GET").toUpperCase();
  const headers = sourceHeaders(job.source.headers);
  const body = sourceBody(job.source.body_base64);
  if (!["GET", "POST"].includes(method)) throw new Error("invalid source method");
  if (job.source.credentialless && (Array.from(headers).length !== 0 || body || method !== "GET")) throw new Error("invalid credentialless source");
  if (!job.source.credentialless) {
    let origin;
    try {
      origin = new URL(job.source.credential_origin).origin;
    } catch {
      throw new Error("invalid credential origin");
    }
    if (origin !== url.origin) throw new Error("credential origin mismatch");
  }
  return { ...job, max_bytes: maxBytes, source: { url, method, headers, body, credentialless: Boolean(job.source.credentialless) } };
}

async function fetchSource(source) {
  let url = source.url;
  let method = source.method;
  let headers = source.headers;
  let body = source.body;
  let credentialsStripped = false;
  for (let redirects = 0; redirects <= MAX_REDIRECTS; redirects++) {
    const response = await fetch(url, { method, headers, body, redirect: "manual" });
    if (![301, 302, 303, 307, 308].includes(response.status)) return response;
    if (redirects === MAX_REDIRECTS) throw new Error("too many source redirects");
    const location = response.headers.get("Location");
    if (!location) throw new Error("source redirect has no location");
    const next = parseSourceURL(new URL(location, url).toString());
    if (url.protocol === "https:" && next.protocol !== "https:") throw new Error("source redirect downgraded HTTPS");
    if (next.origin !== url.origin && !credentialsStripped) {
      const safe = new Headers();
      const accept = headers.get("Accept");
      if (accept) safe.set("Accept", accept);
      headers = safe;
      credentialsStripped = true;
    }
    if ([301, 302, 303].includes(response.status) && method !== "GET") {
      method = "GET";
      body = undefined;
      headers.delete("Content-Type");
    } else if (body && next.origin !== url.origin) {
      throw new Error("source body redirect crossed origin");
    }
    url = next;
  }
  throw new Error("unreachable redirect state");
}

function artifactContentType(job, response) {
  const upstream = (response.headers.get("Content-Type") || "").split(";", 1)[0].trim().toLowerCase();
  const expected = String(job.mime_type || "").split(";", 1)[0].trim().toLowerCase();
  const contentType = upstream || expected || "application/octet-stream";
  const family = job.artifact_type + "/";
  if (job.artifact_type !== "file" && !contentType.startsWith(family) && contentType !== "application/octet-stream") {
    throw new Error("source MIME type does not match artifact type");
  }
  return contentType;
}

async function multipartPut(bucket, key, body, options, maxBytes) {
  const upload = await bucket.createMultipartUpload(key, options);
  const reader = body.getReader();
  const parts = [];
  let chunks = [];
  let buffered = 0;
  let total = 0;
  let partNumber = 1;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      const chunk = value instanceof Uint8Array ? value : new Uint8Array(value);
      total += chunk.byteLength;
      if (total > maxBytes) throw new Error("source body exceeds artifact limit");
      chunks.push(chunk);
      buffered += chunk.byteLength;
      if (buffered < MULTIPART_PART_BYTES) continue;
      const part = new Uint8Array(buffered);
      let offset = 0;
      for (const item of chunks) {
        part.set(item, offset);
        offset += item.byteLength;
      }
      parts.push(await upload.uploadPart(partNumber++, part));
      chunks = [];
      buffered = 0;
    }
    if (buffered > 0 || parts.length === 0) {
      const part = new Uint8Array(buffered);
      let offset = 0;
      for (const item of chunks) {
        part.set(item, offset);
        offset += item.byteLength;
      }
      parts.push(await upload.uploadPart(partNumber, part));
    }
    if (total === 0) throw new Error("source body is empty");
    await upload.complete(parts);
    return total;
  } catch (error) {
    await upload.abort();
    throw error;
  } finally {
    reader.releaseLock();
  }
}

async function storeSource(bucket, job, response) {
  if (response.status !== 200 || !response.body) throw new Error("source did not return a complete artifact");
  const lengthText = response.headers.get("Content-Length");
  const length = lengthText && /^\d+$/.test(lengthText) ? Number(lengthText) : null;
  if (length !== null && (!Number.isSafeInteger(length) || length < 0 || length > job.max_bytes)) throw new Error("source body exceeds artifact limit");
  const contentType = artifactContentType(job, response);
  const options = {
    httpMetadata: { contentType },
    customMetadata: { expires_at: String(job.expires_at), artifact_type: job.artifact_type },
  };
  if (length !== null) {
    await bucket.put(job.object_key, response.body, options);
  } else {
    await multipartPut(bucket, job.object_key, response.body, options, job.max_bytes);
  }
  const stored = await bucket.head(job.object_key);
  if (!stored || stored.size > job.max_bytes) {
    await bucket.delete(job.object_key);
    throw new Error("stored artifact failed validation");
  }
  return stored;
}

function objectExpired(object) {
  const expiresAt = Number(object && object.customMetadata && object.customMetadata.expires_at);
  return !Number.isInteger(expiresAt) || expiresAt <= Math.floor(Date.now() / 1000);
}

async function handleStatus(job, env) {
  if (!job || job.version !== PROTOCOL_VERSION || !validateObjectKey(job.object_key) || !Number.isInteger(job.expires_at)) return json({ error: "invalid_request" }, 400);
  const object = await env.ARTIFACT_BUCKET.head(job.object_key);
  if (!object) return json({ ready: false }, 404);
  if (objectExpired(object)) {
    await env.ARTIFACT_BUCKET.delete(job.object_key);
    return json({ ready: false }, 404);
  }
  return json({ ready: true, size: object.size, mime_type: object.httpMetadata && object.httpMetadata.contentType || "" });
}

async function handleIngest(rawJob, env) {
  let job;
  try {
    job = validateIngestJob(rawJob, env);
  } catch {
    return json({ error: "invalid_request" }, 400);
  }
  const existing = await env.ARTIFACT_BUCKET.head(job.object_key);
  if (existing && !objectExpired(existing)) return json({ ready: true, size: existing.size }, 200);
  if (existing) await env.ARTIFACT_BUCKET.delete(job.object_key);
  try {
    const response = await fetchSource(job.source);
    const stored = await storeSource(env.ARTIFACT_BUCKET, job, response);
    return json({ ready: true, size: stored.size }, 201);
  } catch {
    return json({ error: "ingest_failed" }, 502);
  }
}

function mediaMessage(key, expires) {
  return [PROTOCOL_VERSION, key, String(expires)].join("\n");
}

async function verifyMediaURL(url, env) {
  const expires = Number(url.searchParams.get("expires"));
  if (!Number.isInteger(expires) || expires <= Math.floor(Date.now() / 1000)) return null;
  let key;
  let actual;
  try {
    key = new TextDecoder().decode(decodeBase64URL(url.searchParams.get("key") || ""));
    actual = decodeBase64URL(url.searchParams.get("sig") || "");
  } catch {
    return null;
  }
  if (!validateObjectKey(key)) return null;
  const expected = await hmac(env.MEDIA_HMAC_SECRET || "", mediaMessage(key, expires));
  return timingSafeEqual(actual, expected) ? { key, expires } : null;
}

function applyCORS(request, env, headers) {
  const origin = request.headers.get("Origin");
  if (!origin) return;
  const allowed = String(env.ALLOWED_ORIGINS || "").split(",").map((value) => value.trim()).filter(Boolean);
  if (allowed.includes(origin)) {
    headers.set("Access-Control-Allow-Origin", origin);
    headers.set("Vary", "Origin");
  }
}

function applyObjectHeaders(object, headers) {
  if (object.writeHttpMetadata) object.writeHttpMetadata(headers);
  headers.set("ETag", object.httpEtag || object.etag);
  headers.set("Accept-Ranges", "bytes");
  headers.set("X-Content-Type-Options", "nosniff");
  headers.set("Content-Security-Policy", "default-src 'none'; sandbox");
}

async function handleMedia(request, env) {
  const verified = await verifyMediaURL(new URL(request.url), env);
  if (!verified) return json({ error: "not_found" }, 404);
  const head = await env.ARTIFACT_BUCKET.head(verified.key);
  if (!head) return json({ error: "not_found" }, 404);
  if (objectExpired(head)) {
    await env.ARTIFACT_BUCKET.delete(verified.key);
    return json({ error: "expired" }, 410);
  }
  const headers = new Headers();
  applyObjectHeaders(head, headers);
  applyCORS(request, env, headers);
  headers.set("Cache-Control", "public, max-age=" + Math.max(0, Math.min(verified.expires - Math.floor(Date.now() / 1000), 900)));
  if (request.headers.get("If-None-Match") === head.httpEtag || request.headers.get("If-None-Match") === head.etag) return new Response(null, { status: 304, headers });
  if (request.method === "HEAD") {
    headers.set("Content-Length", String(head.size));
    return new Response(null, { status: 200, headers });
  }
  const object = await env.ARTIFACT_BUCKET.get(verified.key, { range: request.headers });
  if (!object) return json({ error: "not_found" }, 404);
  applyObjectHeaders(object, headers);
  if (object.range) {
    headers.set("Content-Range", `bytes ${object.range.offset}-${object.range.offset + object.range.length - 1}/${object.size}`);
    headers.set("Content-Length", String(object.range.length));
    return new Response(object.body, { status: 206, headers });
  }
  headers.set("Content-Length", String(object.size));
  return new Response(object.body, { status: 200, headers });
}

async function handleSignedJob(request, env) {
  const declaredLength = Number(request.headers.get("Content-Length") || 0);
  if (declaredLength > MAX_JOB_BYTES) return json({ error: "request_too_large" }, 413);
  const body = new Uint8Array(await request.arrayBuffer());
  if (body.byteLength > MAX_JOB_BYTES) return json({ error: "request_too_large" }, 413);
  if (!await authenticateJob(request, env, body)) return json({ error: "not_found" }, 404);
  let job;
  try {
    job = JSON.parse(new TextDecoder().decode(body));
  } catch {
    return json({ error: "invalid_request" }, 400);
  }
  const path = endpointPath(new URL(request.url).pathname);
  if (path === "/v1/status") return handleStatus(job, env);
  if (path === "/v1/ingest") return handleIngest(job, env);
  return json({ error: "not_found" }, 404);
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const endpoint = endpointPath(url.pathname);
    if (request.method === "POST" && (endpoint === "/v1/status" || endpoint === "/v1/ingest")) return handleSignedJob(request, env);
    if ((request.method === "GET" || request.method === "HEAD") && endpoint === "/v1/media") return handleMedia(request, env);
    if (request.method === "OPTIONS" && endpoint === "/v1/media") {
      const headers = new Headers({ "Access-Control-Allow-Methods": "GET, HEAD, OPTIONS", "Access-Control-Allow-Headers": "Range, If-Range, If-None-Match" });
      applyCORS(request, env, headers);
      return new Response(null, { status: 204, headers });
    }
    return json({ error: "not_found" }, 404);
  },
};

export { authenticateJob, endpointPath, fetchSource, hmac, isBlockedHostname, mediaMessage, timingSafeEqual, validateIngestJob, validateObjectKey };
