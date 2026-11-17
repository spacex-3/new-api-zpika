package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "feedback_admin_session"

type sessionClaims struct {
	ExpiresAt int64
	CSRF      string
	ID        string
}

type loginAttempt struct {
	Count       int
	WindowStart time.Time
}

type Auth struct {
	adminHash    []byte
	sessionKey   []byte
	publicOrigin string
	trustCF      bool
	cookieSecure bool
	mu           sync.Mutex
	attempts     map[string]loginAttempt
	sessions     map[string]int64
}

func NewAuth(adminHashHex string, sessionKey []byte, publicOrigin string, trustCF, cookieSecure bool) (*Auth, error) {
	adminHash, err := hex.DecodeString(strings.TrimSpace(adminHashHex))
	if err != nil || len(adminHash) != sha256.Size {
		return nil, errors.New("admin token hash must be a SHA-256 hex digest")
	}
	if len(sessionKey) < 32 {
		return nil, errors.New("session key must contain at least 32 bytes")
	}
	return &Auth{
		adminHash: adminHash, sessionKey: sessionKey,
		publicOrigin: strings.TrimRight(publicOrigin, "/"), trustCF: trustCF,
		cookieSecure: cookieSecure, attempts: make(map[string]loginAttempt),
		sessions: make(map[string]int64),
	}, nil
}

func (a *Auth) clientIP(r *http.Request) string {
	if a.trustCF {
		if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
			return ip
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host := r.RemoteAddr
	if value, _, found := strings.Cut(host, ":"); found {
		return strings.Trim(value, "[]")
	}
	return host
}

func (a *Auth) allowLogin(r *http.Request) bool {
	ip := a.clientIP(r)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts[ip]
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) >= 15*time.Minute {
		attempt = loginAttempt{WindowStart: now}
	}
	if attempt.Count >= 5 {
		return false
	}
	attempt.Count++
	a.attempts[ip] = attempt
	return true
}

func (a *Auth) clearAttempts(r *http.Request) {
	a.mu.Lock()
	delete(a.attempts, a.clientIP(r))
	a.mu.Unlock()
}

func (a *Auth) checkOrigin(r *http.Request) bool {
	if a.publicOrigin == "" {
		return true
	}
	return strings.TrimRight(r.Header.Get("Origin"), "/") == a.publicOrigin
}

func (a *Auth) validAdminToken(token string) bool {
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], a.adminHash) == 1
}

func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *Auth) issueSession(w http.ResponseWriter) (sessionClaims, error) {
	csrf, err := randomToken(24)
	if err != nil {
		return sessionClaims{}, err
	}
	sessionID, err := randomToken(24)
	if err != nil {
		return sessionClaims{}, err
	}
	claims := sessionClaims{ExpiresAt: time.Now().Add(12 * time.Hour).Unix(), CSRF: csrf, ID: sessionID}
	payload := strconv.FormatInt(claims.ExpiresAt, 10) + "." + claims.CSRF + "." + claims.ID
	mac := hmac.New(sha256.New, a.sessionKey)
	_, _ = mac.Write([]byte(payload))
	value := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	a.mu.Lock()
	for id, expiresAt := range a.sessions {
		if time.Now().Unix() >= expiresAt {
			delete(a.sessions, id)
		}
	}
	a.sessions[claims.ID] = claims.ExpiresAt
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/", MaxAge: 12 * 60 * 60,
		HttpOnly: true, Secure: a.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return claims, nil
}

func (a *Auth) clearSession(w http.ResponseWriter, r *http.Request) {
	if claims, ok := a.session(r); ok {
		a.mu.Lock()
		delete(a.sessions, claims.ID)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (a *Auth) session(r *http.Request) (sessionClaims, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sessionClaims{}, false
	}
	encodedPayload, encodedMAC, found := strings.Cut(cookie.Value, ".")
	if !found {
		return sessionClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return sessionClaims{}, false
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil {
		return sessionClaims{}, false
	}
	mac := hmac.New(sha256.New, a.sessionKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), providedMAC) {
		return sessionClaims{}, false
	}
	expiresText, remainder, found := strings.Cut(string(payload), ".")
	if !found {
		return sessionClaims{}, false
	}
	csrf, sessionID, found := strings.Cut(remainder, ".")
	if !found || csrf == "" || sessionID == "" {
		return sessionClaims{}, false
	}
	expiresAt, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil || time.Now().Unix() >= expiresAt {
		return sessionClaims{}, false
	}
	a.mu.Lock()
	activeExpiresAt, active := a.sessions[sessionID]
	if active && time.Now().Unix() >= activeExpiresAt {
		delete(a.sessions, sessionID)
		active = false
	}
	a.mu.Unlock()
	if !active || activeExpiresAt != expiresAt {
		return sessionClaims{}, false
	}
	return sessionClaims{ExpiresAt: expiresAt, CSRF: csrf, ID: sessionID}, true
}

func (a *Auth) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := a.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "需要管理员登录"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !a.checkOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(claims.CSRF)) != 1 {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "请求验证失败"})
				return
			}
		}
		next(w, r)
	}
}

func (a *Auth) sessionInfo(r *http.Request) (map[string]any, error) {
	claims, ok := a.session(r)
	if !ok {
		return nil, fmt.Errorf("not authenticated")
	}
	return map[string]any{"authenticated": true, "csrf_token": claims.CSRF, "expires_at": claims.ExpiresAt}, nil
}
