package system_setting

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
)

const (
	TaskArtifactStoreModeUpstream = "upstream"
	TaskArtifactStoreModeS3       = "s3"
	TaskArtifactStoreModeWorkerR2 = "worker_r2"

	DefaultTaskArtifactStorePresignTTLSeconds = 900
	MaxTaskArtifactStorePresignTTLSeconds     = 7 * 24 * 60 * 60
	DefaultTaskArtifactStoreRetentionSeconds  = 24 * 60 * 60
	MaxTaskArtifactStoreRetentionSeconds      = 7 * 24 * 60 * 60
	DefaultTaskArtifactWorkerTimeoutSeconds   = 15 * 60
	MaxTaskArtifactWorkerTimeoutSeconds       = 30 * 60
	DefaultTaskArtifactWorkerConcurrency      = 2
	MaxTaskArtifactWorkerConcurrency          = 32
)

const (
	TaskArtifactStoreModeEnv         = "TASK_ARTIFACT_STORE_MODE"
	TaskArtifactStoreS3EndpointEnv   = "TASK_ARTIFACT_STORE_S3_ENDPOINT"
	TaskArtifactStoreS3BucketEnv     = "TASK_ARTIFACT_STORE_S3_BUCKET"
	TaskArtifactStoreS3RegionEnv     = "TASK_ARTIFACT_STORE_S3_REGION"
	TaskArtifactStoreS3AccessKeyEnv  = "TASK_ARTIFACT_STORE_S3_ACCESS_KEY"
	TaskArtifactStoreS3SecretKeyEnv  = "TASK_ARTIFACT_STORE_S3_SECRET_KEY"
	TaskArtifactStoreS3PrefixEnv     = "TASK_ARTIFACT_STORE_S3_PREFIX"
	TaskArtifactStoreS3PresignTTLEnv = "TASK_ARTIFACT_STORE_S3_PRESIGN_TTL"
	TaskArtifactWorkerURLEnv         = "TASK_ARTIFACT_WORKER_URL"
	TaskArtifactWorkerHMACKeyIDEnv   = "TASK_ARTIFACT_WORKER_HMAC_KEY_ID"
	TaskArtifactWorkerHMACSecretEnv  = "TASK_ARTIFACT_WORKER_HMAC_SECRET"
	TaskArtifactMediaBaseURLEnv      = "TASK_ARTIFACT_MEDIA_BASE_URL"
	TaskArtifactMediaHMACSecretEnv   = "TASK_ARTIFACT_MEDIA_HMAC_SECRET"
	TaskArtifactStoreRetentionEnv    = "TASK_ARTIFACT_STORE_TTL_SECONDS"
	TaskArtifactWorkerTimeoutEnv     = "TASK_ARTIFACT_WORKER_TIMEOUT_SECONDS"
	TaskArtifactWorkerConcurrencyEnv = "TASK_ARTIFACT_WORKER_CONCURRENCY"
)

