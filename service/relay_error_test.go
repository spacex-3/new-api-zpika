package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestShouldRetryRelayErrorHonorsChannelPinOnChannelError(t *testing.T) {
	err := types.NewError(errors.New("channel failed"), types.ErrorCodeChannelNoAvailableKey)
	for _, test := range []struct {
		name      string
		pin       *dto.ChannelPin
		wantRetry bool
	}{
		{name: "unrestricted channel error", wantRetry: true},
		{
			name: "single attempt pin suppresses channel error retry",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt,
			},
			wantRetry: false,
		},
		{
			name: "origin task pin permits retry on the same channel",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel,
			},
			wantRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin != nil {
				GetChannelConstraints(c).AddPin(*test.pin)
			}
			assert.Equal(t, test.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}

func TestIsUpstreamQuotaError(t *testing.T) {
	for _, code := range []string{
		"insufficient_user_quota",
		"insufficient_quota",
		"quota_exceeded",
		"billing_not_active",
		"billing_hard_limit_reached",
		"insufficient_balance",
		"insufficient_funds",
		"insufficient_credits",
		"credit_balance_too_low",
		"payment_required",
	} {
		t.Run("code_"+code, func(t *testing.T) {
			err := types.WithOpenAIError(types.OpenAIError{Message: "upstream rejected the request", Code: code}, http.StatusPaymentRequired)
			assert.True(t, IsUpstreamQuotaError(err))
		})
	}

	for _, message := range []string{
		"Insufficient user quota, please recharge",
		"Insufficient account balance",
		"insufficient balance",
		"Upstream image account has insufficient balance",
		"Not enough quota to pre-consume for this request",
		"Your credit balance is too low to access the API",
		"You exceeded your current quota, please check your plan and billing details",
		"This team does not have any credits left",
		"预扣费额度失败, 用户剩余额度: 0, 需要预扣费额度: 1",
		"账号余额不足，请充值",
		"帳號餘額不足，請儲值",
	} {
		t.Run("message_"+message, func(t *testing.T) {
			err := types.NewOpenAIError(errors.New(message), types.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired)
			assert.True(t, IsUpstreamQuotaError(err))
		})
	}

	for _, message := range []string{
		"subscription_sharing_unsupported_capability: User subscription sharing is not supported",
		"rate limit exceeded, retry after 30 seconds",
		"quota reset information is temporarily unavailable",
		"billing address is invalid",
		"credit card was declined",
		"用户余额充足",
	} {
		t.Run("not_quota_"+message, func(t *testing.T) {
			err := types.NewOpenAIError(errors.New(message), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
			assert.False(t, IsUpstreamQuotaError(err))
		})
	}
	assert.False(t, IsUpstreamQuotaError(nil))
}

func TestPublicUpstreamQuotaErrorPreservesInternalError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		language string
		message  string
	}{
		{name: "english", language: "en", message: "Upstream service is temporarily unavailable. Please try again later."},
		{name: "simplified chinese", language: "zh-CN", message: "上游服务暂时不可用，请稍后重试"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Request.Header.Set("Accept-Language", tc.language)
			original := types.WithOpenAIError(types.OpenAIError{
				Message: "预扣费额度失败, 用户剩余额度: 0",
				Type:    "insufficient_quota",
				Code:    "insufficient_quota",
			}, http.StatusForbidden)
			c.Set(upstreamQuotaErrorContextKey, original)

			publicErr := PublicUpstreamQuotaError(c, original)
			require.NotSame(t, original, publicErr)
			assert.Equal(t, http.StatusServiceUnavailable, publicErr.StatusCode)
			assert.Equal(t, UpstreamUnavailableErrorCode, publicErr.GetErrorCode())
			assert.Equal(t, types.ErrorTypeUpstreamError, publicErr.GetErrorType())
			assert.Equal(t, tc.message, publicErr.Error())
			assert.Equal(t, "upstream_error", publicErr.ToOpenAIError().Type)
			assert.Equal(t, UpstreamUnavailableErrorCode, publicErr.ToOpenAIError().Code)
			assert.Equal(t, "upstream_error", publicErr.ToClaudeError().Type)

			assert.Equal(t, http.StatusForbidden, original.StatusCode)
			assert.Contains(t, original.Error(), "预扣费额度失败")
		})
	}

	t.Run("unmarked matching error stays unchanged", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		err := types.NewOpenAIError(errors.New("insufficient balance"), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
		assert.Same(t, err, PublicUpstreamQuotaError(c, err))
	})

	t.Run("earlier marked retry does not mask a different final error", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		quotaErr := types.NewOpenAIError(errors.New("insufficient balance"), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
		finalErr := types.NewOpenAIError(errors.New("content policy rejected"), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
		c.Set(upstreamQuotaErrorContextKey, quotaErr)
		assert.Same(t, finalErr, PublicUpstreamQuotaError(c, finalErr))
	})
}

