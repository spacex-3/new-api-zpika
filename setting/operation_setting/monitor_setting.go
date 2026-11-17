package operation_setting

import (
	"fmt"
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled     bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes     float64 `json:"auto_test_channel_minutes"`
	ChannelTestMode            string  `json:"channel_test_mode"`
	ChannelTestConcurrency     int     `json:"channel_test_concurrency"`
	ChannelErrorAlertThreshold int     `json:"channel_error_alert_threshold"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModeAutoBanOnly     = "auto_ban_only"
	ChannelTestModePassiveRecovery = "passive_recovery"

	ChannelTestConcurrencyOptionKey     = "monitor_setting.channel_test_concurrency"
	ChannelErrorAlertThresholdOptionKey = "monitor_setting.channel_error_alert_threshold"
	DefaultChannelTestConcurrency       = 1
	MaxChannelTestConcurrency           = 32
	DefaultChannelErrorAlertThreshold   = 10
	MaxChannelErrorAlertThreshold       = 100
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:     false,
	AutoTestChannelMinutes:     10,
	ChannelTestMode:            ChannelTestModeScheduledAll,
	ChannelTestConcurrency:     DefaultChannelTestConcurrency,
	ChannelErrorAlertThreshold: DefaultChannelErrorAlertThreshold,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
			monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			monitorSetting.AutoTestChannelEnabled = parsed
		}
	}
	switch monitorSetting.ChannelTestMode {
	case ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
	default:
		monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	monitorSetting.ChannelTestConcurrency = NormalizeChannelTestConcurrency(monitorSetting.ChannelTestConcurrency)
	monitorSetting.ChannelErrorAlertThreshold = NormalizeChannelErrorAlertThreshold(monitorSetting.ChannelErrorAlertThreshold)
	return &monitorSetting
}

func NormalizeChannelTestConcurrency(concurrency int) int {
	if concurrency < 1 {
		return DefaultChannelTestConcurrency
	}
	if concurrency > MaxChannelTestConcurrency {
		return MaxChannelTestConcurrency
	}
	return concurrency
}

func ValidateChannelTestConcurrency(value string) error {
	concurrency, err := strconv.Atoi(value)
	if err != nil || concurrency < 1 || concurrency > MaxChannelTestConcurrency {
		return fmt.Errorf("channel test concurrency must be between 1 and %d", MaxChannelTestConcurrency)
	}
	return nil
}

func NormalizeChannelErrorAlertThreshold(threshold int) int {
	if threshold < 1 {
		return DefaultChannelErrorAlertThreshold
	}
	return min(threshold, MaxChannelErrorAlertThreshold)
}

func ValidateChannelErrorAlertThreshold(value string) error {
	threshold, err := strconv.Atoi(value)
	if err != nil || threshold < 1 || threshold > MaxChannelErrorAlertThreshold {
		return fmt.Errorf("channel error alert threshold must be between 1 and %d", MaxChannelErrorAlertThreshold)
	}
	return nil
}
