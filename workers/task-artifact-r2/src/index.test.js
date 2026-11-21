import { describe, expect, test } from "bun:test";
import worker, { endpointPath, fetchSource, hmac, isBlockedHostname, mediaMessage, timingSafeEqual, validateIngestJob, validateObjectKey } from "./index.js";
import { buildContentRequest } from "../../../plugins/tasks/az/plugin.js";

function base64URL(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function signedIngest(job, env) {
  const body = JSON.stringify(job);
  const timestamp = String(Math.floor(Date.now() / 1000));
  const nonce = base64URL(crypto.getRandomValues(new Uint8Array(18)));
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(body)));
  const hex = Array.from(digest, (value) => value.toString(16).padStart(2, "0")).join("");
  const signature = base64URL(await hmac(env.INGEST_HMAC_SECRET, ["v1", "POST", "/v1/ingest", timestamp, nonce, hex].join("\n")));
  return worker.fetch(new Request("https://worker.example/v1/ingest", {
    method: "POST", body, headers: {
      "X-Zpika-Artifact-Key-Id": env.INGEST_HMAC_KEY_ID,
      "X-Zpika-Artifact-Timestamp": timestamp,
      "X-Zpika-Artifact-Nonce": nonce,
      "X-Zpika-Artifact-Signature": signature,
    },
  }), env);
}

