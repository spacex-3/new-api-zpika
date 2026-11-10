import { describe, expect, test } from "bun:test";
import worker, { endpointPath, fetchSource, hmac, isBlockedHostname, mediaMessage, timingSafeEqual, validateIngestJob, validateObjectKey } from "./index.js";

function base64URL(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

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

  test("serves signed R2 media with range support behind a path prefix", async () => {
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
        expect(options.range.get("Range")).toBe("bytes=1-3");
        return { ...object, body: new Response("ide").body, range: { offset: 1, length: 3 } };
      },
    };
    const request = new Request(`https://media.example/artifacts/v1/media?key=${encodedKey}&expires=${expires}&sig=${signature}`, {
      headers: { Range: "bytes=1-3" },
    });
    const response = await worker.fetch(request, { ARTIFACT_BUCKET: bucket, MEDIA_HMAC_SECRET: secret });
    expect(response.status).toBe(206);
    expect(response.headers.get("Content-Range")).toBe("bytes 1-3/5");
    expect(response.headers.get("Content-Type")).toBe("video/mp4");
    expect(await response.text()).toBe("ide");
  });
});
