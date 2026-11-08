package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCangyuanImageProtocolPreservesSyncAndAsyncClientModes(t *testing.T) {
	source, err := builtinplugins.Source("cangyuan")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "cangyuan"})
	require.NoError(t, err)
	_, hasTokens := plugin.Meta.UsageSchema["tokens"]
	assert.True(t, hasTokens, "Cangyuan must reuse the system Seedance u(\"tokens\") pricing field")
	assert.NotContains(t, plugin.Meta.UsageSchema, "tok")

	requestContext := map[string]any{
		"model":     "gpt-image-2",
		"operation": "generate",
		"body": map[string]any{
			"kind":  "json",
			"value": map[string]any{"model": "gpt-image-2", "prompt": "a cat", "n": 2},
		},
	}
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "decodeRequest"}, requestContext)
	require.NoError(t, err)
	decoded, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "image_generation", decoded["action"])
	requestBody, ok := decoded["requestBody"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, requestBody["async"], "the Cangyuan upstream is always submitted asynchronously")

	syncValue, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "render"}, requestContext, map[string]any{
		"task_id": "local-task", "status": "SUCCESS", "created_at": 1710000000,
		"data": map[string]any{"data": []any{map[string]any{"url": "https://cdn.example/image.png"}}},
	})
	require.NoError(t, err)
	syncResponse, ok := syncValue.(map[string]any)
	require.True(t, ok)
	assert.Len(t, syncResponse["data"], 1)

	requestContext["body"].(map[string]any)["value"].(map[string]any)["async"] = true
	asyncValue, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "render"}, requestContext, map[string]any{
		"task_id": "local-task", "status": "SUBMITTED", "progress": "0%",
	})
	require.NoError(t, err)
	asyncResponse, ok := asyncValue.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "local-task", asyncResponse["id"])
	assert.Equal(t, "queued", asyncResponse["status"])
	assert.NotContains(t, asyncResponse, "data")

	requestContext["body"].(map[string]any)["value"].(map[string]any)["images"] = []any{"data:image/png;base64,AAAA"}
	_, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "decodeRequest"}, requestContext)
	require.ErrorContains(t, err, "images must contain public HTTPS URLs")
}

func TestCangyuanPollingAndBillingFactsMatchDocumentedFields(t *testing.T) {
	source, err := builtinplugins.Source("cangyuan")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "cangyuan"})
	require.NoError(t, err)

	queryValue, err := plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{
		"baseUrl": "https://ai.cangyuansuanli.cn", "apiKey": "secret", "action": "image_generation", "taskId": "vendor/task",
	})
	require.NoError(t, err)
	query, ok := queryValue.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://ai.cangyuansuanli.cn/v1/images/generations/vendor%2Ftask", query["url"])
	_, err = plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{
		"baseUrl": "https://other.example", "apiKey": "secret", "action": "image_generation", "taskId": "vendor-task",
	})
	require.ErrorContains(t, err, "requires an HTTPS cangyuansuanli.cn channel base URL")

	resultValue, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{}, map[string]any{
		"id": "vendor-task", "status": "completed", "progress": 100,
		"data": []any{map[string]any{"url": "https://cdn.example/image.png"}},
	})
	require.NoError(t, err)
	result, ok := resultValue.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "SUCCESS", result["status"])

	factsValue, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
		"requestBody": map[string]any{
			"n": 2, "duration": 6, "quality": "xhigh", "size": "2048x2048", "resolution": "1080p",
			"reference_image_urls": []any{"https://example.com/1.png"},
			"reference_videos":     []any{"https://example.com/1.mp4"},
		},
		"usagePurpose": "facts",
	})
	require.NoError(t, err)
	facts, ok := factsValue.(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 2, facts["n"])
	assert.EqualValues(t, 6, facts["vs"])
	assert.EqualValues(t, 1, facts["vref"])
	assert.EqualValues(t, 2, facts["ref"])
	assert.Equal(t, "1080p", facts["resolution"])
	assert.Equal(t, "xhigh", facts["quality"])
	assert.Equal(t, "2048x2048", facts["size"])

	completionValue, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{}, map[string]any{}, map[string]any{
		"usage": map[string]any{"completion_tokens": 4321},
	})
	require.NoError(t, err)
	completion, ok := completionValue.(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 4321, completion["tokens"], "existing Seedance model formulas use u(\"tokens\")")

	artifactsValue, err := plugin.Engine.Call(t.Context(), "listArtifacts", map[string]any{
		"status": "SUCCESS",
		"data": map[string]any{"data": []any{
			map[string]any{"url": "https://cdn.example/first.png"},
			map[string]any{"url": "https://cdn.example/second.png"},
		}},
	})
	require.NoError(t, err)
	artifacts, ok := artifactsValue.([]any)
	require.True(t, ok)
	require.Len(t, artifacts, 2)
	assert.Equal(t, map[string]any{"key": "image-1", "type": "image"}, artifacts[0])

	contentValue, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{
		"artifactKey":   "image-2",
		"clientRequest": map[string]any{"method": "GET"},
		"data": map[string]any{"data": []any{
			map[string]any{"url": "https://cdn.example/first.png"},
			map[string]any{"url": "https://cdn.example/second.png"},
		}},
	})
	require.NoError(t, err)
	content, ok := contentValue.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://cdn.example/second.png", content["url"])
	assert.Equal(t, true, content["credentialless"])

	imageRatios, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
		"model": "gpt-image-2", "requestBody": map[string]any{"n": 2}, "usagePurpose": "billing_ratios",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"n": int64(2)}, imageRatios)
	generationRatios, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
		"model": "omni-fast", "requestBody": map[string]any{"n": 2}, "usagePurpose": "billing_ratios",
	})
	require.NoError(t, err)
	assert.Empty(t, generationRatios, "per-generation prices must not be multiplied by n")
}
