const VIDEO_MODELS = {
  "doubao-seedance-2-0-260128": { resolutions: ["480p", "720p", "1080p", "4k"], maxDuration: 15 },
  "doubao-seedance-2-0-fast-260128": { resolutions: ["480p", "720p"], maxDuration: 15 },
  "doubao-seedance-2-0-mini-260615": { resolutions: ["480p", "720p"], maxDuration: 15 },
  "doubao-seedance-2-5-260628": { resolutions: ["480p", "720p", "1080p"], maxDuration: 30 },
};
const MODELS = Object.keys(VIDEO_MODELS);
const ALL_RESOLUTIONS = ["480p", "720p", "1080p", "4k"];
const DEFAULT_PROFILE = { resolutions: ALL_RESOLUTIONS, maxDuration: 30 };

function usageSchema(profile) {
  return {
    tokens: {
      type: "number",
      unit: "token",
      description: { en: "Video generation token unit price", zh: "视频生成 Token 单价" },
    },
    output_seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    reference_seconds: {
      type: "number",
      unit: "second",
      description: { en: "Reference video unit price", zh: "参考视频单价" },
    },
    resolution: {
      enum: profile.resolutions,
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
    video_input: {
      enum: ["none", "video"],
      enumLabels: {
        none: { en: "No reference video", zh: "无参考视频" },
        video: { en: "With reference video", zh: "有参考视频" },
      },
      description: { en: "Reference video input", zh: "参考视频输入" },
    },
  };
}

function usageExamples(profile) {
  return [{
    label: "720p · " + profile.maxDuration + "s",
    facts: {
      tokens: profile.maxDuration * 21600,
      output_seconds: profile.maxDuration,
      reference_seconds: 0,
      resolution: "720p",
      video_input: "none",
    },
  }];
}

export const meta = {
  apiVersion: 1,
  key: "maitoken",
  name: "渠道 3",
  icon: "Doubao.Color",
  description: {
    en: "Ark-compatible Seedance generation and Seedance video editing through MAI Token",
    zh: "通过 MAI Token 调用 Ark 兼容 Seedance 生成和 Seedance 视频编辑",
  },
  version: "1.1.4",
  author: { name: "QuantumNous" },
  baseUrl: "https://api.mai-token.com",
  models: MODELS,
  fetchMode: "per_task",
  requiredCapabilities: ["duration-auto@1"],
  usageSchema: usageSchema(DEFAULT_PROFILE),
  usageExamples: usageExamples(DEFAULT_PROFILE),
  usageProfiles: MODELS.map(function (model) {
    return { models: [model], schema: usageSchema(VIDEO_MODELS[model]), examples: usageExamples(VIDEO_MODELS[model]) };
  }),
  routes: [
    { method: "POST", path: "/api/v3/contents/generations/tasks", type: "submit", decode: "createArkTask", render: "arkTaskCreated", models: MODELS },
    { method: "GET", path: "/api/v3/contents/generations/tasks/:task_id", type: "query", render: "arkTaskStatus" },
  ],
  protocols: [{ name: "openai_video", models: MODELS }],
};

function trimmed(value) {
  return String(value || "").trim();
}

function object(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function publicStatus(status) {
  const names = {
    NOT_START: "queued",
    SUBMITTED: "queued",
    QUEUED: "queued",
    IN_PROGRESS: "in_progress",
    SUCCESS: "completed",
    FAILURE: "failed",
  };
  return names[String(status || "").toUpperCase()] || "queued";
}

function httpsURLs(value, name, required) {
  if (value === undefined || value === null) {
    if (required) throw new Error(name + " is required");
    return [];
  }
  if (!Array.isArray(value) || (required && value.length === 0)) throw new Error(name + " must be a non-empty array");
  for (const url of value) {
    if (!trimmed(url).startsWith("https://")) throw new Error(name + " must contain public HTTPS URLs");
  }
  return value;
}

function modelProfile(model) {
  return VIDEO_MODELS[trimmed(model)] || DEFAULT_PROFILE;
}

function validateResolution(value, model) {
  const profile = modelProfile(model);
  const resolution = trimmed(value || "720p").toLowerCase();
  if (!profile.resolutions.includes(resolution)) {
    throw new Error("resolution must be one of: " + profile.resolutions.join(", "));
  }
  return resolution;
}

function validateDuration(value, model) {
  if (value === undefined || value === null || value === "") return;
  const duration = Number(value);
  const maximum = modelProfile(model).maxDuration;
  if (!Number.isInteger(duration) || (duration !== -1 && (duration < 4 || duration > maximum))) {
    throw new Error("duration must be -1 or an integer between 4 and " + maximum);
  }
}

function referenceItems(value) {
  return Array.isArray(value) ? value : value === undefined || value === null || value === "" ? [] : [value];
}

function referenceURL(item) {
  if (typeof item === "string") return trimmed(item);
  item = object(item);
  for (const value of [item.url, item.video_url, item.image_url, item.audio_url]) {
    if (typeof value === "string" && trimmed(value)) return trimmed(value);
    const nested = object(value);
    if (trimmed(nested.url)) return trimmed(nested.url);
  }
  return "";
}

function validateReferenceURLs(value, name) {
  for (const item of referenceItems(value)) {
    const url = referenceURL(item);
    if (!url || !url.startsWith("https://")) throw new Error(name + " must contain public HTTPS URLs");
  }
}

function referenceType(item) {
  item = object(item);
  return trimmed(item.type || item.kind).toLowerCase();
}

function referenceVideoCount(payload) {
  let count = referenceItems(payload.reference_videos).length + referenceItems(payload.video_urls).length;
  for (const item of referenceItems(payload.references)) {
    const type = referenceType(item);
    if (type === "video" || type === "video_url") count++;
  }
  if (Array.isArray(payload.content)) {
    for (const item of payload.content) if (referenceType(item) === "video_url") count++;
  }
  return count;
}

function hasReference(payload) {
  for (const key of ["reference_image_urls", "reference_videos", "reference_audios", "video_urls", "image_urls", "references"]) {
    if (referenceItems(payload[key]).length > 0) return true;
  }
  if (trimmed(payload.first_image_url || payload.last_image_url)) return true;
  return Array.isArray(payload.content) && payload.content.some(function (item) { return referenceType(item) !== "text"; });
}

function arkMediaItem(type, role, url) {
  const item = { type: type, role: role };
  item[type] = { url: url };
  return item;
}

function arkContent(payload) {
  const content = [{ type: "text", text: trimmed(payload.prompt) }];
  const media = [];
  const explicitFields = [
    "reference_image_urls", "reference_videos", "reference_audios", "video_urls", "image_urls",
    "references", "first_image_url", "last_image_url",
  ];
  if (payload.content !== undefined && payload.content !== null) {
    if (!Array.isArray(payload.content)) throw new Error("metadata.content must be an array");
    if (explicitFields.some(function (key) { return payload[key] !== undefined && payload[key] !== null; })) {
      throw new Error("metadata.content cannot be combined with reference_* or first/last image fields");
    }
    for (const raw of payload.content) {
      const item = object(raw);
      const type = referenceType(item);
      if (type === "text") continue;
      const url = referenceURL(item);
      if (!url || !url.startsWith("https://")) throw new Error("metadata.content must contain public HTTPS URLs");
      let role = trimmed(item.role).toLowerCase();
      if (!role && type === "image_url") role = "reference_image";
      if (!role && type === "video_url") role = "reference_video";
      if (!role && type === "audio_url") role = "reference_audio";
      if ((type === "image_url" && ["reference_image", "first_frame", "last_frame"].includes(role)) ||
          (type === "video_url" && role === "reference_video") ||
          (type === "audio_url" && role === "reference_audio")) {
        media.push(arkMediaItem(type, role, url));
      } else {
        throw new Error("metadata.content type and role do not match");
      }
    }
  } else {
    const fields = [
      ["reference_image_urls", "image_url", "reference_image"],
      ["image_urls", "image_url", "reference_image"],
      ["reference_videos", "video_url", "reference_video"],
      ["video_urls", "video_url", "reference_video"],
      ["reference_audios", "audio_url", "reference_audio"],
    ];
    for (const field of fields) {
      for (const value of referenceItems(payload[field[0]])) {
        const url = referenceURL(value);
        if (!url || !url.startsWith("https://")) throw new Error(field[0] + " must contain public HTTPS URLs");
        media.push(arkMediaItem(field[1], field[2], url));
      }
    }
    for (const value of referenceItems(payload.references)) {
      const type = referenceType(value);
      const url = referenceURL(value);
      if (!url || !url.startsWith("https://")) throw new Error("references must contain public HTTPS URLs");
      if (type === "image" || type === "image_url") media.push(arkMediaItem("image_url", "reference_image", url));
      else if (type === "video" || type === "video_url") media.push(arkMediaItem("video_url", "reference_video", url));
      else if (type === "audio" || type === "audio_url") media.push(arkMediaItem("audio_url", "reference_audio", url));
      else throw new Error("references type is not supported");
    }
    if (trimmed(payload.first_image_url)) {
      validateReferenceURLs([payload.first_image_url], "first_image_url");
      media.push(arkMediaItem("image_url", "first_frame", trimmed(payload.first_image_url)));
    }
    if (trimmed(payload.last_image_url)) {
      validateReferenceURLs([payload.last_image_url], "last_image_url");
      media.push(arkMediaItem("image_url", "last_frame", trimmed(payload.last_image_url)));
    }
  }

  const roles = media.map(function (item) { return item.role; });
  const firstFrames = roles.filter(function (role) { return role === "first_frame"; }).length;
  const lastFrames = roles.filter(function (role) { return role === "last_frame"; }).length;
  const ordinary = roles.filter(function (role) { return role !== "first_frame" && role !== "last_frame"; });
  if (firstFrames > 1 || lastFrames > 1) throw new Error("only one first_frame and one last_frame are allowed");
  if (lastFrames && !firstFrames) throw new Error("last_frame requires first_frame");
  if ((firstFrames || lastFrames) && ordinary.length) throw new Error("first/last frames cannot be combined with ordinary reference media");
  if (roles.includes("reference_audio") && !roles.includes("reference_image") && !roles.includes("reference_video")) {
    throw new Error("reference audio requires at least one reference image or video");
  }
  return content.concat(media);
}

function normalizedVideoPayload(body, model) {
  const metadata = object(body.metadata);
  const payload = Object.assign({}, metadata, body, { model: model });
  delete payload.metadata;
  if (payload.duration === undefined && payload.seconds !== undefined) payload.duration = payload.seconds;
  delete payload.seconds;
  if (payload.ratio === undefined && payload.aspect_ratio !== undefined) payload.ratio = payload.aspect_ratio;
  delete payload.aspect_ratio;
  payload.resolution = validateResolution(payload.resolution, model);
  validateDuration(payload.duration, model);
  return payload;
}

function videoURL(data) {
  data = object(data);
  const content = object(data.content);
  const result = object(data.result);
  if (trimmed(content.video_url)) return trimmed(content.video_url);
  if (trimmed(result.url)) return trimmed(result.url);
  if (trimmed(data.video_url)) return trimmed(data.video_url);
  if (Array.isArray(data.data) && data.data.length > 0) {
    const first = object(data.data[0]);
    return trimmed(first.video_url || first.url);
  }
  return "";
}

function taskState(task) {
  return object(task && task.state);
}

function taskData(task) {
  return object(task && task.data);
}

function publicVideoTask(ctx, task) {
  const data = taskData(task);
  const output = Object.assign({}, data, {
    id: task.task_id,
    task_id: task.task_id,
    object: "video",
    status: publicStatus(task.status),
    progress: data.progress === undefined ? task.progress : data.progress,
  });
  if (task.status === "SUCCESS") {
    const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
    const url = artifact && trimmed(artifact.url) ? trimmed(artifact.url) : videoURL(data);
    if (url) {
      output.video_url = url;
      output.result = Object.assign({}, object(data.result), { url: url });
    }
  }
  if (task.status === "FAILURE") {
    output.error = object(data.error).message ? data.error : { message: task.fail_reason || "task failed" };
  }
  return output;
}

function arkStatus(task) {
  const data = taskData(task);
  const output = Object.assign({}, data, { id: task.task_id });
  delete output.task_id;
  if (typeof output.status !== "string") {
    const status = publicStatus(task.status);
    output.status = status === "in_progress" ? "running" : status === "completed" ? "succeeded" : status;
  }
  if (task.status === "SUCCESS") {
    const url = videoURL(data);
    if (url) output.content = Object.assign({}, object(data.content), { video_url: url });
  }
  if (task.status === "FAILURE" && (output.status === "queued" || output.status === "running")) {
    output.status = "failed";
    output.error = { code: "InternalServiceError", message: task.fail_reason || "task failed" };
  }
  return output;
}

export const native = {
  createArkTask: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
    const body = ctx.body.value;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
    if (!trimmed(body.model)) throw new Error("model is required");
    if (body.callback_url !== undefined && body.callback_url !== null) throw new Error("callback_url is not supported; query the task instead");
    if (!Array.isArray(body.content) || body.content.length === 0) throw new Error("content is required");
    const hasVideo = body.content.some(function (item) { return object(item).type === "video_url"; });
    return {
      kind: "submit",
      model: trimmed(body.model),
      action: hasVideo ? "image_to_video" : "text_to_video",
      requestBody: { transport: "ark", payload: Object.assign({}, body) },
    };
  },
  arkTaskCreated: function (ctx, task) {
    const output = Object.assign({}, taskData(task), { id: task.task_id });
    delete output.task_id;
    return output;
  },
  arkTaskStatus: function (ctx, task) {
    return arkStatus(task);
  },
  error: function (ctx, error) {
    return { error: { code: error.code || "upstream_error", message: error.message } };
  },
};

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("MAI Token video generation requires a JSON request");
      const body = ctx.body.value;
      if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
      if (!trimmed(body.prompt)) throw new Error("prompt is required");
      const payload = normalizedVideoPayload(body, ctx.model);
      if (body.mode !== undefined && body.mode !== "video_edit") throw new Error("mode must be video_edit when provided");
      if (body.contract_version !== undefined && body.contract_version !== "video-v1") throw new Error("contract_version must be video-v1");
      if (payload.generate_audio !== undefined && typeof payload.generate_audio !== "boolean") throw new Error("generate_audio must be a boolean");
      let transport = "ark";
      if (body.mode === "video_edit") {
        const videos = httpsURLs(body.video_urls, "video_urls", true);
        if (videos.length !== 1) throw new Error("video_urls must contain exactly one video");
        if (payload.image_urls !== undefined) httpsURLs(payload.image_urls, "image_urls", false);
        payload.contract_version = "video-v1";
        transport = "video_edit";
      } else {
        payload.content = arkContent(payload);
        if (payload.content.some(function (item) { return item.role === "first_frame" || item.role === "last_frame"; })) {
          payload.ratio = "adaptive";
        }
        for (const key of [
          "prompt", "mode", "contract_version", "reference_image_urls", "reference_videos", "reference_audios",
          "video_urls", "image_urls", "references", "first_image_url", "last_image_url",
        ]) delete payload[key];
      }
      return {
        kind: "submit",
        model: ctx.model,
        action: hasReference(payload) ? "image_to_video" : "text_to_video",
        requestBody: {
          transport: transport,
          payload: payload,
        },
      };
    },
    render: function (ctx, task) {
      return publicVideoTask(ctx, task);
    },
  },
};

