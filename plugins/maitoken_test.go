package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMAITokenVideoEditAndArkContracts(t *testing.T) {
	source, err := builtinplugins.Source("maitoken")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "maitoken"})
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", plugin.Meta.Version)
	assert.ElementsMatch(t, []string{
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
		"doubao-seedance-2-0-mini-260615",
		"doubao-seedance-2-5-260628",
		"minimax_h3-768p",
		"minimax_h3-2k",
	}, plugin.Meta.Models)
	require.NotEmpty(t, plugin.Meta.Routes)
	assert.ElementsMatch(t, []string{
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
		"doubao-seedance-2-0-mini-260615",
		"doubao-seedance-2-5-260628",
	}, plugin.Meta.Routes[0].Models, "MiniMax H3 must not enter the Seedance Ark route")

	editContext := map[string]any{
		"model":         "doubao-seedance-2-5-260628",
		"upstreamModel": "doubao-seedance-2-5",
		"body": map[string]any{
			"kind": "json",
			"value": map[string]any{
				"model":          "doubao-seedance-2-5-260628",
				"mode":           "video_edit",
				"prompt":         "change daylight to night",
				"video_urls":     []any{"https://media.example/source.mp4"},
				"image_urls":     []any{"https://media.example/reference.png"},
				"resolution":     "720p",
				"generate_audio": true,
			},
		},
	}
	decodedValue, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, editContext)
	require.NoError(t, err)
	decoded := decodedValue.(map[string]any)
	requestBody := decoded["requestBody"].(map[string]any)
	payload := requestBody["payload"].(map[string]any)
	assert.Equal(t, "video_edit", requestBody["transport"])
	assert.Equal(t, "video-v1", payload["contract_version"])

	submitValue, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "model": "doubao-seedance-2-5-260628",
		"upstreamModel": "doubao-seedance-2-5", "action": "image_to_video", "requestBody": requestBody,
	})
	require.NoError(t, err)
	submit := submitValue.(map[string]any)
	assert.Equal(t, "https://api.mai-token.com/v1/videos", submit["url"])
	assert.Equal(t, "doubao-seedance-2-5", submit["body"].(map[string]any)["model"])

	parsedValue, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"requestBody": requestBody}, map[string]any{
		"status": 202, "body": map[string]any{"id": "upstream-edit", "status": "queued"},
	})
	require.NoError(t, err)
	parsed := parsedValue.(map[string]any)
	assert.Equal(t, "upstream-edit", parsed["taskId"])
	assert.Equal(t, "video_edit", parsed["state"].(map[string]any)["transport"])

	queryValue, err := plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{
		"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "taskId": "upstream/edit", "state": parsed["state"],
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.mai-token.com/v1/videos/upstream%2Fedit", queryValue.(map[string]any)["url"])

	resultValue, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{}, map[string]any{
		"status": "completed", "progress": 100,
		"result": map[string]any{"url": "https://api.mai-token.com/v1/videos/upstream-edit/content"},
	})
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", resultValue.(map[string]any)["status"])

	usageValue, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{}, map[string]any{}, map[string]any{
		"status":  "completed",
		"billing": map[string]any{"billing_tokens": 543210, "output_seconds": 8, "reference_seconds": 8, "resolution": "720p"},
		"usage":   map[string]any{"completion_tokens": 1},
	})
	require.NoError(t, err)
	usage := usageValue.(map[string]any)
	assert.EqualValues(t, 543210, usage["tokens"], "MAI duration-conversion billing must override raw usage")
	assert.EqualValues(t, 8, usage["output_seconds"])
	assert.EqualValues(t, 8, usage["reference_seconds"])

	contentValue, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{
		"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "artifactKey": "video",
		"clientRequest": map[string]any{"method": "GET"},
		"data":          map[string]any{"video_url": "/v1/videos/upstream-edit/content"},
	})
	require.NoError(t, err)
	content := contentValue.(map[string]any)
	assert.Equal(t, "https://api.mai-token.com/v1/videos/upstream-edit/content", content["url"])
	assert.Equal(t, map[string]any{"Authorization": "Bearer secret"}, content["headers"])

	arkValue, err := plugin.Engine.CallPath(t.Context(), "native", []string{"createArkTask"}, map[string]any{
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "doubao-seedance-2-5-260628", "content": []any{map[string]any{"type": "text", "text": "lake"}},
			"resolution": "720p", "duration": 4,
		}},
	})
	require.NoError(t, err)
	arkRequest := arkValue.(map[string]any)["requestBody"].(map[string]any)
	arkSubmitValue, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "model": "doubao-seedance-2-5-260628",
		"upstreamModel": "doubao-seedance-2-5", "action": "text_to_video", "requestBody": arkRequest,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.mai-token.com/api/v3/contents/generations/tasks", arkSubmitValue.(map[string]any)["url"])
}