func TestPublicUpstreamQuotaTaskErrorOnlyRewritesUpstreamBillingFailures(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Request.Header.Set("Accept-Language", "en")

	upstream := &dto.TaskError{
		Code:       "bad_response_status_code",
		Message:    "预扣费额度失败, 用户剩余额度: 0",
		StatusCode: http.StatusForbidden,
	}
	publicErr := PublicUpstreamQuotaTaskError(c, upstream)
	require.NotSame(t, upstream, publicErr)
	assert.Equal(t, http.StatusServiceUnavailable, publicErr.StatusCode)
	assert.Equal(t, string(UpstreamUnavailableErrorCode), publicErr.Code)
	assert.Equal(t, "Upstream service is temporarily unavailable. Please try again later.", publicErr.Message)
	assert.True(t, publicErr.NoRetry)
	assert.Equal(t, http.StatusForbidden, upstream.StatusCode)
	assert.Contains(t, upstream.Message, "预扣费额度失败")

	local := &dto.TaskError{Code: "insufficient_user_quota", Message: "用户额度不足", StatusCode: http.StatusForbidden, LocalError: true}
	assert.Same(t, local, PublicUpstreamQuotaTaskError(c, local))
	nonQuota := &dto.TaskError{Code: "content_policy", Message: "content policy rejected", StatusCode: http.StatusForbidden}
	assert.Same(t, nonQuota, PublicUpstreamQuotaTaskError(c, nonQuota))
}

func TestChannelErrorAlertTrackerUsesRollingFailureCount(t *testing.T) {
	tracker := channelErrorAlertTracker{channels: make(map[int]channelErrorWindow)}
	startedAt := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	const threshold = 5
	for count := range threshold - 1 {
		gotCount, notify := tracker.record(7, startedAt.Add(time.Duration(count)*time.Second), threshold)
		assert.Equal(t, count+1, gotCount)
		assert.False(t, notify)
	}
	gotCount, notify := tracker.record(7, startedAt.Add(4*time.Second), threshold)
	assert.Equal(t, threshold, gotCount)
	assert.True(t, notify)
	_, notify = tracker.record(7, startedAt.Add(5*time.Second), threshold)
	assert.False(t, notify, "the same active window must only notify once")
	_, notify = tracker.record(8, startedAt.Add(5*time.Second), threshold)
	assert.False(t, notify, "channels must have independent counters")

	for count := range threshold - 1 {
		gotCount, notify = tracker.record(7, startedAt.Add(time.Duration(65+count)*time.Second), threshold)
		assert.False(t, notify)
	}
	assert.Equal(t, threshold-1, gotCount)
	_, notify = tracker.record(7, startedAt.Add(69*time.Second), threshold)
	assert.True(t, notify, "the alert must re-arm after old failures leave the rolling window")
}

