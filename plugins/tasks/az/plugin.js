// Channel model_mapping may expose temporary aliases such as a -az suffix.
const MODELS = ["nano-banana-2.1", "gemini-3-pro-image-preview", "gemini-3.1-flash-image-preview"];
const MAX_BODY_BYTES = 24 * 1024 * 1024;
const IMAGE_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"];

export const meta = {
  apiVersion: 1,
  key: "az",
  name: "AZ Images",
  icon: "Gemini.Color",
  version: "1.0.0",
  author: { name: "QuantumNous" },
  description: { en: "Asynchronous AZ image generation and editing", zh: "AZ 异步图片生成与编辑" },
  baseUrl: "https://cnmoai.com",
  models: MODELS,
  fetchMode: "per_task",
  upstreams: ["vendor", "new_api"],
  usageSchema: {
    image_count: { type: "number", unit: "count", description: { en: "Image generation unit price", zh: "图片生成单价" } },
    resolution: { enum: ["auto", "1K", "2K", "4K"], description: { en: "Output image resolution", zh: "输出图片分辨率" } },
  },
  routes: [
    { method: "POST", path: "/az/v1/images/generations", type: "submit", decode: "createImage", render: "imageTask" },
    { method: "POST", path: "/az/v1/images/edits", type: "submit", decode: "editImage", render: "imageTask" },
    { method: "GET", path: "/v1/images/generations/:task_id", type: "query", render: "imageTask" },
    { method: "GET", path: "/v1/images/edits/:task_id", type: "query", render: "imageTask" },
  ],
  protocols: [{ name: "openai_image", models: MODELS, supports: ["sync", "background"] }],
};

function object(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function text(value) {
  return typeof value === "string" ? value.trim() : "";
}

function apiRoot(ctx) {
  return text(ctx.baseUrl).replace(/\/+$/, "").replace(/\/v1$/, "");
}

function imageEndpoint(action) {
  return action === "image_edit" ? "/v1/images/edits" : "/v1/images/generations";
}

function resolution(size) {
  if (["auto", "1K", "2K", "4K"].includes(size)) return size;
  const match = /^([1-9][0-9]{0,4})x([1-9][0-9]{0,4})$/.exec(size);
  if (!match) throw new Error("size must be auto, 1K, 2K, 4K, or widthxheight");
  const width = Number(match[1]), height = Number(match[2]);
  let left = width, right = height;
  while (right) { const remainder = left % right; left = right; right = remainder; }
  if (!["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"].includes(width / left + ":" + height / left)) {
    throw new Error("size has an unsupported aspect ratio");
  }
  return Math.max(width, height) <= 1024 ? "1K" : Math.max(width, height) <= 2048 ? "2K" : "4K";
}

function jsonBytes(value) {
  // moejs has no browser TextEncoder. Count UTF-8 bytes, including surrogate pairs.
  const json = JSON.stringify(value);
  let bytes = 0;
  for (let i = 0; i < json.length; i++) {
    const code = json.charCodeAt(i);
    if (code < 128) bytes++;
    else if (code < 2048) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && i + 1 < json.length && json.charCodeAt(i + 1) >= 0xdc00 && json.charCodeAt(i + 1) <= 0xdfff) { bytes += 4; i++; }
    else bytes += 3;
  }
  return bytes;
}

function uploadedImage(file) {
  if (!file || !IMAGE_TYPES.includes(file.mimeType) || !Number.isSafeInteger(file.size) || file.size <= 0 || file.size > MAX_BODY_BYTES) {
    throw new Error("reference image file has an unsupported type or size");
  }
  return { __fileRef: file.ref, encoding: "dataUrl", mimeType: file.mimeType, maxBytes: MAX_BODY_BYTES };
}

function inlineImage(value, files) {
  if (value && typeof value === "object") value = value.image_url || value.url;
  if (value && typeof value === "object" && value.__fileRef) {
    return { image_url: uploadedImage(files.find(function (file) { return file.ref === value.__fileRef; })) };
  }
  if (value && typeof value === "object") value = value.url;
  const url = text(value);
  if (!/^data:image\/(png|jpeg|webp|gif);base64,[A-Za-z0-9+/]+={0,2}$/.test(url)) {
    throw new Error("AZ reference images must be PNG, JPEG, WebP, or GIF Base64 data URLs; upload files for larger images");
  }
  const encoded = url.slice(url.indexOf(",") + 1);
  if (encoded.length % 4 !== 0) throw new Error("reference image has invalid Base64 padding");
  return { image_url: url };
}