func TestMAITokenMiniMaxH3VideoContracts(t *testing.T) {
	source, err := builtinplugins.Source("maitoken")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "maitoken"})
	require.NoError(t, err)

	tests := []struct {
		model      string
		resolution string
		body       map[string]any
	}{
		{"minimax_h3-768p", "768p", map[string]any{"prompt": "lake at sunrise"}},
		{"minimax_h3-2k", "2k", map[string]any{
			"prompt": "lake at sunrise", "contract_version": "video-v1", "seconds": "5",
			"metadata": map[string]any{"resolution": "2K", "aspect_ratio": "9:16"},
		}},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			body := map[string]any{"model": test.model}
			for key, value := range test.body {
				body[key] = value
			}
			decodedValue, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
				"model": test.model, "upstreamModel": test.model, "body": map[string]any{"kind": "json", "value": body},
			})
			require.NoError(t, callErr)
			decoded := decodedValue.(map[string]any)
			requestBody := decoded["requestBody"].(map[string]any)
			assert.Equal(t, "video-v1", requestBody["transport"])
			assert.Equal(t, "text_to_video", decoded["action"])
			payload := requestBody["payload"].(map[string]any)
			assert.Equal(t, "video-v1", payload["contract_version"])
			assert.EqualValues(t, 5, payload["duration"])
			assert.Equal(t, test.resolution, payload["resolution"])
			assert.Equal(t, body["prompt"], payload["prompt"])
			require.Len(t, payload, 5)
			if test.model == "minimax_h3-2k" {
				assert.Equal(t, "9:16", payload["ratio"])
			} else {
				assert.Equal(t, "16:9", payload["ratio"])
			}

			submitValue, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
				"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "model": test.model,
				"upstreamModel": test.model, "action": decoded["action"], "requestBody": requestBody,
			})
			require.NoError(t, callErr)
			submit := submitValue.(map[string]any)
			assert.Equal(t, "https://api.mai-token.com/v1/videos", submit["url"])
			assert.Equal(t, test.model, submit["body"].(map[string]any)["model"])
			require.Len(t, submit["body"].(map[string]any), 6)

			parsedValue, callErr := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"requestBody": requestBody}, map[string]any{
				"status": 202, "body": map[string]any{"id": "upstream-h3", "status": "queued"},
			})
			require.NoError(t, callErr)
			state := parsedValue.(map[string]any)["state"]
			queryValue, callErr := plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{
				"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "taskId": "upstream-h3", "state": state,
			})
			require.NoError(t, callErr)
			assert.Equal(t, "https://api.mai-token.com/v1/videos/upstream-h3", queryValue.(map[string]any)["url"])

			usageValue, callErr := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
				"model": test.model, "upstreamModel": test.model, "requestBody": requestBody,
			})
			require.NoError(t, callErr)
			usage := usageValue.(map[string]any)
			assert.EqualValues(t, 5, usage["seconds"])
			assert.Equal(t, test.resolution, usage["resolution"])

			schema, examples := plugin.Meta.UsageForModel(test.model)
			require.Len(t, schema, 2)
			assert.Contains(t, schema, "seconds")
			assert.Contains(t, schema, "resolution")
			assert.Equal(t, []string{test.resolution}, schema["resolution"].Enum)
			require.Len(t, examples, 1)

			completionValue, callErr := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{
				"model": test.model, "upstreamModel": test.model,
			}, map[string]any{"status": "SUCCESS"}, map[string]any{
				"status": "completed", "billing": map[string]any{"billing_tokens": 999999},
			})
			require.NoError(t, callErr)
			assert.Empty(t, completionValue)
		})
	}

	for _, body := range []map[string]any{
		{"prompt": "lake", "duration": 4},
		{"prompt": "lake", "resolution": "2k"},
		{"prompt": "lake", "ratio": "adaptive"},
		{"prompt": "lake", "video_urls": []any{"https://media.example/reference.mp4"}},
		{"prompt": "lake", "images": []any{"https://media.example/reference.png"}},
		{"prompt": "lake", "generate_audio": true},
		{"prompt": "lake", "mode": "video_edit"},
		{"prompt": "lake", "contract_version": "video-v2"},
	} {
		body["model"] = "minimax_h3-768p"
		_, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
			"model": "minimax_h3-768p", "upstreamModel": "minimax_h3-768p", "body": map[string]any{"kind": "json", "value": body},
		})
		require.Error(t, callErr)
	}
}

