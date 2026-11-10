# New API R2 Artifact Storage Plan

## Objective

Store generated task artifacts in Cloudflare R2 for 24 hours, hide provider
URLs from clients, and keep artifact byte transfer off the New API ARM host in
normal operation.

This plan applies to task-plugin artifacts, including Cangyuan images and
Doubao videos. It must not change ordinary relay routing, task billing, model
pricing, or synchronous providers that do not create a durable task.

Canvas storage is explicitly out of scope for this implementation.

## Architecture Decision

R2 cannot fetch an arbitrary HTTP URL by itself. Use a Cloudflare Worker with
an R2 binding as the primary transfer path:

```text
provider --media bytes--> Cloudflare Worker --stream--> R2
                                ^
                                |
                     signed pull job (metadata only)
                                |
                            New API (ARM)

client --> signed New API artifact URL --> signed Worker media URL --> R2
```

The ARM host sends only a small authenticated pull-job request. The Worker
downloads the provider object and writes it to R2, so image and video bytes do
not pass through ARM unless the Worker path fails.

Keep the existing protected ARM proxy as a compatibility fallback. A storage
failure must not turn a successfully generated and billed task into a failed
task.

## Verified Current State

- Task artifact URLs are signed gateway capability URLs.
- Dashboard artifact URLs are same-origin when `TaskPublicAddress` is empty.
- Artifact content is currently fetched from the provider and streamed through
  New API for every request.
- Artifact responses use `Cache-Control: private, no-store`, so Cloudflare does
  not normally cache them.
- `TaskArtifactStore` and reserved S3/R2 configuration fields exist, but S3
  mode deliberately falls back to upstream proxying.
- No artifact bytes are persisted by the current implementation.
- Cangyuan native image task queries currently return provider `data[].url`
  values. The dashboard uses gateway artifact URLs, but the native query API
  does not rewrite those result URLs yet.
- The restored `doubao@1.3.1` plugin distinguishes signed external URLs from
  protected channel URLs. For a channel URL it refreshes the task result and
  sends `Authorization: Bearer <channel key>` when downloading the artifact.
- A live read-only check against the latest successful `zexitongxue` Seedance
  task confirmed that the refreshed video URL is protected: unauthenticated
  `HEAD` and ranged `GET` returned `401`, while the same requests with the
  channel Bearer credential returned `200` and `206` respectively.

The Worker pull contract must therefore support both credentialless artifacts
and credentialed artifacts. Passing only the provider URL is insufficient.

## Storage Boundary

Use a dedicated R2 Standard bucket for New API artifacts. Do not share runtime
credentials with the Canvas application.

Recommended object key shape:

```text
new-api/v1/<finish-date>/<opaque-task-artifact-key>
```

The object key must be derived with an HMAC or random capability and must not
contain an upstream URL, credential, prompt, username, or raw provider task ID.
The bucket remains private and is accessed only through the Worker binding.

Store only artifact bytes and safe metadata:

- content type;
- byte length when known;
- creation and logical expiration timestamps;
- an opaque gateway artifact identifier when required for operations.

Do not store channel credentials, provider URLs, prompts, or request bodies in
R2 object metadata.

## Worker Pull Job

New API resolves the active plugin's `buildContentRequest` at task completion
or first artifact access. It then sends the Worker a short-lived pull job over
HTTPS containing only what the Worker needs:

```text
object key
source URL
GET method
allowlisted source headers, when required
credential forwarding boundary
expected artifact type / MIME family
maximum byte size
logical expiration
issued-at, expiry, nonce
HMAC signature and key id
```

For Cangyuan's signed public image URL, the header set is normally empty. For
`zexitongxue`, the job carries the channel Bearer header in the HTTPS request
body because the video endpoint rejects an unauthenticated download.

Credential handling is mandatory:

- The Worker request body and headers must never be logged.
- Worker observability must record only job id, object key, source hostname,
  status, byte count, duration, and sanitized error category.
- Channel credentials exist only in New API memory and Worker request memory.
- Credentials must never be written to R2, KV, Queue payloads, Durable Objects,
  URLs, task data, or error text.
- Only an explicit allowlist of safe request headers may cross the boundary;
  reject cookies, proxy credentials, host headers, and hop-by-hop headers.
- Credentialed redirects may retain credentials only within the exact trusted
  source host/port boundary. Any cross-host redirect strips credentials
  permanently; HTTPS-to-HTTP redirects are rejected.
- Jobs expire within a few minutes and are protected against replay by their
  nonce or deterministic idempotency key.
- The Worker authenticates New API with a dedicated shared HMAC secret. A
  Cloudflare Global API Key is neither required nor accepted.

The Worker must validate the HMAC before parsing or fetching the source URL,
reject private/reserved destinations and URL userinfo, cap redirects, enforce
timeouts and byte limits, and accept only `http` or `https` sources.

## Persistence Flow

When a task first becomes `SUCCESS`:

1. Enumerate artifacts through the task plugin's existing `listArtifacts` hook.
2. Start a bounded background persistence attempt; do not delay billing
   settlement or the terminal task status.
3. Resolve the provider request through the existing `buildContentRequest`
   hook, including URL refresh and channel authentication where required.
4. Submit an authenticated pull job to the Worker. The ARM host keeps the small
   HTTP control connection open but never reads the artifact body.
5. The Worker fetches the provider object and streams it into R2.
6. Validate status, content type and final size before publishing the object as
   ready. An incomplete multipart upload is aborted.
7. Use deterministic object keys and idempotent writes so duplicate polling,
   retries and multiple gateway nodes converge on one object.

