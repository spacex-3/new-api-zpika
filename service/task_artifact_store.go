package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/http/httpguts"
)

const (
	taskArtifactWorkerProtocolVersion = "v1"
	taskArtifactWorkerQueueSize       = 128
	taskArtifactResolveTimeout        = 3 * time.Second
	taskArtifactSourceResolveTimeout  = 45 * time.Second
)

var (
	ErrTaskArtifactStoreDisabled     = errors.New("task artifact store is disabled")
	ErrTaskArtifactExpired           = errors.New("task artifact has expired")
	ErrTaskArtifactSourceUnsupported = errors.New("task artifact source is unsupported by worker storage")
)

// StoredArtifactRef identifies one object in the external artifact store.
type StoredArtifactRef struct {
	Backend   string
	ObjectKey string
	MimeType  string
	Size      int64
	ExpiresAt int64
}

// TaskArtifactSource is the already-resolved provider request. Credentials
// remain in memory and are sent only to the authenticated ingest Worker.
type TaskArtifactSource struct {
	URL            string
	Method         string
	Headers        map[string]string
	Body           []byte
	Credentialless bool
	ContentPath    []any
	MimeType       string
}

// TaskArtifactStore persists provider artifacts without changing task rows.
type TaskArtifactStore interface {
	Enabled() bool
	Resolve(ctx context.Context, task *model.Task, artifactKey string) (*StoredArtifactRef, error)
	QueuePersist(task *model.Task, artifact types.TaskArtifact, source TaskArtifactSource)
	Serve(c *gin.Context, task *model.Task, ref *StoredArtifactRef) error
}

// TaskArtifactPersistenceProvider is implemented by task adaptors whose plugin
// can project artifacts and resolve their provider download requests.
type TaskArtifactPersistenceProvider interface {
	ListArtifacts(task *model.Task) ([]types.TaskArtifact, error)
	BuildArtifactSourceRequest(ctx context.Context, task *model.Task, artifactKey string) (*TaskArtifactSource, error)
}

type disabledArtifactStore struct{}

func (disabledArtifactStore) Enabled() bool { return false }

func (disabledArtifactStore) Resolve(context.Context, *model.Task, string) (*StoredArtifactRef, error) {
	return nil, nil
}

func (disabledArtifactStore) QueuePersist(*model.Task, types.TaskArtifact, TaskArtifactSource) {}

func (disabledArtifactStore) Serve(*gin.Context, *model.Task, *StoredArtifactRef) error {
	return ErrTaskArtifactStoreDisabled
}

type workerArtifactJob struct {
	task     model.Task
	artifact types.TaskArtifact
	source   TaskArtifactSource
	ref      StoredArtifactRef
	done     chan struct{}
}

type workerR2ArtifactStore struct {
	config   system_setting.TaskArtifactStoreConfig
	client   *http.Client
	jobs     chan workerArtifactJob
	inflight sync.Map
	ready    sync.Map
}

type workerArtifactStatusRequest struct {
	Version   string `json:"version"`
	ObjectKey string `json:"object_key"`
	ExpiresAt int64  `json:"expires_at"`
}

