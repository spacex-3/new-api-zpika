package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
	if err := store.SetBarkURL("https://api.day.app/device"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(store.statePath), "feedback-state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("person@example.com")) || bytes.Contains(raw, []byte("api.day.app")) {
		t.Fatal("encrypted state leaked plaintext")
	}
	reloaded, err := NewStore(filepath.Dir(store.statePath), key)
	if err != nil {
		t.Fatal(err)
	}
	items := reloaded.List()
	if len(items) != 1 || items[0].Status != "resolved" || reloaded.BarkURL() == "" {
		t.Fatalf("unexpected reloaded state: %#v", items)
	}
}

func TestBuildBarkURLSupportsEndpointAndTemplate(t *testing.T) {
	plain, err := buildBarkURL("https://api.day.app/device", "新反馈", "邮箱：a@example.com")
	if err != nil || plain == "https://api.day.app/device" {
		t.Fatalf("plain endpoint was not expanded: %q %v", plain, err)
	}
	template, err := buildBarkURL("https://bark.example/key/{{title}}/{{content}}?group=feedback", "标题", "内容")
	if err != nil || template == "" {
		t.Fatalf("template endpoint failed: %q %v", template, err)
	}
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