test("AZ signed ingest extracts Base64 task results into R2 without exposing provider credentials", async () => {
  const originalFetch = globalThis.fetch;
  const key = "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
  const expires = Math.floor(Date.now() / 1000) + 3600;
  try {
    for (const shape of ["base64", "data URL", "object"]) {
      const entry = shape === "data URL" ? { url: "data:image/png;base64,aW1hZ2U=" } : { b64_json: "aW1hZ2U=" };
      const taskData = { status: "completed", result: { data: shape === "object" ? entry : [entry] } };
      const descriptor = buildContentRequest({
        baseUrl: "https://provider.example", apiKey: "channel-secret", upstreamTaskId: "azimg_private",
        artifactKey: "image-1", data: taskData, clientRequest: { method: "GET" },
      });
      let stored;
      const bucket = {
        async head() { return stored || null; },
        async put(objectKey, stream, options) {
          expect(objectKey).toBe(key);
          const bytes = await new Response(stream).text();
          expect(bytes).toBe("image");
          expect(options.httpMetadata.contentType).toBe("image/png");
          expect(JSON.stringify(options)).not.toContain("channel-secret");
          stored = { size: bytes.length, ...options };
        },
      };
      let fetches = 0;
      globalThis.fetch = async (url, init) => {
        fetches++;
        expect(String(url)).toBe("https://provider.example/v1/images/tasks/azimg_private");
        expect(new Headers(init.headers).get("Authorization")).toBe("Bearer channel-secret");
        return Response.json(taskData);
      };
      const env = { ARTIFACT_BUCKET: bucket, INGEST_HMAC_KEY_ID: "v1", INGEST_HMAC_SECRET: "test-worker-secret", MAX_ARTIFACT_BYTES: "1024" };
      const job = {
        version: "v1", object_key: key, expires_at: expires, artifact_type: "image", mime_type: descriptor.mimeType, max_bytes: 1024,
        source: { url: descriptor.url, method: descriptor.method, headers: descriptor.headers, credentialless: false,
          credential_origin: "https://provider.example", content_path: descriptor.contentPath },
      };
      const response = await signedIngest(job, env);
      expect(response.status).toBe(201);
      expect(await response.json()).toEqual({ ready: true, size: 5 });
      expect((await signedIngest(job, env)).status).toBe(200);
      expect(fetches).toBe(1);
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("inline R2 ingestion rejects failed, unfinished, missing, invalid, and oversized payloads", async () => {
  const originalFetch = globalThis.fetch;
  let writes = 0;
  const env = {
    INGEST_HMAC_KEY_ID: "v1", INGEST_HMAC_SECRET: "test-worker-secret", MAX_ARTIFACT_BYTES: "1024",
    ARTIFACT_BUCKET: { async head() { return null; }, async put() { writes++; } },
  };
  const job = {
    version: "v1", object_key: "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    expires_at: Math.floor(Date.now() / 1000) + 3600, artifact_type: "image", mime_type: "image/png", max_bytes: 4,
    source: { url: "https://provider.example/task", method: "GET", credentialless: true, content_path: ["result", "data", 0, "b64_json"] },
  };
  try {
    for (const reply of [
      { status: "processing" },
      { status: "failed", error: { code: "insufficient_quota" } },
      { status: "completed", result: { data: [] } },
      { status: "completed", result: { data: [{ b64_json: "%%%" }] } },
      { status: "completed", result: { data: [{ b64_json: "aW1hZ2U=" }] } },
      { status: "completed", result: { data: [{ b64_json: "A".repeat(2 * 1024 * 1024) }] } },
    ]) {
      globalThis.fetch = async () => Response.json(reply);
      const response = await signedIngest(job, env);
      expect(response.status).toBe(502);
      expect(await response.json()).toEqual({ error: "ingest_failed" });
    }
    expect(writes).toBe(0);
    for (const content_path of [[], [null], [-1], [1.5], Array(33).fill("a")]) {
      expect(() => validateIngestJob({ ...job, source: { ...job.source, content_path } }, env)).toThrow("content path");
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
});

describe("artifact worker security helpers", () => {
  test("accepts only opaque New API object keys", () => {
    expect(validateObjectKey("new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")).toBe(true);
    expect(validateObjectKey("new-api/v1/2026-11-10/../secret")).toBe(false);
    expect(validateObjectKey("other/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")).toBe(false);
  });

  test("blocks local and private network source hosts", () => {
    for (const host of ["localhost", "api.localhost", "127.0.0.1", "10.0.0.1", "172.20.1.2", "192.168.1.1", "169.254.1.1", "::1"]) {
      expect(isBlockedHostname(host)).toBe(true);
    }
    expect(isBlockedHostname("tmp.cangyuansuanli.cn")).toBe(false);
    expect(isBlockedHostname("zexitongxue.com")).toBe(false);
  });

  test("media signatures bind object key and expiry", async () => {
    const secret = "0123456789abcdef0123456789abcdef";
    const key = "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
    const first = await hmac(secret, mediaMessage(key, 200));
    const same = await hmac(secret, mediaMessage(key, 200));
    const changed = await hmac(secret, mediaMessage(key, 201));
    expect(timingSafeEqual(first, same)).toBe(true);
    expect(timingSafeEqual(first, changed)).toBe(false);
  });

  test("accepts credentialless HTTPS sources without headers", () => {
    const now = Math.floor(Date.now() / 1000);
    const job = validateIngestJob({
      version: "v1",
      object_key: "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      expires_at: now + 3600,
      artifact_type: "image",
      max_bytes: 1024,
      source: { url: "https://tmp.cangyuansuanli.cn/image.png", method: "GET", credentialless: true },
    }, { MAX_ARTIFACT_BYTES: "2048" });
    expect(job.source.credentialless).toBe(true);
  });

  test("recognizes endpoints behind a path-prefixed reverse proxy", () => {
    expect(endpointPath("/v1/status")).toBe("/v1/status");
    expect(endpointPath("/artifact-store/v1/ingest")).toBe("/v1/ingest");
    expect(endpointPath("/artifact-store/v1/media")).toBe("/v1/media");
    expect(endpointPath("/artifact-store/v1/media/extra")).toBe("");
  });

  test("strips credentials permanently on cross-origin redirects", async () => {
    const originalFetch = globalThis.fetch;
    const seen = [];
    globalThis.fetch = async (url, init) => {
      seen.push({ url: String(url), headers: new Headers(init.headers) });
      if (seen.length === 1) return new Response(null, { status: 302, headers: { Location: "https://cdn.example/video.mp4" } });
      return new Response("video", { status: 200, headers: { "Content-Type": "video/mp4" } });
    };
    try {
      const response = await fetchSource({
        url: new URL("https://provider.example/protected"), method: "GET", body: undefined, credentialless: false,
        headers: new Headers({ Accept: "video/mp4", Authorization: "Bearer channel-secret" }),
      });
      expect(response.status).toBe(200);
      expect(seen).toHaveLength(2);
      expect(seen[0].headers.get("Authorization")).toBe("Bearer channel-secret");
      expect(seen[1].headers.get("Authorization")).toBeNull();
      expect(seen[1].headers.get("Accept")).toBe("video/mp4");
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  test("serves signed R2 media as full, range, and attachment responses", async () => {
    const secret = "abcdef0123456789abcdef0123456789";
    const key = "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
    const expires = Math.floor(Date.now() / 1000) + 300;
    const signature = base64URL(await hmac(secret, mediaMessage(key, expires)));
    const encodedKey = base64URL(new TextEncoder().encode(key));
    const object = {
      size: 5,
      etag: "etag-1",
      httpEtag: '"etag-1"',
      httpMetadata: { contentType: "video/mp4" },
      customMetadata: { expires_at: String(expires + 300) },
      writeHttpMetadata(headers) { headers.set("Content-Type", "video/mp4"); },
    };
    const bucket = {
      async head(requestedKey) {
        expect(requestedKey).toBe(key);
        return object;
      },
      async get(requestedKey, options) {
        expect(requestedKey).toBe(key);
        if (options) {
          expect(options.range.get("Range")).toBe("bytes=1-3");
          return { ...object, body: new Response("ide").body, range: { offset: 1, length: 3 } };
        }
        return { ...object, body: new Response("video").body, range: { offset: 0, length: 5 } };
      },
    };
    const request = new Request(`https://media.example/artifacts/v1/media?key=${encodedKey}&expires=${expires}&sig=${signature}`, {
      headers: { Range: "bytes=1-3" },
    });
    const response = await worker.fetch(request, { ARTIFACT_BUCKET: bucket, MEDIA_HMAC_SECRET: secret });
    expect(response.status).toBe(206);
    expect(response.headers.get("Content-Range")).toBe("bytes 1-3/5");
    expect(response.headers.get("Content-Type")).toBe("video/mp4");
    expect(response.headers.get("Content-Security-Policy")).toBeNull();
    expect(await response.text()).toBe("ide");

    const fullResponse = await worker.fetch(new Request(
      `https://media.example/artifacts/v1/media?key=${encodedKey}&expires=${expires}&sig=${signature}`,
    ), { ARTIFACT_BUCKET: bucket, MEDIA_HMAC_SECRET: secret });
    expect(fullResponse.status).toBe(200);
    expect(fullResponse.headers.get("Content-Range")).toBeNull();
    expect(await fullResponse.text()).toBe("video");

    const downloadResponse = await worker.fetch(new Request(
      `https://media.example/artifacts/v1/media?key=${encodedKey}&expires=${expires}&sig=${signature}&download=1&name=video`,
    ), { ARTIFACT_BUCKET: bucket, MEDIA_HMAC_SECRET: secret });
    expect(downloadResponse.status).toBe(200);
    expect(downloadResponse.headers.get("Content-Disposition")).toBe('attachment; filename="video.mp4"');

    const imageObject = {
      ...object,
      httpMetadata: { contentType: "image/png" },
      customMetadata: { ...object.customMetadata, artifact_type: "image" },
      writeHttpMetadata(headers) { headers.set("Content-Type", "image/png"); },
    };
    const imageBucket = {
      async head() { return imageObject; },
      async get() { return { ...imageObject, body: new Response("image").body }; },
    };
    const imageDownload = await worker.fetch(new Request(
      `https://media.example/artifacts/v1/media?key=${encodedKey}&expires=${expires}&sig=${signature}&download=1&name=image-1`,
    ), { ARTIFACT_BUCKET: imageBucket, MEDIA_HMAC_SECRET: secret });
    expect(imageDownload.headers.get("Content-Disposition")).toBe('attachment; filename="image-1.png"');
  });
});