type workerArtifactStatusResponse struct {
	Ready    bool   `json:"ready"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type workerArtifactSourceRequest struct {
	URL              string            `json:"url"`
	Method           string            `json:"method"`
	Headers          map[string]string `json:"headers,omitempty"`
	BodyBase64       string            `json:"body_base64,omitempty"`
	Credentialless   bool              `json:"credentialless"`
	CredentialOrigin string            `json:"credential_origin,omitempty"`
}

type workerArtifactIngestRequest struct {
	Version      string                      `json:"version"`
	ObjectKey    string                      `json:"object_key"`
	ExpiresAt    int64                       `json:"expires_at"`
	ArtifactType string                      `json:"artifact_type"`
	MimeType     string                      `json:"mime_type,omitempty"`
	MaxBytes     int64                       `json:"max_bytes"`
	Source       workerArtifactSourceRequest `json:"source"`
}

var taskArtifactStore TaskArtifactStore = &disabledArtifactStore{}

var taskArtifactSourceSlots = make(chan struct{}, 8)

func init() {
	config := system_setting.LoadTaskArtifactStoreConfig()
	if config.Mode == system_setting.TaskArtifactStoreModeWorkerR2 {
		taskArtifactStore = newWorkerR2ArtifactStore(config)
	}
}

func newWorkerR2ArtifactStore(config system_setting.TaskArtifactStoreConfig) *workerR2ArtifactStore {
	store := &workerR2ArtifactStore{
		config: config,
		client: &http.Client{
			Transport: http.DefaultTransport,
			Timeout:   time.Duration(config.WorkerTimeoutSeconds) * time.Second,
		},
		jobs: make(chan workerArtifactJob, taskArtifactWorkerQueueSize),
	}
	for range config.WorkerConcurrency {
		go store.runWorker()
	}
	return store
}

// GetTaskArtifactStore returns the process-wide artifact storage backend.
func GetTaskArtifactStore() TaskArtifactStore { return taskArtifactStore }

// ScheduleTaskArtifactPersistence resolves provider requests outside the poll
// critical path. A saturated resolver is harmless: first artifact access will
// schedule the same deterministic object again.
func ScheduleTaskArtifactPersistence(provider TaskArtifactPersistenceProvider, task *model.Task) {
	store := GetTaskArtifactStore()
	if !store.Enabled() || provider == nil || task == nil || task.Status != model.TaskStatusSuccess || !task.ResultRetrievable() {
		return
	}
	select {
	case taskArtifactSourceSlots <- struct{}{}:
	default:
		return
	}
	taskCopy := *task
	taskCopy.Data = append([]byte(nil), task.Data...)
	go func() {
		defer func() { <-taskArtifactSourceSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), taskArtifactSourceResolveTimeout)
		defer cancel()
		artifacts, err := provider.ListArtifacts(&taskCopy)
		if err != nil {
			logger.LogWarn(ctx, common.LogText("task artifact projection failed for task %s", taskCopy.TaskID))
			return
		}
		for _, artifact := range artifacts {
			source, sourceErr := provider.BuildArtifactSourceRequest(ctx, &taskCopy, artifact.Key)
			if sourceErr != nil || source == nil {
				logger.LogWarn(ctx, common.LogText("task artifact source resolution failed for task %s artifact %s", taskCopy.TaskID, artifact.Key))
				continue
			}
			store.QueuePersist(&taskCopy, artifact, *source)
		}
	}()
}

func (s *workerR2ArtifactStore) Enabled() bool { return s != nil }

func (s *workerR2ArtifactStore) Resolve(ctx context.Context, task *model.Task, artifactKey string) (*StoredArtifactRef, error) {
	ref, err := s.artifactRef(task, artifactKey)
	if err != nil {
		return nil, err
	}
	if cached, ok := s.ready.Load(ref.ObjectKey); ok {
		stored := cached.(StoredArtifactRef)
		if time.Now().Unix() < stored.ExpiresAt {
			return &stored, nil
		}
		s.ready.Delete(ref.ObjectKey)
	}
	resolveCtx, cancel := context.WithTimeout(ctx, taskArtifactResolveTimeout)
	defer cancel()
	if pending, ok := s.inflight.Load(ref.ObjectKey); ok {
		select {
		case <-pending.(chan struct{}):
			if cached, ready := s.ready.Load(ref.ObjectKey); ready {
				stored := cached.(StoredArtifactRef)
				return &stored, nil
			}
		case <-resolveCtx.Done():
			return nil, resolveCtx.Err()
		}
	}

	requestBody, err := common.Marshal(workerArtifactStatusRequest{
		Version: taskArtifactWorkerProtocolVersion, ObjectKey: ref.ObjectKey, ExpiresAt: ref.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	resp, err := s.doSignedRequest(resolveCtx, "/v1/status", requestBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artifact worker status returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	var status workerArtifactStatusResponse
	if err = common.Unmarshal(body, &status); err != nil || !status.Ready {
		return nil, nil
	}
	ref.MimeType = status.MimeType
	ref.Size = status.Size
	s.ready.Store(ref.ObjectKey, *ref)
	return ref, nil
}

func (s *workerR2ArtifactStore) QueuePersist(task *model.Task, artifact types.TaskArtifact, source TaskArtifactSource) {
	if task == nil || len(source.ContentPath) != 0 || strings.HasPrefix(strings.TrimSpace(source.URL), "data:") {
		return
	}
	ref, err := s.artifactRef(task, artifact.Key)
	if err != nil || time.Now().Unix() >= ref.ExpiresAt {
		return
	}
	if _, ok := s.ready.Load(ref.ObjectKey); ok {
		return
	}
	done := make(chan struct{})
	if _, loaded := s.inflight.LoadOrStore(ref.ObjectKey, done); loaded {
		return
	}
	job := workerArtifactJob{
		task:     *task,
		artifact: artifact,
		source: TaskArtifactSource{
			URL: source.URL, Method: source.Method, Credentialless: source.Credentialless,
			MimeType: source.MimeType, Body: append([]byte(nil), source.Body...),
			Headers: cloneStringMap(source.Headers),
		},
		ref:  *ref,
		done: done,
	}
	select {
	case s.jobs <- job:
	default:
		s.inflight.Delete(ref.ObjectKey)
		close(done)
	}
}

func (s *workerR2ArtifactStore) Serve(c *gin.Context, _ *model.Task, ref *StoredArtifactRef) error {
	if ref == nil || ref.Backend != system_setting.TaskArtifactStoreModeWorkerR2 || time.Now().Unix() >= ref.ExpiresAt {
		return ErrTaskArtifactExpired
	}
	expires := min(time.Now().Unix()+int64(s.config.S3PresignTTLSeconds), ref.ExpiresAt)
	message := taskArtifactMediaSignatureMessage(ref.ObjectKey, expires)
	mac := hmac.New(sha256.New, []byte(s.config.MediaHMACSecret))
	_, _ = mac.Write(message)
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	mediaURL, err := url.Parse(s.config.MediaBaseURL)
	if err != nil {
		return err
	}
	mediaURL.Path = strings.TrimRight(mediaURL.Path, "/") + "/v1/media"
	query := mediaURL.Query()
	query.Set("key", base64.RawURLEncoding.EncodeToString([]byte(ref.ObjectKey)))
	query.Set("expires", strconv.FormatInt(expires, 10))
	query.Set("sig", signature)
	mediaURL.RawQuery = query.Encode()
	c.Header("Cache-Control", "private, no-store")
	c.Redirect(http.StatusTemporaryRedirect, mediaURL.String())
	return nil
}

func (s *workerR2ArtifactStore) runWorker() {
	for job := range s.jobs {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.config.WorkerTimeoutSeconds)*time.Second)
		err := s.persist(ctx, job)
		cancel()
		if err == nil {
			s.ready.Store(job.ref.ObjectKey, job.ref)
		}
		s.inflight.Delete(job.ref.ObjectKey)
		close(job.done)
		if err != nil {
			logger.LogWarn(context.Background(), common.LogText("task artifact persistence failed for task %s artifact %s", job.task.TaskID, job.artifact.Key))
			continue
		}
	}
}

func (s *workerR2ArtifactStore) persist(ctx context.Context, job workerArtifactJob) error {
	sourceURL, err := url.Parse(strings.TrimSpace(job.source.URL))
	if err != nil || sourceURL == nil || (sourceURL.Scheme != "http" && sourceURL.Scheme != "https") || sourceURL.Host == "" || sourceURL.User != nil || sourceURL.Fragment != "" {
		return ErrTaskArtifactSourceUnsupported
	}
	method := strings.ToUpper(strings.TrimSpace(job.source.Method))
	if method == "" || method == http.MethodHead {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return ErrTaskArtifactSourceUnsupported
	}
	headers, err := taskArtifactWorkerHeaders(job.source.Headers)
	if err != nil {
		return err
	}
	if job.source.Credentialless && (len(headers) != 0 || len(job.source.Body) != 0 || method != http.MethodGet) {
		return ErrTaskArtifactSourceUnsupported
	}

	maxBytes := int64(constant.MaxFileDownloadMB) << 20
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	origin := sourceURL.Scheme + "://" + sourceURL.Host
	ingest := workerArtifactIngestRequest{
		Version:      taskArtifactWorkerProtocolVersion,
		ObjectKey:    job.ref.ObjectKey,
		ExpiresAt:    job.ref.ExpiresAt,
		ArtifactType: job.artifact.Type,
		MimeType:     firstNonEmpty(job.artifact.MimeType, job.source.MimeType),
		MaxBytes:     maxBytes,
		Source: workerArtifactSourceRequest{
			URL: job.source.URL, Method: method, Headers: headers,
			Credentialless:   job.source.Credentialless,
			CredentialOrigin: origin,
		},
	}
	if len(job.source.Body) > 0 {
		ingest.Source.BodyBase64 = base64.StdEncoding.EncodeToString(job.source.Body)
	}
	body, err := common.Marshal(ingest)
	if err != nil {
		return err
	}
	resp, err := s.doSignedRequest(ctx, "/v1/ingest", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("artifact worker ingest returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *workerR2ArtifactStore) doSignedRequest(ctx context.Context, path string, body []byte) (*http.Response, error) {
	workerURL, err := url.Parse(s.config.WorkerURL)
	if err != nil {
		return nil, err
	}
	workerURL.Path = strings.TrimRight(workerURL.Path, "/") + path
	signedPath := workerURL.EscapedPath()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 18)
	if _, err = rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	digest := sha256.Sum256(body)
	message := strings.Join([]string{
		taskArtifactWorkerProtocolVersion,
		http.MethodPost,
		signedPath,
		timestamp,
		nonce,
		hex.EncodeToString(digest[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte(s.config.WorkerHMACSecret))
	_, _ = mac.Write([]byte(message))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workerURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Zpika-Artifact-Key-Id", s.config.WorkerHMACKeyID)
	req.Header.Set("X-Zpika-Artifact-Timestamp", timestamp)
	req.Header.Set("X-Zpika-Artifact-Nonce", nonce)
	req.Header.Set("X-Zpika-Artifact-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return s.client.Do(req)
}

func (s *workerR2ArtifactStore) artifactRef(task *model.Task, artifactKey string) (*StoredArtifactRef, error) {
	if task == nil || strings.TrimSpace(task.TaskID) == "" || strings.TrimSpace(artifactKey) == "" {
		return nil, errors.New("task artifact identity is invalid")
	}
	expiresAt := TaskArtifactExpiresAt(task, s.config.RetentionSeconds)
	if time.Now().Unix() >= expiresAt {
		return nil, ErrTaskArtifactExpired
	}
	finishedAt := normalizedTaskArtifactTime(task.FinishTime)
	if finishedAt == 0 {
		finishedAt = normalizedTaskArtifactTime(task.UpdatedAt)
	}
	if finishedAt == 0 {
		finishedAt = time.Now().Unix()
	}
	mac := hmac.New(sha256.New, []byte(s.config.WorkerHMACSecret))
	_, _ = mac.Write([]byte(task.TaskID + "\x00" + artifactKey))
	digest := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	objectKey := "new-api/v1/" + time.Unix(finishedAt, 0).UTC().Format("2006-01-02") + "/" + digest
	return &StoredArtifactRef{
		Backend: system_setting.TaskArtifactStoreModeWorkerR2, ObjectKey: objectKey, ExpiresAt: expiresAt,
	}, nil
}

// TaskArtifactExpiresAt returns the logical artifact expiry in Unix seconds.
func TaskArtifactExpiresAt(task *model.Task, retentionSeconds int) int64 {
	if task == nil || retentionSeconds <= 0 {
		return 0
	}
	finishedAt := normalizedTaskArtifactTime(task.FinishTime)
	if finishedAt == 0 {
		finishedAt = normalizedTaskArtifactTime(task.UpdatedAt)
	}
	if finishedAt == 0 {
		finishedAt = normalizedTaskArtifactTime(task.CreatedAt)
	}
	if finishedAt == 0 {
		finishedAt = time.Now().Unix()
	}
	return finishedAt + int64(retentionSeconds)
}

func normalizedTaskArtifactTime(value int64) int64 {
	if value > 100_000_000_000 {
		return value / 1000
	}
	return value
}

func taskArtifactWorkerHeaders(headers map[string]string) (map[string]string, error) {
	allowed := map[string]struct{}{
		"Accept": {}, "Authorization": {}, "Content-Type": {}, "Api-Key": {}, "X-Api-Key": {}, "X-Goog-Api-Key": {},
	}
	result := make(map[string]string, len(headers))
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if _, ok := allowed[canonical]; !ok || !httpguts.ValidHeaderFieldName(canonical) || !httpguts.ValidHeaderFieldValue(value) || len(value) > 8<<10 {
			return nil, ErrTaskArtifactSourceUnsupported
		}
		result[canonical] = value
	}
	return result, nil
}

func taskArtifactMediaSignatureMessage(objectKey string, expires int64) []byte {
	return []byte(taskArtifactWorkerProtocolVersion + "\n" + objectKey + "\n" + strconv.FormatInt(expires, 10))
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

var _ TaskArtifactStore = disabledArtifactStore{}
var _ TaskArtifactStore = (*workerR2ArtifactStore)(nil)
