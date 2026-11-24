import { describe, expect, test } from "bun:test";
import * as az from "./tasks/az/plugin.js";
import * as cangyuan from "./tasks/cangyuan/plugin.js";

const alias = "gemini-3-pro-image-preview-az";
const rawModel = "gemini-3-pro-image-preview";
const image = "iVBORw0KGgo=";
const body = { model: alias, prompt: "a cat", size: "1024x1024", n: 1, async: true };
const decode = (value = body, operation = "generate") => az.protocols.openai_image.decodeRequest({
  model: alias, operation, body: { kind: "json", value },
});
const driver = (intent = decode()) => ({
  ...intent, baseUrl: "https://provider.example", apiKey: "channel-secret", upstreamModel: rawModel,
});
const completed = { status: "completed", result: { created: 123, data: [{ b64_json: image }] } };
const view = { task_id: "task_public", status: "SUCCESS", created_at: 100, data: completed };

describe("AZ image tasks", () => {
  test("keeps Cangyuan client modes, aliases, and image query routes", () => {
    expect(az.meta.version).toBe("1.1.0");
    expect(az.meta.baseUrl).toBe("http://az-proxy:8080");
    expect(az.meta.routes.filter((route) => route.type === "query").map((route) => route.path)).toEqual(
      cangyuan.meta.routes.filter((route) => route.path.startsWith("/v1/images/")).map((route) => route.path),
    );
    const intent = decode();
    expect(intent.model).toBe(alias);
    expect(intent.action).toBe("image_generation");
    const submit = az.buildSubmitRequest(driver(intent));
    expect(submit.url).toBe("https://provider.example/v1/images/generations/async");
    expect(submit.body.model).toBe(rawModel);
    expect(submit.body.async).toBeUndefined();
    expect(submit.body.response_format).toBe("b64_json");
    expect(az.meta.models).toContain(rawModel);
    expect(az.meta.protocols.map((claim) => claim.name)).toEqual(["openai_image"]);
    expect(az.buildSubmitRequest({ ...driver(), baseUrl: "https://provider.example/v1/" }).url).toBe(submit.url);
    expect(az.parseSubmitResponse({}, { statusCode: 202, body: { task_id: "azimg_private", status: "processing" } }).taskId).toBe("azimg_private");
    expect(az.buildQueryRequest({ ...driver(), taskId: "azimg_private" }).url).toBe("https://provider.example/v1/images/tasks/azimg_private");
    const async = az.protocols.openai_image.render({ body: { value: body } }, { ...view, status: "SUBMITTED", data: {} });
    expect(async.id).toBe(view.task_id);
    expect(async.status).toBe("queued");
    const sync = az.protocols.openai_image.render({ body: { value: { ...body, async: false } } }, view);
    expect(sync).toEqual({ created: 123, data: [{ b64_json: image }] });
  });

  test("adapts Base64 and multipart references without forwarding unsupported fields", () => {
    const edited = decode({ ...body, images: [{ image_url: `data:image/png;base64,${image}` }] }, "edit");
    expect(edited.action).toBe("image_edit");
    expect(az.buildSubmitRequest(driver(edited)).url).toEndWith("/v1/images/edits/async");
    expect(edited.requestBody.images).toEqual([{ image_url: `data:image/png;base64,${image}` }]);
    const upload = {
      body: { kind: "multipart", fields: { model: [alias], prompt: ["edit"], async: ["true"] },
        files: [{ ref: "request_file:image[]#1", field: "image[]", mimeType: "image/png", size: 12 }] },
    };
    const native = az.native.editImage(upload);
    expect(native.requestBody.images[0].image_url.__fileRef).toBe("request_file:image[]#1");
    expect(native.requestBody.images[0].image_url.encoding).toBe("dataUrl");
    const submittedUpload = az.buildSubmitRequest({ ...driver(native), files: upload.body.files });
    expect(submittedUpload.body.images).toEqual(native.requestBody.images);
    expect(submittedUpload.url).toEndWith("/v1/images/edits/async");
    expect(() => az.protocols.openai_image.decodeRequest({ ...upload, model: alias, operation: "edit" })).toThrow("/az/v1/images/edits");
    expect(() => decode(body, "edit")).toThrow("at least one reference");
    expect(() => decode({ ...body, images: Array(11).fill(`data:image/png;base64,${image}`) })).toThrow("at most 10");
    expect(() => az.native.editImage({ body: { ...upload.body, files: [{ ...upload.body.files[0], size: 19 * 1024 * 1024 }] } })).toThrow("24 MiB");
    for (const extra of [{ mask: "anything" }, { stream: true }, { metadata: { n: 100 } }]) {
      expect(() => decode({ ...body, ...extra })).toThrow("Unsupported AZ image parameter");
    }
  });

  test("passes HTTPS references unchanged through protocol, native, and generic edits", () => {
    const url = "https://cdn.example/reference.png?signature=a%2Bb&expires=123";
    for (const images of [[url], [{ image_url: url }], [{ image_url: { url } }], [{ url }]]) {
      const value = { ...body, quality: "2k", size: "1:1", images };
      const generation = decode(value, "generate");
      expect(generation.action).toBe("image_generation");
      expect(generation.requestBody.images).toEqual([{ image_url: url }]);
      expect(az.buildSubmitRequest({ ...driver(generation), requestBody: value }).url).toEndWith("/v1/images/generations/async");
      const edit = decode(value, "edit");
      expect(edit.action).toBe("image_edit");
      expect(edit.requestBody.images).toEqual([{ image_url: url }]);
      const submit = az.buildSubmitRequest({ ...driver(edit), requestBody: value });
      expect(submit.url).toEndWith("/v1/images/edits/async");
      expect(submit.body.images).toEqual([{ image_url: url }]);
      expect(submit.body.size).toBe("2048x2048");
      expect(az.native.editImage({ body: { kind: "json", value } }).requestBody.images).toEqual([{ image_url: url }]);
      expect(az.native.createImage({ body: { kind: "json", value } }).action).toBe("image_generation");
    }
    expect(decode({ ...body, reference_image_urls: [url] }, "edit").requestBody.images).toEqual([{ image_url: url }]);
    expect(decode({ ...body, reference_images: [{ url }] }, "edit").requestBody.images).toEqual([{ image_url: url }]);
    expect(decode({ ...body, reference_image_url: url }, "edit").requestBody.images).toEqual([{ image_url: url }]);
    expect(decode({ ...body, image_url: { url } }, "edit").requestBody.images).toEqual([{ image_url: url }]);
    const legacy = "data:image/png;base64," + image;
    const cangyuanShape = { ...body, images: [{ url, image_url: legacy }] };
    expect(decode(cangyuanShape, "edit").requestBody.images).toEqual([{ image_url: url }]);
    expect(az.buildSubmitRequest({ ...driver(), requestBody: cangyuanShape }).body.images).toEqual([{ image_url: url }]);
    expect(decode({ ...body, images: [{ url: " ", image_url: url }] }, "edit").requestBody.images).toEqual([{ image_url: url }]);
    for (const url of ["http://cdn.example/image.png", "https://", "https:///image.png", "https://cdn.example/a b.png", "https://user:password@cdn.example/image.png", "https://cdn.example\\image.png"]) {
      expect(() => decode({ ...body, images: [{ image_url: url }] }, "edit")).toThrow("HTTPS URLs");
      expect(() => az.buildSubmitRequest({ ...driver(), requestBody: { ...body, images: [url] } })).toThrow("HTTPS URLs");
    }
    expect(() => decode({ ...body, images: Array(11).fill(url) }, "edit")).toThrow("at most 10");
  });

  test("bounds billing quantities and derives resolution from the submitted size", () => {
    for (const n of [0, -1, 2, 1.5, 2147483647, "1", null]) {
      expect(() => decode({ ...body, n })).toThrow("n=1");
      expect(() => az.buildSubmitRequest({ ...driver(), requestBody: { ...body, n } })).toThrow("n=1");
    }
    for (const [size, resolution] of [["auto", "auto"], ["1024x1024", "1K"], ["1536x1024", "2K"], ["4096x4096", "4K"], ["2K", "2K"]]) {
      expect(az.extractUsage({ ...driver(decode({ ...body, size })), usagePurpose: "facts" })).toEqual({ image_count: 1, resolution });
    }
    expect(() => decode({ ...body, size: "1024x1000" })).toThrow("aspect ratio");
    expect(() => decode({ ...body, size: "0x1024" })).toThrow("size must be");
    expect(az.extractUsage({ ...driver(), usagePurpose: "billing_ratios" })).toEqual({});
    for (const data of [[{ b64_json: image }], { url: "https://cdn.example/image.png" }, [{ url: "https://cdn.example/image.png" }, { b64_json: image }]]) {
      expect(az.extractUsageOnComplete({}, { status: "SUCCESS" }, { result: { data } })).toEqual({ image_count: 1 });
    }
    expect(az.extractUsageOnComplete({}, { status: "FAILURE" }, completed)).toEqual({});
    expect(az.extractUsageOnComplete({}, { status: "SUCCESS" }, { result: { data: [{ revised_prompt: "no image" }] } })).toEqual({});
  });

  test("accepts client output format hints on JSON, multipart, and generic tasks", () => {
    for (const output_format of ["png", "jpeg", "jpg", "webp"]) {
      const value = { ...body, output_format, images: [{ image_url: `data:image/png;base64,${image}` }] };
      const edit = decode(value, "edit");
      expect(edit.action).toBe("image_edit");
      expect(edit.requestBody).toEqual(decode({ ...value, output_format: undefined }, "edit").requestBody);
      expect(value.output_format).toBe(output_format);
      const request = az.buildSubmitRequest({ ...driver(edit), requestBody: value });
      expect(request.body.output_format).toBeUndefined();
      expect(request.body.response_format).toBe("b64_json");
      expect(az.extractUsage({ ...driver(edit), usagePurpose: "facts" })).toEqual({ image_count: 1, resolution: "1K" });
      const multipart = az.protocols.openai_image.decodeRequest({
        model: alias, operation: "edit", body: {
          kind: "multipart", fields: { model: [alias], prompt: ["edit"], n: ["1"], output_format: [output_format] },
          files: [{ ref: "request_file:image", field: "image", mimeType: "image/png", size: 12 }],
        },
      });
      expect(multipart.action).toBe("image_edit");
      expect(multipart.requestBody.output_format).toBeUndefined();
    }
    expect(az.native.createImage({ body: { kind: "json", value: { ...body, output_format: "png" } } }).action).toBe("image_generation");
    for (const output_format of [null, 1, {}, "gif", "pdf"]) {
      expect(() => decode({ ...body, output_format })).toThrow("output_format must be");
    }
    expect(() => decode({ ...body, output_format: "png", n: 2 })).toThrow("n=1");
    expect(() => az.buildSubmitRequest({ ...driver(), requestBody: { ...body, output_format: "png", n: 2 } })).toThrow("n=1");
    // A format hint cannot relabel JPEG bytes as PNG.
    const jpegTask = { ...view, data: { status: "completed", result: { data: [{ b64_json: "/9j/AA==" }] } } };
    expect(az.listArtifacts(jpegTask)[0].mimeType).toBe("image/jpeg");
  });

  test("maps Cangyuan image quality and aspect-ratio fields to AZ size", () => {
    const value = { ...body, size: "1:1", quality: "2k", output_format: "png", images: [{ image_url: `data:image/png;base64,${image}` }] };
    const edit = decode(value, "edit");
    expect(edit.requestBody).toEqual({
      model: alias, prompt: "a cat", size: "2048x2048", n: 1, response_format: "b64_json",
      images: [{ image_url: `data:image/png;base64,${image}` }],
    });
    expect(edit.requestBody.quality).toBeUndefined();
    expect(edit.requestBody.output_format).toBeUndefined();
    expect(az.extractUsage({ ...driver(edit), usagePurpose: "facts" })).toEqual({ image_count: 1, resolution: "2K" });
    expect(az.buildSubmitRequest(driver(edit)).body.size).toBe("2048x2048");
    const generic = { ...driver(edit), requestBody: value, usagePurpose: "facts" };
    expect(az.buildSubmitRequest(generic).body.size).toBe("2048x2048");
    expect(az.extractUsage(generic)).toEqual({ image_count: 1, resolution: "2K" });
    for (const [size, quality, want] of [["16:9", "2k", "2048x1152"], ["3:2", "1k", "1023x682"], ["1:1", "4K", "4096x4096"]]) {
      const request = decode({ ...body, size, quality }, "generate");
      expect(request.requestBody.size).toBe(want);
      expect(az.extractUsage({ ...driver(request), usagePurpose: "facts" }).resolution).toBe(quality.toUpperCase());
    }
    for (const quality of ["1k", "2k", "4k"]) {
      for (const size of ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"]) {
        const request = decode({ ...body, size, quality });
        const submitted = az.buildSubmitRequest(driver(request));
        const [width, height] = submitted.body.size.split("x").map(Number);
        const [widthRatio, heightRatio] = size.split(":").map(Number);
        expect(width * heightRatio).toBe(height * widthRatio);
        expect(az.extractUsage({ ...driver(request), usagePurpose: "facts" }).resolution).toBe(quality.toUpperCase());
      }
    }
    const multipart = az.protocols.openai_image.decodeRequest({
      model: alias, operation: "edit", body: {
        kind: "multipart", fields: { model: [alias], prompt: ["edit"], n: ["1"], size: ["1:1"], quality: ["2k"] },
        files: [{ ref: "request_file:image", field: "image", mimeType: "image/jpeg", size: 12 }],
      },
    });
    expect(multipart.requestBody.size).toBe("2048x2048");
    expect(multipart.requestBody.images[0].image_url.__fileRef).toBe("request_file:image");
    for (const quality of ["high", "8k", "", 2, null, {}]) {
      expect(() => decode({ ...body, quality })).toThrow("quality must be");
      expect(() => az.buildSubmitRequest({ ...driver(), requestBody: { ...body, quality } })).toThrow("quality must be");
    }
    expect(() => decode({ ...body, size: "1:1", quality: "2k", n: 2 })).toThrow("n=1");
    expect(() => decode({ ...body, size: "1:100", quality: "2k" })).toThrow("aspect ratio");
  });

  test("preserves quota machine codes for host refunds and administrator Bark alerts", () => {
    const failure = az.parseTaskResult({}, { status: "failed", error: { code: "insufficient_quota", type: "billing_error", message: "Request failed" } });
    expect(failure.status).toBe("FAILURE");
    expect(failure.reason).toContain("insufficient_quota");
    expect(az.parseTaskResult({}, { status: "processing" }).status).toBe("IN_PROGRESS");
    expect(az.parseTaskResult({}, completed).status).toBe("SUCCESS");
    expect(az.parseTaskResult({}, { status: "completed", result: { data: [] } }).status).toBe("UNKNOWN");
    expect(az.parseTaskResult({}, { status: "new-provider-state" }).status).toBe("UNKNOWN");
    const publicFailure = az.native.imageTask({}, { task_id: "public", status: "FAILURE", fail_reason: "Service temporarily unavailable", data: { error: { remaining_balance: 0 }, poll_url: "/private" } });
    expect(publicFailure.error.message).toBe("Service temporarily unavailable");
    expect(JSON.stringify(publicFailure)).not.toContain("remaining_balance");
    expect(JSON.stringify(publicFailure)).not.toContain("poll_url");
    expect(az.native.imageTask({}, { task_id: "public", status: "FAILURE", fail_reason: "Service temporarily unavailable" }).error.code).toBe("upstream_unavailable");
    expect(az.native.imageTask({}, { status: "FAILURE", fail_reason: "Failed at https://private.example/path?token=secret" }).error.message).not.toContain("private.example");
  });

  test("projects R2 image sources and uses signed gateway URLs in task results", () => {
    expect(az.listArtifacts(view)).toEqual([{ key: "image-1", type: "image", mimeType: "image/png" }]);
    const ctx = { ...driver(), artifactKey: "image-1", upstreamTaskId: "azimg_private", data: completed, clientRequest: { method: "GET" } };
    const descriptor = az.buildContentRequest(ctx);
    expect(descriptor.url).toBe("https://provider.example/v1/images/tasks/azimg_private");
    expect(descriptor.headers.Authorization).toBe("Bearer channel-secret");
    expect(descriptor.contentPath).toEqual(["result", "data", 0, "b64_json"]);
    expect(descriptor.mimeType).toBe("image/png");
    expect(az.buildContentRequest({ ...ctx, data: { ...completed, result: { data: [{ revised_prompt: "empty" }, { b64_json: image }] } } }).contentPath).toEqual(["result", "data", 1, "b64_json"]);
    expect(az.buildContentRequest({ ...ctx, data: { ...completed, result: { data: { b64_json: image } } } }).contentPath).toEqual(["result", "data", "b64_json"]);
    const artifacts = { "image-1": { url: "https://gateway.example/signed/image-1" } };
    const result = az.native.imageTask({ artifacts }, view);
    expect(result.data).toEqual([{ url: artifacts["image-1"].url }]);
    expect(JSON.stringify(result)).not.toContain(image);
    const dataURL = { status: "completed", result: { data: [{ url: `data:image/png;base64,${image}` }] } };
    expect(az.buildContentRequest({ ...ctx, data: dataURL }).contentPath).toEqual(["result", "data", 0, "url"]);
    expect(az.native.imageTask({}, { ...view, data: dataURL }).data).toEqual([{ b64_json: image }]);
    expect(() => az.buildContentRequest({ ...ctx, artifactKey: "image-2" })).toThrow("artifact_not_found");
  });

  test("uses the same plugin's native routes when forwarding through another gateway", () => {
    const ctx = { ...driver(), upstreamModel: alias, upstream: { kind: "new_api" } };
    const submit = az.buildSubmitRequest(ctx);
    expect(submit.url).toBe("https://provider.example/az/v1/images/generations");
    expect(submit.body.async).toBe(true);
    expect(az.buildQueryRequest({ ...ctx, taskId: "task_public" }).url).toBe("https://provider.example/az/v1/images/generations/task_public");
    const url = "https://gateway.example/signed/image-1";
    expect(az.buildContentRequest({ ...ctx, artifactKey: "image-1", clientRequest: { method: "GET" }, data: { data: [{ url }] } })).toEqual({ url, method: "GET", credentialless: true });
  });
});