func TestProcessChannelErrorSendsIndependentQuotaAndThresholdAlerts(t *testing.T) {
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousAutoDisable, previousErrorLog := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	previousNotifyLimit := constant.NotifyLimitCount
	previousClient, previousWorker := httpClient, system_setting.WorkerUrl
	fetch := system_setting.GetFetchSetting()
	previousFetch := *fetch
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = previousAutoDisable, previousErrorLog
		constant.NotifyLimitCount = previousNotifyLimit
		httpClient, system_setting.WorkerUrl = previousClient, previousWorker
		*fetch = previousFetch
	})
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, database.AutoMigrate(&model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = false, false
	constant.NotifyLimitCount = 10

	notifications := make(chan WebhookPayload, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload WebhookPayload
		if common.DecodeJson(r.Body, &payload) == nil {
			notifications <- payload
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	httpClient, system_setting.WorkerUrl = server.Client(), ""
	fetch.EnableSSRFProtection = false
	settings, err := common.Marshal(kitdto.UserSetting{NotifyType: kitdto.NotifyTypeWebhook, WebhookUrl: server.URL})
	require.NoError(t, err)
	root := &model.User{Username: "burst-notification-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Setting: string(settings)}
	require.NoError(t, database.Create(root).Error)

	const channelID, retryChannelID = 77, 78
	channelErrorAlerts.reset(channelID)
	channelErrorAlerts.reset(retryChannelID)
	burstNotifyKey := fmt.Sprintf("%d:channel_error_%d", root.Id, channelID)
	quotaNotifyKey := fmt.Sprintf("%d:channel_upstream_quota_%d", root.Id, channelID)
	retryQuotaNotifyKey := fmt.Sprintf("%d:channel_upstream_quota_%d", root.Id, retryChannelID)
	notifyLimitStore.Delete(burstNotifyKey)
	notifyLimitStore.Delete(quotaNotifyKey)
	notifyLimitStore.Delete(retryQuotaNotifyKey)
	t.Cleanup(func() {
		channelErrorAlerts.reset(channelID)
		channelErrorAlerts.reset(retryChannelID)
		notifyLimitStore.Delete(burstNotifyKey)
		notifyLimitStore.Delete(quotaNotifyKey)
		notifyLimitStore.Delete(retryQuotaNotifyKey)
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://private.example.com")
	quotaErr := types.NewOpenAIError(errors.New("预扣费额度失败, 用户剩余额度: 0, 需要预扣费额度: 1; upstream https://private.example.com/path?token=quota-token api_key:quota-secret"), types.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired)
	apiErr := types.WithOpenAIError(types.OpenAIError{Message: "invalid credential", Type: "invalid_request_error", Code: "invalid_api_key"}, http.StatusUnauthorized)
	channelError := types.ChannelError{ChannelId: channelID, ChannelName: "edge-singapore"}
	monitorSetting := operation_setting.GetMonitorSetting()
	previousThreshold := monitorSetting.ChannelErrorAlertThreshold
	monitorSetting.ChannelErrorAlertThreshold = 5
	t.Cleanup(func() { monitorSetting.ChannelErrorAlertThreshold = previousThreshold })
	ProcessChannelError(c, channelError, quotaErr, nil)
	ProcessChannelError(c, channelError, quotaErr, nil)
	ProcessChannelError(c, types.ChannelError{ChannelId: retryChannelID, ChannelName: "edge-tokyo"}, quotaErr, nil)
	for range 2 {
		ProcessChannelError(c, channelError, apiErr, nil)
	}
	successContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	successContext.Set("channel_id", channelID)
	MarkRequestPolicySuccess(successContext, nil)
	ProcessChannelError(c, channelError, apiErr, nil)

	received := make(map[string]WebhookPayload)
	deadline := time.After(5 * time.Second)
	for len(received) < 3 {
		select {
		case notification := <-notifications:
			received[notification.Type] = notification
		case <-deadline:
			t.Fatalf("channel error notifications were not delivered: got %v", received)
		}
	}

	quotaNotification, ok := received[fmt.Sprintf("channel_upstream_quota_%d", channelID)]
	require.True(t, ok)
	assert.Equal(t, "Upstream balance alert", quotaNotification.Title)
	assert.Contains(t, quotaNotification.Content, `Channel "edge-singapore" (#77)`)
	assert.Contains(t, quotaNotification.Content, "HTTP status: 402")
	assert.Contains(t, quotaNotification.Content, "预扣费额度失败")
	assert.Contains(t, quotaNotification.Content, "https://***.com/***?token=***")
	assert.NotContains(t, quotaNotification.Content, "quota-token")
	assert.NotContains(t, quotaNotification.Content, "quota-secret")
	retryQuotaNotification, ok := received[fmt.Sprintf("channel_upstream_quota_%d", retryChannelID)]
	require.True(t, ok)
	assert.Contains(t, retryQuotaNotification.Content, `Channel "edge-tokyo" (#78)`)

	burstNotification, ok := received[fmt.Sprintf("channel_error_%d", channelID)]
	require.True(t, ok)
	assert.Equal(t, "Frequent channel errors", burstNotification.Title)
	assert.Contains(t, burstNotification.Content, `Channel "edge-singapore" (#77)`)
	assert.Contains(t, burstNotification.Content, "failed 5 times within the last minute")
	assert.Contains(t, burstNotification.Content, "Latest error code: invalid_api_key")
	assert.Contains(t, burstNotification.Content, "HTTP status: 401")

	select {
	case notification := <-notifications:
		t.Fatalf("same request and channel sent a duplicate notification: %#v", notification)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestProcessChannelErrorMasksDisableReasonAndNotification(t *testing.T) {
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousAutoDisable, previousErrorLog := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	previousNotifyLimit := constant.NotifyLimitCount
	previousClient, previousWorker := httpClient, system_setting.WorkerUrl
	fetch := system_setting.GetFetchSetting()
	previousFetch := *fetch
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = previousAutoDisable, previousErrorLog
		constant.NotifyLimitCount = previousNotifyLimit
		httpClient, system_setting.WorkerUrl = previousClient, previousWorker
		*fetch = previousFetch
	})
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = true, false
	constant.NotifyLimitCount = 10
	channel := &model.Channel{Name: "relay-review", Key: "fixture-key", Type: 1, Status: common.ChannelStatusEnabled, Group: "default", Models: "test-model"}
	require.NoError(t, channel.Insert())
	notifications := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		notifications <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	httpClient, system_setting.WorkerUrl = server.Client(), ""
	fetch.EnableSSRFProtection = false
	settings, err := common.Marshal(kitdto.UserSetting{NotifyType: kitdto.NotifyTypeWebhook, WebhookUrl: server.URL})
	require.NoError(t, err)
	root := &model.User{Username: "notification-test-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Setting: string(settings)}
	require.NoError(t, database.Create(root).Error)
	notifyKey := fmt.Sprintf("%d:%s:%s", root.Id, formatNotifyType(channel.Id, common.ChannelStatusAutoDisabled), time.Now().Format("2006010215"))
	notifyLimitStore.Delete(notifyKey)
	t.Cleanup(func() { notifyLimitStore.Delete(notifyKey) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://private.example.com")
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream https://private.example.com/path?token=review-token api_key:review-secret"), types.ErrorCodeChannelNoAvailableKey, http.StatusUnauthorized)
	ProcessChannelError(c, types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, AutoBan: true}, apiErr, nil)
	var notification WebhookPayload
	select {
	case payload := <-notifications:
		require.NoError(t, common.Unmarshal(payload, &notification))
	case <-time.After(5 * time.Second):
		t.Fatal("automatic channel-disable notification was not delivered")
	}
	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
	wantReason := "status_code=401, upstream https://***.com/***?token=*** api_key:***"
	assert.Equal(t, wantReason, loaded.GetOtherInfo()["status_reason"])
	assert.Contains(t, notification.Content, wantReason)
	assert.NotContains(t, notification.Content, "review-token")
	assert.NotContains(t, notification.Content, "review-secret")
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

// Only addresses the admin configured are masked: a channel on the base URL
// new-api presets reaches a public official endpoint, and so do adaptors that
// build official endpoints themselves.
func TestUpstreamAddressesMaskOnlyConfiguredAddresses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channelType int
		baseURL     string
		origin      string
		proxyEnv    string
		message     string
		want        string
	}{
		{
			name:        "preset base URL stays readable",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			origin:      "https://api.anthropic.com",
			message:     "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
			want:        "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
		},
		{
			name:        "official endpoint built by the adaptor stays readable",
			channelType: constant.ChannelTypeVertexAi,
			origin:      "https://us-central1-aiplatform.googleapis.com",
			message:     "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
			want:        "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
		},
		{
			name:        "configured base URL is masked",
			channelType: constant.ChannelTypeOpenAI,
			baseURL:     "https://relay.private.example",
			origin:      "https://relay.private.example",
			message:     "request to https://relay.private.example/v1/chat/completions failed",
			want:        "request to https://***.example/***/***/*** failed",
		},
		{
			name:        "absolute Advanced Custom route is masked",
			channelType: constant.ChannelTypeAdvancedCustom,
			origin:      "https://route.private.example",
			message:     "dial tcp: lookup route.private.example: no such host",
			want:        "dial tcp: lookup ***.***.example: no such host",
		},
		{
			name:        "environment proxy is masked on a preset channel",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			proxyEnv:    "http://egress.private.invalid:8080",
			message:     "proxyconnect tcp: dial tcp: lookup egress.private.invalid: no such host",
			want:        "proxyconnect tcp: dial tcp: lookup ***.***.invalid: no such host",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", tc.proxyEnv)
			t.Setenv("HTTP_PROXY", "")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, tc.baseURL)
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, tc.origin)
			apiErr := types.NewErrorWithStatusCode(errors.New(tc.message), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

			assert.Equal(t, tc.want, apiErr.ToOpenAIError(common.UpstreamAddresses(c)...).Message)
		})
	}
}

func TestDecideRelayRetryReasons(t *testing.T) {
	upstream := func(status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, status)
	}
	for _, tc := range []struct {
		name    string
		err     *types.NewAPIError
		retries int
		setup   func(*gin.Context)
		want    PolicyDecision
	}{
		{name: "retry status matched", err: upstream(http.StatusTooManyRequests), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "status outside retry rules", err: upstream(http.StatusBadRequest), retries: 1, want: PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}},
		{name: "attempt budget exhausted", err: upstream(http.StatusTooManyRequests), retries: 0, want: PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}},
		{name: "always skipped status", err: upstream(http.StatusGatewayTimeout), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "success status never retries", err: upstream(http.StatusOK), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "skip retry error", err: types.NewErrorWithStatusCode(errors.New("local"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry()), retries: 1, want: PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}},
		{name: "channel error retries without budget", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 0, want: PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}},
		{name: "single attempt pin", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
		}, want: PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}},
		{name: "strict session", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			c.Set(ginKeyChannelAffinitySkipRetry, true)
			RequestPolicy(c).SessionModeSource = "global"
		}, want: PolicyDecision{Action: "stop", Reason: "strict_session", Source: "global"}},
		{name: "nil error", retries: 1, want: PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.setup != nil {
				tc.setup(c)
			}
			decision := DecideRelayRetry(c, tc.err, tc.retries)
			assert.Equal(t, tc.want, decision)
			assert.Equal(t, tc.want.Action == "retry", ShouldRetryRelayError(c, tc.err, tc.retries))
		})
	}
}

