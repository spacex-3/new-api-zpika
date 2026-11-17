package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePersistsEncryptedFeedbackAndStatus(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	store, err := NewStore(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	feedback, err := store.Create("person@example.com", "这是一条足够详细的反馈内容")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateStatus(feedback.ID, "resolved"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddReply(feedback.ID, "admin", "", "这是管理员的中文回复"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBarkURL("https://api.day.app/device"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(store.statePath), "feedback-state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("person@example.com")) || bytes.Contains(raw, []byte("api.day.app")) || bytes.Contains(raw, []byte("管理员的中文回复")) {
		t.Fatal("encrypted state leaked plaintext")
	}
	reloaded, err := NewStore(filepath.Dir(store.statePath), key)
	if err != nil {
		t.Fatal(err)
	}
	items := reloaded.List()
	if len(items) != 1 || items[0].Status != "resolved" || len(items[0].Replies) != 1 || reloaded.BarkURL() == "" {
		t.Fatalf("unexpected reloaded state: %#v", items)
	}
}

func TestBuildBarkRequestUsesUTF8JSONForStandardEndpoint(t *testing.T) {
	request, err := buildBarkRequest("https://api.day.app/device-key", "新留言反馈", "邮箱：a@example.com\n\n反馈内容：\n中文内容")
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, request.Method)
	assert.Equal(t, "https://api.day.app/push", request.URL.String())
	assert.Equal(t, "application/json; charset=utf-8", request.Header.Get("Content-Type"))
	body, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "新留言反馈")
	assert.NotContains(t, string(body), "%E6%96%B0")
	var payload map[string]string
	require.NoError(t, json.Unmarshal(body, &payload))
	assert.Equal(t, "device-key", payload["device_key"])
	assert.Equal(t, "邮箱：a@example.com\n\n反馈内容：\n中文内容", payload["body"])

	template, err := buildBarkRequest("https://bark.example/key/{{title}}/{{content}}?group=feedback", "标题", "内容")
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, template.Method)
	assert.Contains(t, template.URL.String(), "group=feedback")
}

func TestStoreEmailLookupAndReplyOwnership(t *testing.T) {
	store, err := NewStore(t.TempDir(), bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	first, err := store.Create("person@example.com", "第一条足够详细的反馈内容")
	require.NoError(t, err)
	_, err = store.Create("other@example.com", "另一条足够详细的反馈内容")
	require.NoError(t, err)

	items := store.ListByEmail("person@example.com")
	require.Len(t, items, 1)
	assert.Equal(t, first.ID, items[0].ID)
	_, err = store.AddReply(first.ID, "client", "wrong@example.com", "错误邮箱的回复")
	assert.ErrorIs(t, err, os.ErrNotExist)
	updated, err := store.AddReply(first.ID, "client", "person@example.com", "客户补充内容")
	require.NoError(t, err)
	require.Len(t, updated.Replies, 1)
	assert.Equal(t, "client", updated.Replies[0].Author)
}

func TestStoreLoadsVersionOneStateWithoutReplies(t *testing.T) {
	dataDir := t.TempDir()
	key := bytes.Repeat([]byte{3}, 32)
	plaintext := []byte(`{"version":1,"feedbacks":[{"id":"fb_old","email":"old@example.com","content":"旧版反馈内容","status":"new","notification_delivered":false,"created_at":"2026-11-17T00:00:00Z","updated_at":"2026-11-17T00:00:00Z"}]}`)
	ciphertext, err := encryptState(key, plaintext)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "feedback-state.enc"), ciphertext, 0o600))

	store, err := NewStore(dataDir, key)
	require.NoError(t, err)
	items := store.List()
	require.Len(t, items, 1)
	assert.Empty(t, items[0].Replies)
}

