package service

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const channelErrorAlertWindow = time.Minute

const upstreamQuotaAlertContextKeyPrefix = "upstream_quota_alert_sent_"

const (
	upstreamQuotaErrorContextKey = "upstream_quota_error"
	UpstreamUnavailableErrorCode = types.ErrorCode("upstream_unavailable")
)

var upstreamQuotaErrorCodes = []string{
	"account_balance_insufficient",
	"billing_hard_limit_reached",
	"billing_not_active",
	"credit_balance_too_low",
	"credit_exhausted",
	"credits_exhausted",
	"insufficient_balance",
	"insufficient_credits",
	"insufficient_funds",
	"insufficient_quota",
	"insufficient_user_quota",
	"payment_required",
	"quota_exceeded",
	"quota_exhausted",
}

var upstreamQuotaErrorPhrases = []string{
	"account balance is insufficient",
	"account balance not enough",
	"account has insufficient funds",
	"balance exhausted",
	"balance is too low",
	"balance not enough",
	"billing hard limit",
	"billing is not active",
	"billing not active",
	"credit balance is insufficient",
	"credit balance is too low",
	"credits exhausted",
	"does not have any credits left",
	"exceeded your current quota",
	"hard limit has been reached",
	"insufficient account balance",
	"insufficient balance",
	"insufficient credit",
	"insufficient funds",
	"insufficient user quota",
	"no credits left",
	"not enough balance",
	"not enough credit",
	"not enough quota to pre-consume",
	"out of credit",
	"payment required",
	"please recharge",
	"please top up",
	"quota exhausted",
	"quota has been exhausted",
	"quota is exhausted",
	"recharge your account",
	"预扣费额度失败",
	"余额不足",
	"余额已耗尽",
	"余额已用尽",
	"额度不足",
	"额度已耗尽",
	"额度已用尽",
	"配额不足",
	"请充值",
	"欠费",
	"預扣費額度失敗",
	"餘額不足",
	"餘額已耗盡",
	"餘額已用盡",
	"額度不足",
	"額度已耗盡",
	"額度已用盡",
	"配額不足",
	"請儲值",
}

type channelErrorWindow struct {
	failures []time.Time
	notified bool
}

type channelErrorAlertTracker struct {
	mu       sync.Mutex
	channels map[int]channelErrorWindow
}

var channelErrorAlerts = channelErrorAlertTracker{channels: make(map[int]channelErrorWindow)}

func (t *channelErrorAlertTracker) record(channelID int, now time.Time, threshold int) (int, bool) {
	threshold = operation_setting.NormalizeChannelErrorAlertThreshold(threshold)
	t.mu.Lock()
	defer t.mu.Unlock()
	window := t.channels[channelID]
	cutoff := now.Add(-channelErrorAlertWindow)
	firstActive := 0
	for firstActive < len(window.failures) && !window.failures[firstActive].After(cutoff) {
		firstActive++
	}
	window.failures = window.failures[firstActive:]
	if len(window.failures) < threshold {
		window.notified = false
	}
	window.failures = append(window.failures, now)
	if len(window.failures) > threshold {
		window.failures = window.failures[len(window.failures)-threshold:]
	}
	shouldNotify := len(window.failures) >= threshold && !window.notified
	if shouldNotify {
		window.notified = true
	}
	t.channels[channelID] = window
	return len(window.failures), shouldNotify
}

func (t *channelErrorAlertTracker) reset(channelID int) {
	t.mu.Lock()
	delete(t.channels, channelID)
	t.mu.Unlock()
}

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

// IsUpstreamQuotaError recognizes balance and billing failures returned by a
// selected upstream channel. It must not be used for local billing errors.
func IsUpstreamQuotaError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(string(err.GetErrorCode())))
	if slices.Contains(upstreamQuotaErrorCodes, code) {
		return true
	}
	message := strings.ToLower(err.Error())
	return slices.ContainsFunc(upstreamQuotaErrorPhrases, func(phrase string) bool {
		return strings.Contains(message, phrase)
	})
}

