package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisabledTaskArtifactStoreHasNoStorageBehavior(t *testing.T) {
	store := GetTaskArtifactStore()
	require.NotNil(t, store)
	assert.False(t, store.Enabled())

	task := &model.Task{TaskID: "task-disabled-store"}
	ref, err := store.Resolve(t.Context(), task, "video")
	require.NoError(t, err)
	assert.Nil(t, ref)

	store.QueuePersist(task, types.TaskArtifact{Key: "video", Type: "video"}, TaskArtifactSource{URL: "https://example.com/video.mp4"})
	assert.ErrorIs(t, store.Serve(&gin.Context{}, task, &StoredArtifactRef{Backend: "worker_r2"}), ErrTaskArtifactStoreDisabled)
	assert.Same(t, store, GetTaskArtifactStore())
}

func TestTaskArtifactExpiryNormalizesSecondsAndMilliseconds(t *testing.T) {
	assert.EqualValues(t, 1010, TaskArtifactExpiresAt(&model.Task{FinishTime: 1000}, 10))
	assert.EqualValues(t, 1_700_000_010, TaskArtifactExpiresAt(&model.Task{FinishTime: 1_700_000_000_000}, 10))
	assert.Zero(t, TaskArtifactExpiresAt(nil, 10))
}

func TestTaskArtifactWorkerHeadersAllowOnlyDownloadCredentials(t *testing.T) {
	headers, err := taskArtifactWorkerHeaders(map[string]string{
		"authorization": "Bearer secret",
		"Accept":        "video/mp4",
	})
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret", headers["Authorization"])
	assert.Equal(t, "video/mp4", headers["Accept"])

	_, err = taskArtifactWorkerHeaders(map[string]string{"Cookie": "session=secret"})
	assert.ErrorIs(t, err, ErrTaskArtifactSourceUnsupported)
	_, err = taskArtifactWorkerHeaders(map[string]string{"Authorization": "Bearer secret\r\nX-Injected: 1"})
	assert.ErrorIs(t, err, ErrTaskArtifactSourceUnsupported)
}

func TestWorkerArtifactObjectKeyIsOpaqueAndDeterministic(t *testing.T) {
	store := &workerR2ArtifactStore{config: testWorkerArtifactConfig()}
	task := &model.Task{TaskID: "task_private_identity", FinishTime: time.Now().Unix()}
	first, err := store.artifactRef(task, "video")
	require.NoError(t, err)
	second, err := store.artifactRef(task, "video")
	require.NoError(t, err)
	assert.Equal(t, first.ObjectKey, second.ObjectKey)
	assert.NotContains(t, first.ObjectKey, task.TaskID)
	assert.NotContains(t, first.ObjectKey, "video")
}

func TestWorkerArtifactResolveWaitsForLocalInFlightUpload(t *testing.T) {
	store := &workerR2ArtifactStore{config: testWorkerArtifactConfig()}
	task := &model.Task{TaskID: "task-inflight", FinishTime: time.Now().Unix()}
	ref, err := store.artifactRef(task, "video")
	require.NoError(t, err)
	done := make(chan struct{})
	store.inflight.Store(ref.ObjectKey, done)
	go func() {
		store.ready.Store(ref.ObjectKey, *ref)
		store.inflight.Delete(ref.ObjectKey)
		close(done)
	}()

	resolved, err := store.Resolve(t.Context(), task, "video")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, ref.ObjectKey, resolved.ObjectKey)
}

func TestWorkerArtifactSignedRequestIncludesConfiguredPathPrefix(t *testing.T) {
	body := []byte(`{"version":"v1"}`)
	secret := "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "/artifact-worker/v1/status", r.URL.Path)
		assert.Equal(t, body, requestBody)

		digest := sha256.Sum256(requestBody)
		message := strings.Join([]string{
			taskArtifactWorkerProtocolVersion,
			http.MethodPost,
			r.URL.EscapedPath(),
			r.Header.Get("X-Zpika-Artifact-Timestamp"),
			r.Header.Get("X-Zpika-Artifact-Nonce"),
			hex.EncodeToString(digest[:]),
		}, "\n")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(message))
		assert.Equal(t, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Zpika-Artifact-Signature"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":false}`))
	}))
	defer server.Close()

	store := &workerR2ArtifactStore{
		config: system_setting.TaskArtifactStoreConfig{
			WorkerURL: server.URL + "/artifact-worker", WorkerHMACKeyID: "v1", WorkerHMACSecret: secret,
		},
		client: server.Client(),
	}
	response, err := store.doSignedRequest(t.Context(), "/v1/status", body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func TestWorkerArtifactServeRedirectsToSignedPrefixedMediaURL(t *testing.T) {
	config := testWorkerArtifactConfig()
	config.MediaBaseURL = "https://media.example.com/artifacts"
	store := &workerR2ArtifactStore{config: config}
	ref := &StoredArtifactRef{
		Backend: system_setting.TaskArtifactStoreModeWorkerR2, ObjectKey: "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/artifact", nil)

	require.NoError(t, store.Serve(c, &model.Task{}, ref))
	assert.Equal(t, http.StatusTemporaryRedirect, recorder.Code)
	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/artifacts/v1/media", location.Path)
	decodedKey, err := base64.RawURLEncoding.DecodeString(location.Query().Get("key"))
	require.NoError(t, err)
	assert.Equal(t, ref.ObjectKey, string(decodedKey))
	expires, err := strconv.ParseInt(location.Query().Get("expires"), 10, 64)
	require.NoError(t, err)
	assert.LessOrEqual(t, expires, ref.ExpiresAt)
	mac := hmac.New(sha256.New, []byte(config.MediaHMACSecret))
	_, _ = mac.Write(taskArtifactMediaSignatureMessage(ref.ObjectKey, expires))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), location.Query().Get("sig"))
}

func TestWorkerArtifactPersistCarriesCredentialOnlyInSignedJob(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var job workerArtifactIngestRequest
		require.NoError(t, common.Unmarshal(body, &job))
		assert.Equal(t, "https://video.example.com", job.Source.CredentialOrigin)
		assert.Equal(t, "Bearer channel-secret", job.Source.Headers["Authorization"])
		assert.False(t, job.Source.Credentialless)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	store := &workerR2ArtifactStore{
		config: system_setting.TaskArtifactStoreConfig{
			WorkerURL: server.URL, WorkerHMACKeyID: "v1", WorkerHMACSecret: secret, WorkerTimeoutSeconds: 10,
		},
		client: server.Client(),
	}
	job := workerArtifactJob{
		artifact: types.TaskArtifact{Key: "video", Type: "video", MimeType: "video/mp4"},
		source: TaskArtifactSource{
			URL: "https://video.example.com/protected.mp4?token=opaque", Method: http.MethodGet,
			Headers: map[string]string{"Authorization": "Bearer channel-secret"},
		},
		ref: StoredArtifactRef{ObjectKey: "new-api/v1/2026-11-10/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}
	require.NoError(t, store.persist(t.Context(), job))
}

func testWorkerArtifactConfig() system_setting.TaskArtifactStoreConfig {
	return system_setting.TaskArtifactStoreConfig{
		Mode:                system_setting.TaskArtifactStoreModeWorkerR2,
		WorkerHMACSecret:    "0123456789abcdef0123456789abcdef",
		MediaHMACSecret:     "abcdef0123456789abcdef0123456789",
		S3PresignTTLSeconds: 900,
		RetentionSeconds:    86400,
	}
}

var _ TaskArtifactStore = (*disabledArtifactStore)(nil)