An upstream response with a trustworthy `Content-Length` can stream directly
to an R2 `put`. An unknown-length response uses bounded multipart chunks; it
must not buffer a whole image or video in Worker memory. Worker memory remains
bounded well below the platform limit.

The first implementation may use a direct authenticated Worker request rather
than Cloudflare Queues. This avoids queue latency and avoids placing channel
credentials in a durable queue payload. A New API restart may lose an
in-flight attempt; the first later artifact access retries idempotently.

## Delivery Contract

1. Public API, dashboard, Canvas, Responses, and video surfaces return the
   existing signed New API artifact URL. This preserves the active reverse
   proxy hostname and avoids exposing provider URLs.
2. The signed gateway endpoint validates task ownership/capability and the
   24-hour logical lifetime.
3. If the R2 object is ready, the endpoint redirects to a short-lived signed
   Worker media URL. The Worker validates the signature and serves R2 with
   `GET`, `HEAD`, validators, and range support.
4. If persistence is still running or failed, the gateway may temporarily use
   the existing protected upstream proxy and schedule one bounded retry. It
   must never redirect the client to the provider URL.
5. Cangyuan completed image task responses rewrite `data[].url` to matching
   signed gateway artifact URLs. Synchronous OpenAI image behavior is unchanged
   unless the result is retained as a durable task.
6. `b64_json` remains an explicit client-requested inline response and is not
   used as the R2 storage representation.

The gateway URL remains stable for the task lifetime. The Worker delivery URL
is short-lived and is an implementation detail.

## Expiration And Deletion

- Logical expiration is exactly 24 hours after the task finish time.
- The gateway and Worker return `410 Gone` after logical expiration even when
  R2 physical deletion has not completed.
- Configure an R2 lifecycle rule for the `new-api/` prefix as the durable
  physical cleanup mechanism.
- Worker cache TTL must never exceed the remaining logical lifetime.
- R2 lifecycle deletion is asynchronous; logical expiration is the
  authoritative access rule.

## Configuration

New API needs only the Worker control endpoint and shared signing secrets; R2
credentials stay inside Cloudflare as a Worker binding:

```text
TASK_ARTIFACT_STORE_MODE=worker_r2
TASK_ARTIFACT_WORKER_URL=https://artifact-ingest.example.workers.dev
TASK_ARTIFACT_WORKER_HMAC_KEY_ID=v1
TASK_ARTIFACT_WORKER_HMAC_SECRET=
TASK_ARTIFACT_MEDIA_BASE_URL=https://media.zpika.com
TASK_ARTIFACT_MEDIA_HMAC_SECRET=
TASK_ARTIFACT_STORE_TTL_SECONDS=86400
```

Use separate ingest and media HMAC secrets so a leaked public media signature
cannot authorize an ingest job. Support overlapping key ids during rotation.
Secrets must never be returned by status APIs, written to logs, committed, or
placed in plugin state.

Cloudflare configuration consists of:

- one private R2 Standard bucket;
- one Worker with an R2 binding;
- ingest and media secrets stored as Worker secrets;
- a controlled media hostname such as `media.zpika.com`;
- an R2 lifecycle rule deleting the `new-api/` prefix after one day.

Bucket access keys and a Cloudflare Global API Key are not needed on ARM.

## Failure And Rollback

- Worker or R2 failure must not change task billing or mark a generated task as
  failed.
- Before expiration, a missing R2 object falls back to the protected ARM proxy
  and schedules a retry.
- Provider URLs remain server-side and are never used as the client fallback.
- Credentialed sources such as `zexitongxue` retain their existing refresh and
  Bearer behavior in the ARM fallback.
- `TASK_ARTIFACT_STORE_MODE=upstream` restores current behavior without a
  database migration.
- Switching back to the official image leaves R2 objects to their lifecycle
  rule and does not change task rows.

## Verification

Backend and Worker tests must cover:

- HMAC validation, expiry, replay protection, key rotation and idempotency;
- source URL validation, SSRF blocking and redirect credential stripping;
- credentialless Cangyuan fetches;
- credentialed `zexitongxue` fetches, including refreshed URLs and ranged
  provider responses;
- known-length direct streaming and unknown-length multipart streaming;
- image and video transfers without whole-file buffering;
- size, timeout, MIME, incomplete upload and retry behavior;
- R2 hit, miss, ingest failure and ARM fallback;
- exact logical expiration and `410 Gone`;
- Worker `GET`, `HEAD`, `Range`, ETag and content-type behavior;
- Cangyuan asynchronous query URL rewriting;
- Doubao video and Responses artifact URLs;
- no provider URL or channel credential in public responses, R2 metadata, or
  logs;
- upstream mode behavior unchanged;
- SQLite, MySQL and PostgreSQL compatibility if persisted task metadata or
  schema behavior changes.

Deployment verification must use the isolated `newapi-zpika-test` Compose
project first. Formal `new-api-vip`, `postgres-vip`, and `redis-vip` containers
must remain untouched until the test domain passes Cangyuan image,
`zexitongxue` video, range, expiration, credential-leak, and rollback checks.

## Acceptance Criteria

- Provider bytes bypass ARM in normal Worker/R2 operation.
- One provider download and one R2 object per artifact in normal operation.
- Completed Cangyuan image APIs no longer expose provider URLs.
- Protected `zexitongxue` Seedance videos are copied successfully without
  exposing or persisting the channel credential.
- Doubao task-log, Responses and video content continue to work.
- Customers receive a controlled Zpika URL and do not need provider network
  access.
- New API does not relay subsequent R2-backed downloads through ARM.
- Artifacts become inaccessible after 24 hours and are physically removed by
  R2 lifecycle cleanup.
- Billing, task settlement, ordinary Codex/text relay and unrelated channels
  remain unchanged.