var (
	taskArtifactStoreBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	taskArtifactStoreRegionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// TaskArtifactStoreConfig holds startup-only artifact persistence settings.
// Legacy S3 fields remain reserved; worker_r2 is the implemented backend.
type TaskArtifactStoreConfig struct {
	Mode                 string
	S3Endpoint           string
	S3Bucket             string
	S3Region             string
	S3AccessKey          string
	S3SecretKey          string
	S3Prefix             string
	S3PresignTTLSeconds  int
	WorkerURL            string
	WorkerHMACKeyID      string
	WorkerHMACSecret     string
	MediaBaseURL         string
	MediaHMACSecret      string
	RetentionSeconds     int
	WorkerTimeoutSeconds int
	WorkerConcurrency    int
}

// LoadTaskArtifactStoreConfig reads and validates startup-only configuration.
// S3 mode is deliberately disabled until a storage implementation is shipped.
func LoadTaskArtifactStoreConfig() TaskArtifactStoreConfig {
	config := TaskArtifactStoreConfig{
		Mode:                 common.GetEnvOrDefaultString(TaskArtifactStoreModeEnv, TaskArtifactStoreModeUpstream),
		S3Endpoint:           common.GetEnvOrDefaultString(TaskArtifactStoreS3EndpointEnv, ""),
		S3Bucket:             common.GetEnvOrDefaultString(TaskArtifactStoreS3BucketEnv, ""),
		S3Region:             common.GetEnvOrDefaultString(TaskArtifactStoreS3RegionEnv, ""),
		S3AccessKey:          common.GetEnvOrDefaultString(TaskArtifactStoreS3AccessKeyEnv, ""),
		S3SecretKey:          common.GetEnvOrDefaultString(TaskArtifactStoreS3SecretKeyEnv, ""),
		S3Prefix:             common.GetEnvOrDefaultString(TaskArtifactStoreS3PrefixEnv, ""),
		S3PresignTTLSeconds:  common.GetEnvOrDefault(TaskArtifactStoreS3PresignTTLEnv, DefaultTaskArtifactStorePresignTTLSeconds),
		WorkerURL:            common.GetEnvOrDefaultString(TaskArtifactWorkerURLEnv, ""),
		WorkerHMACKeyID:      common.GetEnvOrDefaultString(TaskArtifactWorkerHMACKeyIDEnv, ""),
		WorkerHMACSecret:     common.GetEnvOrDefaultString(TaskArtifactWorkerHMACSecretEnv, ""),
		MediaBaseURL:         common.GetEnvOrDefaultString(TaskArtifactMediaBaseURLEnv, ""),
		MediaHMACSecret:      common.GetEnvOrDefaultString(TaskArtifactMediaHMACSecretEnv, ""),
		RetentionSeconds:     common.GetEnvOrDefault(TaskArtifactStoreRetentionEnv, DefaultTaskArtifactStoreRetentionSeconds),
		WorkerTimeoutSeconds: common.GetEnvOrDefault(TaskArtifactWorkerTimeoutEnv, DefaultTaskArtifactWorkerTimeoutSeconds),
		WorkerConcurrency:    common.GetEnvOrDefault(TaskArtifactWorkerConcurrencyEnv, DefaultTaskArtifactWorkerConcurrency),
	}
	if err := ValidateTaskArtifactStoreConfig(config); err != nil {
		common.SysError(common.LogText("invalid task artifact store configuration: %s; using upstream mode", err.Error()))
		config.Mode = TaskArtifactStoreModeUpstream
		return config
	}
	if config.Mode == TaskArtifactStoreModeS3 {
		common.SysError(common.LogText("task artifact S3 storage is not implemented; using upstream mode"))
		config.Mode = TaskArtifactStoreModeUpstream
	}
	return config
}

// ValidateTaskArtifactStoreConfig performs syntax checks only. It never
// resolves hosts, contacts an endpoint, or verifies credentials.
func ValidateTaskArtifactStoreConfig(config TaskArtifactStoreConfig) error {
	if config.Mode != TaskArtifactStoreModeUpstream && config.Mode != TaskArtifactStoreModeS3 && config.Mode != TaskArtifactStoreModeWorkerR2 {
		return fmt.Errorf("unsupported mode %q", config.Mode)
	}
	if config.S3PresignTTLSeconds <= 0 || config.S3PresignTTLSeconds > MaxTaskArtifactStorePresignTTLSeconds {
		return fmt.Errorf("S3 presign TTL must be between 1 and %d seconds", MaxTaskArtifactStorePresignTTLSeconds)
	}
	if config.RetentionSeconds <= 0 || config.RetentionSeconds > MaxTaskArtifactStoreRetentionSeconds {
		return fmt.Errorf("artifact retention must be between 1 and %d seconds", MaxTaskArtifactStoreRetentionSeconds)
	}
	if config.WorkerTimeoutSeconds <= 0 || config.WorkerTimeoutSeconds > MaxTaskArtifactWorkerTimeoutSeconds {
		return fmt.Errorf("artifact worker timeout must be between 1 and %d seconds", MaxTaskArtifactWorkerTimeoutSeconds)
	}
	if config.WorkerConcurrency <= 0 || config.WorkerConcurrency > MaxTaskArtifactWorkerConcurrency {
		return fmt.Errorf("artifact worker concurrency must be between 1 and %d", MaxTaskArtifactWorkerConcurrency)
	}

	requireS3Fields := config.Mode == TaskArtifactStoreModeS3
	if requireS3Fields && config.S3Endpoint == "" {
		return errors.New("S3 endpoint is required")
	}
	if config.S3Endpoint != "" {
		if config.S3Endpoint != strings.TrimSpace(config.S3Endpoint) {
			return errors.New("S3 endpoint must not contain surrounding whitespace")
		}
		endpoint, err := url.Parse(config.S3Endpoint)
		if err != nil || endpoint == nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Opaque != "" {
			return errors.New("S3 endpoint must be an absolute URL without userinfo")
		}
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return errors.New("S3 endpoint must use http or https")
		}
		if endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
			return errors.New("S3 endpoint must not contain a query or fragment")
		}
	}

	if requireS3Fields && config.S3Bucket == "" {
		return errors.New("S3 bucket is required")
	}
	if config.S3Bucket != "" {
		if !taskArtifactStoreBucketPattern.MatchString(config.S3Bucket) ||
			strings.Contains(config.S3Bucket, "..") || net.ParseIP(config.S3Bucket) != nil {
			return errors.New("S3 bucket syntax is invalid")
		}
	}

	if requireS3Fields && config.S3Region == "" {
		return errors.New("S3 region is required")
	}
	if config.S3Region != "" && !taskArtifactStoreRegionPattern.MatchString(config.S3Region) {
		return errors.New("S3 region syntax is invalid")
	}
	if err := validateTaskArtifactStoreCredential("access key", config.S3AccessKey, 256, requireS3Fields); err != nil {
		return err
	}
	if err := validateTaskArtifactStoreCredential("secret key", config.S3SecretKey, 1024, requireS3Fields); err != nil {
		return err
	}

	if config.S3Prefix != "" {
		if config.S3Prefix != strings.TrimSpace(config.S3Prefix) || len(config.S3Prefix) > 512 ||
			strings.HasPrefix(config.S3Prefix, "/") || strings.Contains(config.S3Prefix, "\\") {
			return errors.New("S3 prefix syntax is invalid")
		}
		for part := range strings.SplitSeq(config.S3Prefix, "/") {
			if part == "." || part == ".." {
				return errors.New("S3 prefix must not contain dot segments")
			}
		}
		for _, character := range config.S3Prefix {
			if unicode.IsControl(character) {
				return errors.New("S3 prefix must not contain control characters")
			}
		}
	}

	requireWorker := config.Mode == TaskArtifactStoreModeWorkerR2
	if err := validateTaskArtifactHTTPSURL("worker URL", config.WorkerURL, requireWorker); err != nil {
		return err
	}
	if err := validateTaskArtifactHTTPSURL("media base URL", config.MediaBaseURL, requireWorker); err != nil {
		return err
	}
	if err := validateTaskArtifactWorkerCredential("HMAC key id", config.WorkerHMACKeyID, 64, requireWorker); err != nil {
		return err
	}
	if err := validateTaskArtifactWorkerCredential("HMAC secret", config.WorkerHMACSecret, 1024, requireWorker); err != nil {
		return err
	}
	if err := validateTaskArtifactWorkerCredential("media HMAC secret", config.MediaHMACSecret, 1024, requireWorker); err != nil {
		return err
	}
	if requireWorker && len(config.WorkerHMACSecret) < 32 {
		return errors.New("worker HMAC secret must contain at least 32 characters")
	}
	if requireWorker && len(config.MediaHMACSecret) < 32 {
		return errors.New("media HMAC secret must contain at least 32 characters")
	}
	return nil
}

func validateTaskArtifactHTTPSURL(name, value string, required bool) error {
	if required && value == "" {
		return fmt.Errorf("artifact %s is required", name)
	}
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("artifact %s must not contain surrounding whitespace", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("artifact %s must be an absolute HTTPS URL without credentials, query, or fragment", name)
	}
	return nil
}

func validateTaskArtifactWorkerCredential(name, value string, maxLength int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("artifact worker %s is required", name)
	}
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || len(value) > maxLength {
		return fmt.Errorf("artifact worker %s syntax is invalid", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("artifact worker %s syntax is invalid", name)
		}
	}
	return nil
}

func validateTaskArtifactStoreCredential(name, value string, maxLength int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("S3 %s is required", name)
	}
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || len(value) > maxLength {
		return fmt.Errorf("S3 %s syntax is invalid", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("S3 %s syntax is invalid", name)
		}
	}
	return nil
}