func TestMAITokenVideoGenerationModelsAndUsage(t *testing.T) {
	source, err := builtinplugins.Source("maitoken")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "maitoken"})
	require.NoError(t, err)

	tests := []struct {
		name            string
		model           string
		upstreamModel   string
		resolution      string
		duration        int
		referenceVideos []any
		wantTokens      float64
		wantVideoInput  string
	}{
		{"2.0 4k", "doubao-seedance-2-0-260128", "doubao-seedance-2-0-260128", "4k", 4, nil, 777600, "none"},
		{"2.5 reference video", "doubao-seedance-2-5-260628", "doubao-seedance-2-5", "1080p", 5, []any{"https://media.example/reference.mp4"}, 1701000, "video"},
		{"fast", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2.0-fast", "720p", 6, nil, 129600, "none"},
		{"mini auto duration", "doubao-seedance-2-0-mini-260615", "doubao-seedance-2.0-mini", "480p", -1, nil, 144112.5, "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{
				"model": test.model, "prompt": "lake at sunrise", "duration": test.duration,
				"metadata": map[string]any{"resolution": test.resolution, "ratio": "16:9", "reference_videos": test.referenceVideos},
			}
			decodedValue, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
				"model": test.model, "upstreamModel": test.upstreamModel, "body": map[string]any{"kind": "json", "value": body},
			})
			require.NoError(t, callErr)
			decoded := decodedValue.(map[string]any)
			requestBody := decoded["requestBody"].(map[string]any)
			assert.Equal(t, "ark", requestBody["transport"])
			payload := requestBody["payload"].(map[string]any)
			assert.Equal(t, test.resolution, payload["resolution"])
			assert.Equal(t, "16:9", payload["ratio"])
			assert.NotContains(t, payload, "metadata")
			assert.NotContains(t, payload, "prompt")
			content := payload["content"].([]any)
			assert.Equal(t, map[string]any{"type": "text", "text": "lake at sunrise"}, content[0])
			if len(test.referenceVideos) > 0 {
				require.Len(t, content, 2)
				assert.Equal(t, map[string]any{
					"type": "video_url", "role": "reference_video",
					"video_url": map[string]any{"url": "https://media.example/reference.mp4"},
				}, content[1])
			} else {
				assert.Len(t, content, 1)
			}

			submitValue, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
				"baseUrl": "https://api.mai-token.com", "apiKey": "secret", "model": test.model,
				"upstreamModel": test.upstreamModel, "action": decoded["action"], "requestBody": requestBody,
			})
			require.NoError(t, callErr)
			submit := submitValue.(map[string]any)
			assert.Equal(t, "https://api.mai-token.com/api/v3/contents/generations/tasks", submit["url"])
			assert.Equal(t, test.upstreamModel, submit["body"].(map[string]any)["model"])

			usageValue, callErr := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
				"model": test.model, "upstreamModel": test.upstreamModel, "requestBody": requestBody,
			})
			require.NoError(t, callErr)
			usage := usageValue.(map[string]any)
			assert.InDelta(t, test.wantTokens, usage["tokens"], 0.001)
			assert.Equal(t, test.wantVideoInput, usage["video_input"])
		})
	}

	_, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
		"model": "doubao-seedance-2-0-fast-260128",
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "doubao-seedance-2-0-fast-260128", "prompt": "lake", "duration": 16, "resolution": "1080p",
		}},
	})
	require.Error(t, err)
}