func TestRequestPolicyEventsReachLogAdminInfo(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("auto_ban", true)
	c.Set("channel_id", 7)
	state := RequestPolicy(c)
	state.BeginAttempt(&model.Channel{Id: 7}, "default")
	apiErr := types.NewOpenAIError(errors.New("invalid credential"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))

	failed := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, failed)
	events, ok := failed.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a failed relay exposes its decision events to administrators")
	require.Len(t, events, 3)
	assert.Equal(t, PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}, events[0].Decision)
	assert.Equal(t, "default", events[0].Group)
	assert.Equal(t, PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: "upstream"}, events[1].Decision)
	assert.Equal(t, http.StatusUnauthorized, events[1].Status)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}, events[2].Decision)
	assert.Equal(t, "channel_disable_requested", events[2].Health, "the health entry follows the automatic disable rules")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))
	assert.Equal(t, "key_disable_requested", state.Events()[4].Health)

	state.BeginAttempt(&model.Channel{Id: 8}, "default")
	c.Set("channel_id", 8)
	MarkRequestPolicySuccess(c, nil)
	MarkRequestPolicySuccess(c, nil)
	succeeded := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, succeeded)
	events, ok = succeeded.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a successful relay exposes its decision events to administrators")
	require.Len(t, events, 7, "the outcome is recorded once")
	assert.Equal(t, PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}, events[6].Decision)
	assert.Equal(t, 8, events[6].ChannelID)
	assert.Equal(t, 2, events[6].Attempt)
	assert.True(t, state.Successful)

	untouched, _ := gin.CreateTestContext(httptest.NewRecorder())
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(untouched, nil, other)
	assert.NotContains(t, other.Snapshot()["admin_info"], "request_policy", "requests without decisions do not carry an empty record")
}
