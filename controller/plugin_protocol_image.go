package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// serveTaskPluginImageProtocol answers an OpenAI Images API request that a
// task plugin claimed. The durable submission runs first on a context that
// survives a client disconnect. Background requests return that task at once;
// synchronous requests poll it until terminal and render the final response.
func serveTaskPluginImageProtocol(c *gin.Context, pinned pluginruntime.PinnedEndpoint, deps pluginProtocolBridgeDeps) {
	deps = deps.withDefaults()
	requestValue, exists := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocolRequest, ok := requestValue.(pluginruntime.ProtocolRequestContext)
	if !exists || !ok || protocolRequest.Protocol != pinned.Protocol || pinned.Plugin == nil {
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	imageRequest, err := taskPluginImageBillingRequest(protocolRequest)
	if err != nil {
		respondPluginProtocolError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	clientRequest := c.Request
	var relayInfo *relaycommon.RelayInfo
	var relayInfoErr error
	var outcome *taskSubmissionOutcome
	var taskErr *dto.TaskError
	// DashScope bills the images as soon as it accepts the upstream call. A
	// client that gives up while the submission is in flight must not cancel
	// that call, the task row, its settlement, or its consume log, so the
	// submission runs on its own bounded context exactly like the Responses
	// bridge. Only the in-request wait and the response write observe the
	// client connection.
	func() {
		submissionContext, cancelSubmission := context.WithTimeout(
			context.WithoutCancel(clientRequest.Context()),
			deps.submissionTimeout,
		)
		c.Request = clientRequest.Clone(submissionContext)
		defer func() {
			c.Request = clientRequest
			cancelSubmission()
		}()

		relayInfo, relayInfoErr = relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIImage, imageRequest, nil)
		if relayInfoErr != nil {
			return
		}
		relayInfo.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
		relayInfo.IsStream = false
		relayInfo.OriginModelName = c.GetString("resolved_task_model")
		if action := c.GetString("task_action"); action != "" {
			relayInfo.Action = action
		}
		if taskErr = relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
			return
		}
		if taskErr = relay.ApplyOriginTaskAffinity(c, relayInfo); taskErr != nil {
			return
		}
		outcome, taskErr = deps.submit(c, relayInfo)
	}()
	if clientRequest.Context().Err() != nil {
		// The client is gone. A durable task keeps its settlement and consume
		// log; an asynchronous one is finished by the background poller.
		return
	}
	if relayInfoErr != nil {
		logger.LogError(c, common.LogText("build task protocol relay info failed: %s", relayInfoErr.Error()))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if taskErr != nil {
		respondTaskPluginImageError(c, taskErr)
		return
	}
	if outcome == nil || outcome.Task == nil || outcome.Task.Platform != constant.TaskPlatform(pinned.Plugin.Meta.Key) {
		logger.LogError(c, common.LogText("task protocol submission returned an invalid durable outcome"))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	task := outcome.Task
	asynchronous := taskPluginImageAsyncRequest(protocolRequest)
	if !asynchronous && task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure {
		if taskErr = waitTaskPluginImageTask(c, task, deps); taskErr != nil {
			if c.Request.Context().Err() == nil {
				respondTaskPluginImageError(c, taskErr)
			}
			return
		}
	}
	if !asynchronous && task.Status == model.TaskStatusFailure {
		reason := strings.TrimSpace(task.FailReason)
		if reason == "" {
			reason = "image generation failed"
		}
		// The fail reason comes from the upstream, so mask the channel address in it.
		taskErr = service.TaskErrorWrapper(errors.New(reason), "image_generation_failed", http.StatusBadRequest, common.UpstreamAddresses(c)...)
		taskErr.LocalError = true
		respondTaskPluginImageError(c, taskErr)
		return
	}
	view, err := service.BuildTaskPluginView(task)
	if err != nil {
		logger.LogError(c, common.LogText("build task protocol view failed: %s", err.Error()))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	viewValue, err := taskPluginProtocolJSONValue(view)
	if err != nil {
		logger.LogError(c, common.LogText("encode task protocol view failed: %s", err.Error()))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	payload, err := pinned.Plugin.Engine.CallPathWithAdmissionTimeout(c.Request.Context(), deps.admissionTimeout, "protocols", []string{pinned.Protocol, "render"}, protocolRequest.JSValue(), viewValue)
	if err != nil {
		logger.LogError(c, common.LogText("task protocol image render hook failed: %s", err.Error()))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	response, ok := payload.(map[string]any)
	if !ok {
		logger.LogError(c, common.LogText("task protocol image render hook must return an object"))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if asynchronous {
		c.JSON(http.StatusOK, response)
		return
	}
	data, hasData := response["data"].([]any)
	if !hasData {
		logger.LogError(c, common.LogText("task protocol image render hook must return an object with a data array"))
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if _, present := response["created"]; !present {
		createdAt := task.CreatedAt
		if createdAt == 0 {
			createdAt = task.SubmitTime
		}
		response["created"] = createdAt
	}
	// response_format is host-owned: the plugin renders upstream URLs and the
	// host inlines each image when the client asked for Base64.
	if taskPluginImageResponseFormat(protocolRequest) == "b64_json" {
		for _, entry := range data {
			item, isObject := entry.(map[string]any)
			if !isObject {
				continue
			}
			url, _ := item["url"].(string)
			if existing, _ := item["b64_json"].(string); strings.TrimSpace(url) == "" || existing != "" {
				continue
			}
			_, encoded, downloadErr := deps.downloadImage(url)
			if downloadErr != nil {
				logger.LogWarn(c, common.LogText("task protocol image download failed; returning the upstream URL instead of b64_json: %s", common.MaskSensitiveInfo(downloadErr.Error(), common.UpstreamAddresses(c)...)))
				continue
			}
			item["b64_json"] = encoded
		}
	}
	c.JSON(http.StatusOK, response)
}

func taskPluginImageBillingRequest(request pluginruntime.ProtocolRequestContext) (*kitdto.ImageRequest, error) {
	imageRequest := &kitdto.ImageRequest{Model: request.Model}
	body, ok := request.Body.(map[string]any)
	if !ok {
		return nil, errors.New("image request body is invalid")
	}
	switch body["kind"] {
	case string(pluginruntime.BodyJSON):
		value, ok := body["value"].(map[string]any)
		if !ok {
			return nil, errors.New("image request body is invalid")
		}
		encoded, err := common.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err = common.Unmarshal(encoded, imageRequest); err != nil {
			return nil, err
		}
	case string(pluginruntime.BodyMultipart), string(pluginruntime.BodyForm):
		fields, ok := body["fields"].(map[string][]string)
		if !ok {
			return nil, errors.New("image request fields are invalid")
		}
		first := func(name string) string {
			if values := fields[name]; len(values) > 0 {
				return values[0]
			}
			return ""
		}
		imageRequest.Model = first("model")
		imageRequest.Size = first("size")
		imageRequest.Quality = first("quality")
		if rawN := strings.TrimSpace(first("n")); rawN != "" {
			n, err := strconv.ParseUint(rawN, 10, 64)
			if err != nil || n > kitdto.MaxImageN {
				return nil, fmt.Errorf("n must be an integer between 1 and %d", kitdto.MaxImageN)
			}
			count := uint(n)
			imageRequest.N = &count
		}
		if parameters := strings.TrimSpace(first("parameters")); parameters != "" {
			imageRequest.Extra = map[string]json.RawMessage{"parameters": json.RawMessage(parameters)}
		}
	default:
		return nil, errors.New("image request body is invalid")
	}
	if imageRequest.Model == "" {
		imageRequest.Model = request.Model
	}
	if raw, exists := imageRequest.Extra["parameters"]; exists {
		parameters := &kitdto.ImageBillingParameters{}
		if err := common.Unmarshal(raw, parameters); err != nil {
			return nil, fmt.Errorf("invalid image parameters: %w", err)
		}
		imageRequest.BillingParameters = parameters
	}
	if _, err := imageRequest.ImageCount(false); err != nil {
		return nil, err
	}
	return imageRequest, nil
}

func taskPluginImageAsyncRequest(request pluginruntime.ProtocolRequestContext) bool {
	body, ok := request.Body.(map[string]any)
	if !ok || body["kind"] != string(pluginruntime.BodyJSON) {
		return false
	}
	value, ok := body["value"].(map[string]any)
	if !ok {
		return false
	}
	async, _ := value["async"].(bool)
	return async
}

// waitTaskPluginImageTask polls an asynchronous image task inside the client
// request through the ordinary task polling path, so completion settles
// billing exactly as the background poller would, until the task is terminal
// or the protocol timeout elapses. A client disconnect stops waiting; the
// background poller then finishes the durable task.
func waitTaskPluginImageTask(c *gin.Context, task *model.Task, deps pluginProtocolBridgeDeps) *dto.TaskError {
	ctx, cancel := context.WithTimeout(c.Request.Context(), deps.submissionTimeout)
	defer cancel()
	for {
		timer := time.NewTimer(deps.imagePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && c.Request.Context().Err() == nil {
				return service.TaskErrorWrapperLocal(fmt.Errorf("image generation is still running; task %s settles when it completes", task.TaskID), "task_timeout", http.StatusGatewayTimeout)
			}
			return service.TaskErrorWrapperLocal(ctx.Err(), "request_cancelled", http.StatusRequestTimeout)
		case <-timer.C:
		}
		if err := deps.pollTask(ctx, task); err != nil {
			logger.LogWarn(c, common.LogText("task protocol image poll failed for task %s: %s", task.TaskID, common.MaskSensitiveInfo(err.Error(), common.UpstreamAddresses(c)...)))
			continue
		}
		if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
			return nil
		}
	}
}

// pollTaskPluginImageTask runs one poll round for a single task through the
// same code path as the scheduled task poller, including status CAS and
// completion settlement.
func pollTaskPluginImageTask(ctx context.Context, task *model.Task) error {
	upstreamID := task.GetUpstreamTaskID()
	if upstreamID == "" {
		return errors.New("task has no upstream id")
	}
	return service.UpdateVideoTasks(ctx, task.Platform, map[int][]string{task.ChannelId: {upstreamID}}, map[string]*model.Task{upstreamID: task})
}

func taskPluginImageResponseFormat(request pluginruntime.ProtocolRequestContext) string {
	body, ok := request.Body.(map[string]any)
	if !ok {
		return ""
	}
	switch body["kind"] {
	case string(pluginruntime.BodyJSON):
		value, _ := body["value"].(map[string]any)
		format, _ := value["response_format"].(string)
		return strings.TrimSpace(format)
	case string(pluginruntime.BodyMultipart), string(pluginruntime.BodyForm):
		fields, _ := body["fields"].(map[string][]string)
		if values := fields["response_format"]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

// respondTaskPluginImageError writes an OpenAI error envelope for the Images
// API. DashScope failure bodies arrive as JSON {code, message}; their message
// is surfaced so a client sees the vendor reason instead of raw JSON.
func respondTaskPluginImageError(c *gin.Context, taskErr *dto.TaskError) {
	service.RecordRequestPolicyTermination(c, taskSubmissionAPIError(taskErr))
	status := http.StatusInternalServerError
	if taskErr.StatusCode >= 400 && taskErr.StatusCode <= 599 {
		status = taskErr.StatusCode
	}
	code := taskErr.Code
	message := taskErr.Message
	var upstream struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if common.UnmarshalJsonStr(message, &upstream) == nil && strings.TrimSpace(upstream.Message) != "" {
		message = upstream.Message
		if strings.TrimSpace(upstream.Code) != "" {
			code = upstream.Code
		}
	}
	errorType := "new_api_error"
	if status < http.StatusInternalServerError {
		errorType = "invalid_request_error"
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
		"type":    errorType,
		"code":    code,
	}})
}