export function buildSubmitRequest(ctx) {
  const request = object(ctx.requestBody);
  const payload = Object.assign({}, object(request.payload), { model: ctx.upstreamModel || ctx.model });
  const ark = request.transport === "ark";
  return {
    url: trimmed(ctx.baseUrl).replace(/\/+$/, "") + (ark ? "/api/v3/contents/generations/tasks" : "/v1/videos"),
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: payload,
    action: ctx.action,
  };
}

export function parseSubmitResponse(ctx, response) {
  const body = object(response.body);
  const taskId = trimmed(body.id || body.task_id);
  if (!taskId) throw new Error("task_id is empty");
  const request = object(ctx.requestBody);
  return { taskId: taskId, taskData: body, state: { transport: request.transport || "video-v1" } };
}

export function buildQueryRequest(ctx) {
  const ark = taskState(ctx).transport === "ark";
  const root = trimmed(ctx.baseUrl).replace(/\/+$/, "");
  const path = ark ? "/api/v3/contents/generations/tasks/" : "/v1/videos/";
  return {
    url: root + path + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  body = object(body);
  const status = String(body.status || "").toLowerCase();
  if (status === "queued" || status === "pending") return { status: "QUEUED", progress: String(body.progress === undefined ? 10 : body.progress) };
  if (status === "running" || status === "processing" || status === "in_progress") return { status: "IN_PROGRESS", progress: String(body.progress === undefined ? 50 : body.progress) };
  if (status === "succeeded" || status === "completed") return { status: "SUCCESS", progress: "100%", url: videoURL(body) };
  if (status === "failed" || status === "expired" || status === "cancelled") {
    return { status: "FAILURE", progress: "100%", reason: object(body.error).message || body.fail_reason || status };
  }
  return { status: "UNKNOWN", reason: "unrecognized status: " + status };
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS" || !videoURL(taskData(task))) return [];
  return [{ key: "video", type: "video", mimeType: "video/mp4" }];
}

export function buildContentRequest(ctx) {
  let url = videoURL(taskData(ctx));
  if (!url) throw new Error("artifact_not_found");
  const base = trimmed(ctx.baseUrl).replace(/\/+$/, "");
  if (url.startsWith("/")) url = base + url;
  if (url === base || url.startsWith(base + "/") || url.startsWith(base + "?") || url.startsWith(base + "#")) {
    return { url: url, method: ctx.clientRequest.method, headers: { Authorization: "Bearer " + ctx.apiKey } };
  }
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

function boundedSeconds(value, fallback) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 && number <= 3600 ? number : fallback;
}

function tokenRate(resolution) {
  if (resolution === "480p") return 9607.5;
  if (resolution === "1080p") return 48600;
  if (resolution === "4k") return 194400;
  return 21600;
}

function arkVideoCount(content) {
  if (!Array.isArray(content)) return 0;
  return content.filter(function (item) { return object(item).type === "video_url"; }).length;
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const request = object(ctx.requestBody);
  const payload = object(request.payload);
  const profile = modelProfile(ctx.model);
  const resolution = validateResolution(payload.resolution, ctx.model);
  let outputSeconds;
  let referenceSeconds;
  if (request.transport === "video_edit") {
    outputSeconds = profile.maxDuration;
    referenceSeconds = profile.maxDuration;
  } else {
    outputSeconds = boundedSeconds(payload.duration, profile.maxDuration);
    const referenceCount = request.transport === "ark" ? arkVideoCount(payload.content) : referenceVideoCount(payload);
    referenceSeconds = Math.min(referenceCount, 3) * profile.maxDuration;
  }
  return {
    tokens: Math.min((outputSeconds + referenceSeconds) * tokenRate(resolution), 2147483647),
    output_seconds: outputSeconds,
    reference_seconds: referenceSeconds,
    resolution: resolution,
    video_input: referenceSeconds > 0 ? "video" : "none",
  };
}

export function extractUsageOnComplete(task, taskResult, body) {
  body = object(body);
  const status = String(body.status || "").toLowerCase();
  if (status !== "succeeded" && status !== "completed") return {};
  const billing = object(body.billing);
  const usage = object(body.usage);
  const facts = {};
  let tokens = Number(billing.billing_tokens);
  if (!Number.isFinite(tokens) || tokens < 0) tokens = Number(usage.completion_tokens);
  if (!Number.isFinite(tokens) || tokens < 0) tokens = Number(usage.total_tokens);
  if (Number.isFinite(tokens) && tokens >= 0) facts.tokens = Math.min(tokens, 2147483647);
  const outputSeconds = Number(billing.output_seconds);
  if (Number.isFinite(outputSeconds) && outputSeconds >= 0 && outputSeconds <= 3600) facts.output_seconds = outputSeconds;
  const referenceSeconds = Number(billing.reference_seconds);
  if (Number.isFinite(referenceSeconds) && referenceSeconds >= 0 && referenceSeconds <= 3600) facts.reference_seconds = referenceSeconds;
  const content = object(body.content);
  const resolution = trimmed(billing.resolution || content.resolution || body.resolution).toLowerCase();
  if (ALL_RESOLUTIONS.includes(resolution)) facts.resolution = resolution;
  return facts;
}