function decodeImage(ctx) {
  const multipart = ctx.body && ctx.body.kind === "multipart";
  if (!ctx.body || (!multipart && ctx.body.kind !== "json")) throw new Error("JSON or multipart image request required");
  let body = object(ctx.body.value);
  if (multipart) {
    body = {};
    for (const key of Object.keys(ctx.body.fields)) {
      const values = ctx.body.fields[key];
      if (values.length !== 1) throw new Error("image parameters must not be repeated");
      body[key] = values[0];
    }
    if (body.n !== undefined) body.n = Number(body.n);
    if (body["async"] !== undefined) {
      if (body["async"] !== "true" && body["async"] !== "false") throw new Error("async must be a boolean");
      body["async"] = body["async"] === "true";
    }
    if (body["async"] === true && !ctx.nativeAsync) throw new Error("Use /az/v1/images/edits for asynchronous multipart uploads");
  }
  for (const key of Object.keys(body)) {
    if (!["model", "prompt", "size", "n", "response_format", "async", "images", "image", "reference_image_urls"].includes(key)) {
      throw new Error("Unsupported AZ image parameter: " + key);
    }
  }
  if (body.n !== undefined && body.n !== 1) throw new Error("AZ supports n=1; submit separate tasks for multiple images");
  if (body["async"] !== undefined && typeof body["async"] !== "boolean") throw new Error("async must be a boolean");
  if (body.response_format !== undefined && !["url", "b64_json"].includes(body.response_format)) throw new Error("response_format must be url or b64_json");
  const model = text(ctx.model || body.model), prompt = text(body.prompt), size = body.size === undefined ? "auto" : text(body.size);
  if (!model || !prompt) throw new Error("model and a non-empty prompt are required");
  resolution(size);
  const request = { model: model, prompt: prompt, size: size, n: 1, response_format: "b64_json" };
  const images = [];
  for (const key of ["images", "image", "reference_image_urls"]) {
    if (body[key] === undefined) continue;
    const values = Array.isArray(body[key]) ? body[key] : [body[key]];
    for (const value of values) images.push(inlineImage(value, ctx.files || []));
  }
  let fileBytes = 0;
  if (multipart) {
    for (const file of ctx.body.files || []) {
      if (!["image", "image[]", "images", "images[]"].includes(file.field)) throw new Error("Unsupported AZ image file field");
      images.push({ image_url: uploadedImage(file) });
    }
  }
  const files = multipart ? ctx.body.files || [] : ctx.files || [];
  for (const entry of images) {
    if (typeof entry.image_url !== "object") continue;
    const file = files.find(function (candidate) { return candidate.ref === entry.image_url.__fileRef; });
    fileBytes += 4 * Math.ceil(file.size / 3) + file.mimeType.length + 20;
  }
  if (images.length > 10) throw new Error("AZ accepts at most 10 reference images");
  if (ctx.operation === "edit" && images.length === 0) throw new Error("image editing requires at least one reference image");
  if (images.length) request.images = images;
  if (jsonBytes(request) + fileBytes > MAX_BODY_BYTES) throw new Error("AZ image request exceeds the 24 MiB body limit");
  return { kind: "submit", model: model, action: images.length ? "image_edit" : "image_generation", requestBody: request };
}

function imageEntries(data) {
  const result = object(object(data).result);
  const entries = result.data === undefined ? object(data).data : result.data;
  return Array.isArray(entries) ? entries.map(function (entry) { return object(entry); }) : entries && typeof entries === "object" ? [entries] : [];
}

function mimeType(entry) {
  const value = text(entry.b64_json);
  if (value.startsWith("/9j/")) return "image/jpeg";
  if (value.startsWith("UklGR")) return "image/webp";
  if (value.startsWith("R0lGOD")) return "image/gif";
  const match = /^data:(image\/[a-z0-9.+-]+);base64,/.exec(text(entry.url));
  return match ? match[1] : "image/png";
}

function publicImageData(ctx, data) {
  return imageEntries(data).filter(function (entry) { return text(entry.b64_json) || text(entry.url); }).map(function (entry, index) {
    const artifact = object(ctx.artifacts)["image-" + (index + 1)];
    if (artifact && artifact.url) return { url: artifact.url };
    // Inline fallback also works while R2 is unavailable; never present a data URL as an HTTP link.
    if (text(entry.b64_json)) return { b64_json: entry.b64_json };
    if (text(entry.url).startsWith("data:")) return { b64_json: entry.url.slice(entry.url.indexOf(",") + 1) };
    return { url: entry.url };
  });
}

function publicTask(ctx, task) {
  const statuses = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
  const output = { id: task.task_id, task_id: task.task_id, status: statuses[task.status] || "queued", progress: task.progress || "0%" };
  if (task.status === "SUCCESS") output.data = publicImageData(ctx, task.data);
  // BuildTaskPluginView owns quota redaction; do not echo the upstream error snapshot or poll_url.
  if (task.status === "FAILURE") {
    const reason = (task.fail_reason || "Image generation failed").replace(/https?:\/\/[^\s<>]+/gi, "[redacted]").replace(/Bearer\s+\S+|sk-[A-Za-z0-9_-]{8,}/gi, "[redacted]");
    // AZ always retains a submit snapshot. The host omits it only when it
    // redacts an upstream quota failure in BuildTaskPluginView.
    output.error = { code: task.data ? "image_generation_failed" : "upstream_unavailable", message: reason };
  }
  return output;
}