func TestAdminSessionRejectsMissingCSRF(t *testing.T) {
	auth, err := NewAuth(
		"2bb80d537b1da3e38bd30361aa855686bde0ba2ecb3ce837dcb3f9f6b52a5b2f",
		bytes.Repeat([]byte{4}, 32), "https://feedback.example.com", false, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if _, err := auth.issueSession(recorder); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "https://feedback.example.com/api/admin/settings", nil)
	request.Header.Set("Origin", "https://feedback.example.com")
	for _, cookie := range recorder.Result().Cookies() {
		request.AddCookie(cookie)
	}
	called := false
	auth.requireSession(func(http.ResponseWriter, *http.Request) { called = true })(httptest.NewRecorder(), request)
	if called {
		t.Fatal("state-changing request without CSRF token was accepted")
	}
}

func TestNotificationClientRejectsPrivateDestinationsAndRedirects(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1/device-key",
		"https://[::1]/device-key",
		"https://192.168.1.10/device-key",
	} {
		if err := validateBarkURL(endpoint); err == nil {
			t.Fatalf("validateBarkURL(%q) unexpectedly succeeded", endpoint)
		}
	}

	client := notificationClient()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://127.0.0.1/device-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); err == nil || !strings.Contains(err.Error(), "公网地址") {
		t.Fatalf("expected private destination rejection, got %v", err)
	}

	if err := client.CheckRedirect(&http.Request{}, []*http.Request{{}}); err == nil {
		t.Fatal("expected redirect rejection")
	}
}

func TestAdminLogoutRevokesSession(t *testing.T) {
	auth, err := NewAuth(
		"5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8",
		[]byte("01234567890123456789012345678901"),
		"https://feedback.example.com",
		false,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if _, err := auth.issueSession(recorder); err != nil {
		t.Fatal(err)
	}
	response := recorder.Result()
	defer response.Body.Close()
	if len(response.Cookies()) != 1 {
		t.Fatalf("expected one session cookie, got %d", len(response.Cookies()))
	}
	request := httptest.NewRequest(http.MethodPost, "https://feedback.example.com/api/admin/logout", nil)
	request.AddCookie(response.Cookies()[0])
	if _, ok := auth.session(request); !ok {
		t.Fatal("expected issued session to be active")
	}
	auth.clearSession(httptest.NewRecorder(), request)
	if _, ok := auth.session(request); ok {
		t.Fatal("expected logged out session to be revoked")
	}
}

func TestAdminSessionRejectsInvalidOriginAndExpiredCookie(t *testing.T) {
	sessionKey := []byte("01234567890123456789012345678901")
	auth, err := NewAuth(
		"5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8",
		sessionKey,
		"https://feedback.example.com",
		false,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	claims, err := auth.issueSession(recorder)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "https://feedback.example.com/api/admin/settings", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-CSRF-Token", claims.CSRF)
	request.AddCookie(recorder.Result().Cookies()[0])
	called := false
	auth.requireSession(func(http.ResponseWriter, *http.Request) { called = true })(httptest.NewRecorder(), request)
	if called {
		t.Fatal("state-changing request from an invalid origin was accepted")
	}

	expiresAt := time.Now().Add(-time.Minute).Unix()
	sessionID := "expired-session"
	payload := strconv.FormatInt(expiresAt, 10) + ".csrf-token." + sessionID
	mac := hmac.New(sha256.New, sessionKey)
	_, _ = mac.Write([]byte(payload))
	value := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	auth.sessions[sessionID] = expiresAt
	expiredRequest := httptest.NewRequest(http.MethodGet, "https://feedback.example.com/api/admin/session", nil)
	expiredRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	if _, ok := auth.session(expiredRequest); ok {
		t.Fatal("expired session cookie was accepted")
	}
}

func TestAdminLoginRateLimitAllowsFiveAttemptsPerWindow(t *testing.T) {
	auth, err := NewAuth(
		"5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8",
		[]byte("01234567890123456789012345678901"),
		"https://feedback.example.com",
		false,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://feedback.example.com/api/admin/login", nil)
	request.RemoteAddr = "198.51.100.10:4321"
	for range 5 {
		if !auth.allowLogin(request) {
			t.Fatal("login attempt was blocked before the fifth attempt")
		}
	}
	if auth.allowLogin(request) {
		t.Fatal("sixth login attempt in the same window was accepted")
	}
}