// PublicUpstreamQuotaError returns the stable client-facing error only when
// ProcessChannelError marked this exact error as an upstream quota failure.
func PublicUpstreamQuotaError(c *gin.Context, err *types.NewAPIError) *types.NewAPIError {
	if c == nil || err == nil {
		return err
	}
	marked, exists := c.Get(upstreamQuotaErrorContextKey)
	if !exists || marked != err {
		return err
	}
	return types.NewUpstreamError(
		errors.New(i18n.T(c, i18n.MsgRelayUpstreamUnavailable)),
		UpstreamUnavailableErrorCode,
		http.StatusServiceUnavailable,
		types.ErrOptionWithSkipRetry(),
	)
}

// PublicUpstreamQuotaTaskError applies the same public contract to upstream
// task submission errors. Local billing and validation failures are retained.
func PublicUpstreamQuotaTaskError(c *gin.Context, taskErr *taskdto.TaskError) *taskdto.TaskError {
	if taskErr == nil || taskErr.LocalError {
		return taskErr
	}
	sourceErr := taskErr.Error
	if sourceErr == nil {
		sourceErr = errors.New(taskErr.Message)
	}
	apiErr := types.NewOpenAIError(sourceErr, types.ErrorCode(taskErr.Code), taskErr.StatusCode)
	if !IsUpstreamQuotaError(apiErr) {
		return taskErr
	}
	publicErr := *taskErr
	publicErr.Code = string(UpstreamUnavailableErrorCode)
	publicErr.Message = i18n.T(c, i18n.MsgRelayUpstreamUnavailable)
	publicErr.Data = nil
	publicErr.StatusCode = http.StatusServiceUnavailable
	publicErr.Error = nil
	publicErr.NoRetry = true
	return &publicErr
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	upstreamURLs := common.UpstreamAddresses(c)
	logger.LogError(c, common.LogText("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode(upstreamURLs...))))
	if channelError.ChannelId > 0 {
		upstreamQuotaError := IsUpstreamQuotaError(err)
		if upstreamQuotaError {
			c.Set(upstreamQuotaErrorContextKey, err)
		}
		quotaAlertKey := fmt.Sprintf("%s%d", upstreamQuotaAlertContextKeyPrefix, channelError.ChannelId)
		if upstreamQuotaError && !c.GetBool(quotaAlertKey) {
			c.Set(quotaAlertKey, true)
			errorCode := string(err.GetErrorCode())
			if errorCode == "" {
				errorCode = "unknown"
			}
			params := map[string]any{
				"Name": channelError.ChannelName, "Id": channelError.ChannelId,
				"ErrorCode": errorCode, "StatusCode": err.StatusCode,
				"Error": common.LocalLogPreview(err.MaskSensitiveError(upstreamURLs...)),
			}
			gopool.Go(func() {
				NotifyChannelErrorWatchers(fmt.Sprintf("channel_upstream_quota_%d", channelError.ChannelId), func(lang string) (string, string) {
					return i18n.Translate(lang, i18n.MsgChannelUpstreamQuotaSubject, params), i18n.Translate(lang, i18n.MsgChannelUpstreamQuotaContent, params)
				})
			})
		}
		threshold := operation_setting.GetMonitorSetting().ChannelErrorAlertThreshold
		if count, shouldNotify := channelErrorAlerts.record(channelError.ChannelId, time.Now(), threshold); shouldNotify {
			errorCode := string(err.GetErrorCode())
			if errorCode == "" {
				errorCode = "unknown"
			}
			params := map[string]any{
				"Name": channelError.ChannelName, "Id": channelError.ChannelId, "Count": count,
				"ErrorCode": errorCode, "StatusCode": err.StatusCode,
			}
			gopool.Go(func() {
				NotifyChannelErrorWatchers(fmt.Sprintf("channel_error_%d", channelError.ChannelId), func(lang string) (string, string) {
					return i18n.Translate(lang, i18n.MsgChannelErrorBurstSubject, params), i18n.Translate(lang, i18n.MsgChannelErrorBurstContent, params)
				})
			})
		}
	}
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode(upstreamURLs...)
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(upstreamURLs...), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