export const native = {
  createImage: function (ctx) { return decodeImage(Object.assign({}, ctx, { operation: "generate", nativeAsync: true })); },
  editImage: function (ctx) { return decodeImage(Object.assign({}, ctx, { operation: "edit", nativeAsync: true })); },
  imageTask: function (ctx, task) { return publicTask(ctx, task); },
  error: function (ctx, error) { return { error: { code: error.code, message: error.message } }; },
};

export const protocols = {
  openai_image: {
    decodeRequest: decodeImage,
    render: function (ctx, task) {
      if (object(object(ctx.body).value)["async"] === true || task.status !== "SUCCESS") return publicTask(ctx, task);
      return { created: object(object(task.data).result).created || task.created_at, data: publicImageData(ctx, task.data) };
    },
  },
};

export function buildSubmitRequest(ctx) {
  const gateway = ctx.upstream && ctx.upstream.kind === "new_api";
  // The generic /v1/tasks/az entry bypasses protocol decoders. Validate here as
  // well, before billing, using the same body that is actually sent upstream.
  const intent = decodeImage({ model: ctx.model, operation: ctx.action === "image_edit" ? "edit" : "generate", body: { kind: "json", value: ctx.requestBody }, files: ctx.files || [] });
  const body = Object.assign({}, intent.requestBody, { model: ctx.upstreamModel || ctx.model });
  if (gateway) body["async"] = true;
  return {
    url: apiRoot(ctx) + (gateway ? "/az" : "") + imageEndpoint(intent.action) + (gateway ? "" : "/async"), method: "POST",
    headers: { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json" }, body: body, action: intent.action,
  };
}

export function parseSubmitResponse(ctx, response) {
  const body = object(response.body), taskId = text(body.task_id || body.id);
  if (!taskId) throw new Error("AZ response has no task ID");
  return { taskId: taskId, taskData: body };
}

export function buildQueryRequest(ctx) {
  const gateway = ctx.upstream && ctx.upstream.kind === "new_api";
  return {
    url: apiRoot(ctx) + (gateway ? "/az" + imageEndpoint(ctx.action) + "/" : "/v1/images/tasks/") + encodeURIComponent(ctx.taskId),
    method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  body = object(body);
  const statuses = { processing: "IN_PROGRESS", queued: "QUEUED", in_progress: "IN_PROGRESS", completed: "SUCCESS", failed: "FAILURE" };
  const status = statuses[text(body.status)] || "UNKNOWN";
  const error = object(body.error);
  // Preserve machine codes even when the message is generic, for the host's quota detection and Bark alert.
  const reason = [text(error.code), text(error.type), text(error.message)].filter(function (value) { return value; }).join(": ");
  if (status === "SUCCESS" && !imageEntries(body).some(function (entry) { return text(entry.b64_json) || text(entry.url); })) {
    return { status: "UNKNOWN" };
  }
  return { status: status, progress: status === "SUCCESS" || status === "FAILURE" ? "100%" : "50%", reason: reason || (status === "FAILURE" ? "AZ image generation failed" : "") };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return {};
  return { image_count: 1, resolution: resolution(text(object(ctx.requestBody).size) || "auto") };
}

export function extractUsageOnComplete(ctx, result, body) {
  if (result.status !== "SUCCESS") return {};
  const images = imageEntries(body);
  const urls = images.filter(function (entry) { return text(entry.url); }).length;
  const base64 = images.filter(function (entry) { return text(entry.b64_json); }).length;
  // AZ accepts n=1. Never increase billing for an inconsistent or split upstream payload.
  return Math.max(urls, base64) === 1 ? { image_count: 1 } : {};
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  return imageEntries(task.data).filter(function (entry) { return text(entry.b64_json) || text(entry.url); }).map(function (entry, index) {
    return { key: "image-" + (index + 1), type: "image", mimeType: mimeType(entry) };
  });
}

export function buildContentRequest(ctx) {
  const match = /^image-([1-9][0-9]*)$/.exec(text(ctx.artifactKey));
  const entries = imageEntries(ctx.data);
  let entry, sourceIndex = 0, imageIndex = 0;
  for (let i = 0; match && i < entries.length; i++) {
    const candidate = entries[i];
    if (!text(candidate.b64_json) && !text(candidate.url)) continue;
    imageIndex++;
    if (imageIndex === Number(match[1])) { entry = candidate; sourceIndex = i; break; }
  }
  if (!entry) throw new Error("artifact_not_found");
  if (!text(entry.b64_json) && /^https:\/\//.test(text(entry.url))) {
    return { url: entry.url, method: ctx.clientRequest.method, credentialless: true };
  }
  const request = buildQueryRequest(Object.assign({}, ctx, { taskId: ctx.upstreamTaskId }));
  // Ask the task endpoint again rather than putting multi-megabyte Base64 in the signed Worker job.
  const result = object(ctx.data).result;
  request.contentPath = result ? ["result", "data"] : ["data"];
  if (Array.isArray(result ? result.data : object(ctx.data).data)) request.contentPath.push(sourceIndex);
  request.contentPath.push(text(entry.b64_json) ? "b64_json" : "url");
  request.mimeType = mimeType(entry);
  return request;
}