func TestMAITokenConvertsCanvasSeedanceReferences(t *testing.T) {
	source, err := builtinplugins.Source("maitoken")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "maitoken"})
	require.NoError(t, err)

	decode := func(content []any) map[string]any {
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
			"model": "doubao-seedance-2-5-260628",
			"body": map[string]any{"kind": "json", "value": map[string]any{
				"model": "doubao-seedance-2-5-260628", "prompt": "follow the references", "duration": 5, "seconds": "5",
				"metadata": map[string]any{"resolution": "720p", "ratio": "16:9", "generate_audio": true, "content": content},
			}},
		})
		require.NoError(t, callErr)
		return value.(map[string]any)
	}

	ordinary := decode([]any{
		map[string]any{"type": "image_url", "role": "reference_image", "image_url": map[string]any{"url": "https://media.example/image.png"}},
		map[string]any{"type": "video_url", "role": "reference_video", "video_url": map[string]any{"url": "https://media.example/video.mp4"}},
		map[string]any{"type": "audio_url", "role": "reference_audio", "audio_url": map[string]any{"url": "https://media.example/audio.mp3"}},
	})
	assert.Equal(t, "image_to_video", ordinary["action"])
	requestBody := ordinary["requestBody"].(map[string]any)
	assert.Equal(t, "ark", requestBody["transport"])
	payload := requestBody["payload"].(map[string]any)
	assert.Equal(t, "16:9", payload["ratio"])
	assert.Equal(t, []any{
		map[string]any{"type": "text", "text": "follow the references"},
		map[string]any{"type": "image_url", "role": "reference_image", "image_url": map[string]any{"url": "https://media.example/image.png"}},
		map[string]any{"type": "video_url", "role": "reference_video", "video_url": map[string]any{"url": "https://media.example/video.mp4"}},
		map[string]any{"type": "audio_url", "role": "reference_audio", "audio_url": map[string]any{"url": "https://media.example/audio.mp3"}},
	}, payload["content"])
	assert.NotContains(t, payload, "metadata")
	assert.NotContains(t, payload, "seconds")
	assert.NotContains(t, payload, "reference_videos")

	frames := decode([]any{
		map[string]any{"type": "image_url", "role": "first_frame", "image_url": map[string]any{"url": "https://media.example/first.png"}},
		map[string]any{"type": "image_url", "role": "last_frame", "image_url": map[string]any{"url": "https://media.example/last.png"}},
	})
	framePayload := frames["requestBody"].(map[string]any)["payload"].(map[string]any)
	assert.Equal(t, "adaptive", framePayload["ratio"])
	assert.Equal(t, []any{
		map[string]any{"type": "text", "text": "follow the references"},
		map[string]any{"type": "image_url", "role": "first_frame", "image_url": map[string]any{"url": "https://media.example/first.png"}},
		map[string]any{"type": "image_url", "role": "last_frame", "image_url": map[string]any{"url": "https://media.example/last.png"}},
	}, framePayload["content"])
	assert.NotContains(t, framePayload, "first_image_url")
	assert.NotContains(t, framePayload, "last_image_url")
}
