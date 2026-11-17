package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type appConfig struct {
	ListenAddr         string
	DataDir            string
	AllowedHost        string
	PublicOrigin       string
	TrustCloudflare    bool
	CookieSecure       bool
	EncryptionKeyFile  string
	SessionKeyFile     string
	AdminHashFile      string
	BootstrapBarkFile  string
	EmbedParentOrigins string
}

type App struct {
	store              *Store
	auth               *Auth
	limiter            *submissionLimiter
	client             *http.Client
	allowedHost        string
	embedParentOrigins []string
	static             http.Handler
}

type submissionWindow struct {
	Count     int
	StartedAt time.Time
}

type submissionLimiter struct {
	mu      sync.Mutex
	windows map[string]submissionWindow
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value == "true"
}

func loadConfig() appConfig {
	return appConfig{
		ListenAddr:         envOrDefault("LISTEN_ADDR", ":8080"),
		DataDir:            envOrDefault("DATA_DIR", "/data"),
		AllowedHost:        envOrDefault("ALLOWED_HOST", "kf.zpika.com"),
		PublicOrigin:       envOrDefault("PUBLIC_ORIGIN", "https://kf.zpika.com"),
		TrustCloudflare:    envBool("TRUST_CLOUDFLARE", false),
		CookieSecure:       envBool("COOKIE_SECURE", true),
		EncryptionKeyFile:  envOrDefault("ENCRYPTION_KEY_FILE", "/run/secrets/encryption_key"),
		SessionKeyFile:     envOrDefault("SESSION_KEY_FILE", "/run/secrets/session_key"),
		AdminHashFile:      envOrDefault("ADMIN_TOKEN_HASH_FILE", "/run/secrets/admin_token_hash"),
		BootstrapBarkFile:  envOrDefault("BOOTSTRAP_BARK_URL_FILE", "/run/secrets/bootstrap_bark_url"),
		EmbedParentOrigins: strings.TrimSpace(os.Getenv("EMBED_PARENT_ORIGINS")),
	}
}

func readSecret(path string, decodeBase64 bool) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimSpace(string(data)))
	if !decodeBase64 {
		return data, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(string(data))
	}
	if err != nil {
		return nil, fmt.Errorf("decode secret %s: %w", path, err)
	}
	return decoded, nil
}

func parseEmbedParentOrigins(value string) ([]string, error) {
	var origins []string
	for raw := range strings.SplitSeq(value, ",") {
		origin := strings.TrimRight(strings.TrimSpace(raw), "/")
		if origin == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("invalid embed parent origin %q", origin)
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

func newApp(config appConfig) (*App, error) {
	encryptionKey, err := readSecret(config.EncryptionKeyFile, true)
	if err != nil {
		return nil, fmt.Errorf("load encryption key: %w", err)
	}
	sessionKey, err := readSecret(config.SessionKeyFile, true)
	if err != nil {
		return nil, fmt.Errorf("load session key: %w", err)
	}
	adminHash, err := readSecret(config.AdminHashFile, false)
	if err != nil {
		return nil, fmt.Errorf("load admin token hash: %w", err)
	}
	store, err := NewStore(config.DataDir, encryptionKey)
	if err != nil {
		return nil, err
	}
	if store.BarkURL() == "" {
		if bootstrapURL, readErr := readSecret(config.BootstrapBarkFile, false); readErr == nil && len(bootstrapURL) > 0 {
			if err := validateBarkURL(string(bootstrapURL)); err != nil {
				return nil, fmt.Errorf("bootstrap Bark URL: %w", err)
			}
			if err := store.SetBarkURL(string(bootstrapURL)); err != nil {
				return nil, fmt.Errorf("save bootstrap Bark URL: %w", err)
			}
		}
	}
	auth, err := NewAuth(string(adminHash), sessionKey, config.PublicOrigin, config.TrustCloudflare, config.CookieSecure)
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	origins, err := parseEmbedParentOrigins(config.EmbedParentOrigins)
	if err != nil {
		return nil, err
	}
	return &App{
		store: store, auth: auth, limiter: &submissionLimiter{windows: make(map[string]submissionWindow)},
		client: notificationClient(), allowedHost: config.AllowedHost,
		embedParentOrigins: origins, static: http.FileServer(http.FS(staticFS)),
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式无效"})
		return false
	}
	return true
}

func (l *submissionLimiter) allow(key string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.windows[key]
	if window.StartedAt.IsZero() || now.Sub(window.StartedAt) >= 10*time.Minute {
		window = submissionWindow{StartedAt: now}
	}
	if window.Count >= limit {
		return false
	}
	window.Count++
	l.windows[key] = window
	return true
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if value, _, found := strings.Cut(host, ":"); found {
			host = value
		}
		if a.allowedHost != "" && host != a.allowedHost {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		frameAncestors := "'none'"
		if r.URL.Path == "/embed" && len(a.embedParentOrigins) > 0 {
			frameAncestors = strings.Join(a.embedParentOrigins, " ")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors "+frameAncestors)
		w.Header().Set("Referrer-Policy", "strict-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/feedback", a.createFeedback)
	mux.HandleFunc("POST /api/feedbacks/lookup", a.lookupFeedbacks)
	mux.HandleFunc("POST /api/feedbacks/{id}/replies", a.createClientReply)
	mux.HandleFunc("POST /api/admin/login", a.login)
	mux.HandleFunc("POST /api/admin/logout", a.auth.requireSession(a.logout))
	mux.HandleFunc("GET /api/admin/session", a.auth.requireSession(a.session))
	mux.HandleFunc("GET /api/admin/feedbacks", a.auth.requireSession(a.listFeedbacks))
	mux.HandleFunc("PUT /api/admin/feedbacks/{id}", a.auth.requireSession(a.updateFeedback))
	mux.HandleFunc("POST /api/admin/feedbacks/{id}/replies", a.auth.requireSession(a.createAdminReply))
	mux.HandleFunc("GET /api/admin/settings", a.auth.requireSession(a.adminSettings))
	mux.HandleFunc("PUT /api/admin/settings", a.auth.requireSession(a.updateSettings))
	mux.HandleFunc("POST /api/admin/notifications/test", a.auth.requireSession(a.testNotification))
	mux.HandleFunc("GET /admin", a.serveAdmin)
	mux.HandleFunc("GET /embed", a.serveIndex)
	mux.HandleFunc("GET /", a.serveIndex)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", a.static))
	return a.securityHeaders(mux)
}

func (a *App) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/embed" {
		http.NotFound(w, r)
		return
	}
	a.serveStaticFile(w, "index.html")
}

func (a *App) serveAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	a.serveStaticFile(w, "admin.html")
}

func (a *App) serveStaticFile(w http.ResponseWriter, name string) {
	data, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 254 {
		return "", errors.New("请输入有效邮箱")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || strings.ToLower(address.Address) != value {
		return "", errors.New("请输入有效邮箱")
	}
	return value, nil
}

func normalizeContent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) < 10 {
		return "", errors.New("反馈内容至少需要 10 个字")
	}
	if len([]rune(value)) > 4000 {
		return "", errors.New("反馈内容不能超过 4000 个字")
	}
	return value, nil
}

func normalizeReplyContent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("请输入回复内容")
	}
	if len([]rune(value)) > 4000 {
		return "", errors.New("回复内容不能超过 4000 个字")
	}
	return value, nil
}

type publicFeedback struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Replies   []Reply   `json:"replies"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func publicFeedbacks(feedbacks []Feedback) []publicFeedback {
	result := make([]publicFeedback, len(feedbacks))
	for i, feedback := range feedbacks {
		result[i] = publicFeedback{
			ID: feedback.ID, Content: feedback.Content, Status: feedback.Status,
			Replies: feedback.Replies, CreatedAt: feedback.CreatedAt, UpdatedAt: feedback.UpdatedAt,
		}
	}
	return result
}

func (a *App) createFeedback(w http.ResponseWriter, r *http.Request) {
	if !a.auth.checkOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "请求来源无效"})
		return
	}
	if !a.limiter.allow("create:"+a.auth.clientIP(r), 5, time.Now()) {
		w.Header().Set("Retry-After", "600")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "提交过于频繁，请稍后再试"})
		return
	}
	var request struct {
		Email   string `json:"email"`
		Content string `json:"content"`
		Website string `json:"website"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Website != "" {
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
		return
	}
	email, err := normalizeEmail(request.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	content, err := normalizeContent(request.Content)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	feedback, err := a.store.Create(email, content)
	if err != nil {
		slog.Error("save feedback", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "暂时无法保存反馈，请稍后再试"})
		return
	}
	notification := "邮箱：" + feedback.Email + "\n\n反馈内容：\n" + feedback.Content
	if err := sendBark(a.client, a.store.BarkURL(), "新留言反馈", notification); err != nil {
		slog.Warn("feedback notification failed", "feedback_id", feedback.ID, "error", err)
		_ = a.store.MarkNotification(feedback.ID, false, err.Error())
	} else {
		_ = a.store.MarkNotification(feedback.ID, true, "")
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": feedback.ID})
}

func (a *App) lookupFeedbacks(w http.ResponseWriter, r *http.Request) {
	if !a.auth.checkOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "请求来源无效"})
		return
	}
	if !a.limiter.allow("lookup:"+a.auth.clientIP(r), 20, time.Now()) {
		w.Header().Set("Retry-After", "600")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "查询过于频繁，请稍后再试"})
		return
	}
	var request struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	email, err := normalizeEmail(request.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	feedbacks := publicFeedbacks(a.store.ListByEmail(email))
	writeJSON(w, http.StatusOK, map[string]any{"feedbacks": feedbacks, "total": len(feedbacks)})
}

func (a *App) createClientReply(w http.ResponseWriter, r *http.Request) {
	if !a.auth.checkOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "请求来源无效"})
		return
	}
	if !a.limiter.allow("reply:"+a.auth.clientIP(r), 10, time.Now()) {
		w.Header().Set("Retry-After", "600")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "回复过于频繁，请稍后再试"})
		return
	}
	var request struct {
		Email   string `json:"email"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	email, err := normalizeEmail(request.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	content, err := normalizeReplyContent(request.Content)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	feedback, err := a.store.AddReply(r.PathValue("id"), "client", email, content)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "留言不存在或邮箱不匹配"})
		return
	}
	if err != nil {
		slog.Error("save client reply", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "暂时无法保存回复，请稍后再试"})
		return
	}
	notification := "邮箱：" + email + "\n\n回复内容：\n" + content
	if err := sendBark(a.client, a.store.BarkURL(), "留言有新回复", notification); err != nil {
		slog.Warn("reply notification failed", "feedback_id", feedback.ID, "error", err)
		_ = a.store.MarkNotification(feedback.ID, false, err.Error())
	} else {
		_ = a.store.MarkNotification(feedback.ID, true, "")
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feedback": publicFeedbacks([]Feedback{feedback})[0]})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.auth.checkOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "请求来源无效"})
		return
	}
	if !a.auth.allowLogin(r) {
		w.Header().Set("Retry-After", "900")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "登录尝试过多，请稍后再试"})
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !a.auth.validAdminToken(request.Token) {
		time.Sleep(350 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "管理员口令无效"})
		return
	}
	claims, err := a.auth.issueSession(w)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "无法创建登录会话"})
		return
	}
	a.auth.clearAttempts(r)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": claims.CSRF, "expires_at": claims.ExpiresAt})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.auth.clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) session(w http.ResponseWriter, r *http.Request) {
	info, _ := a.auth.sessionInfo(r)
	writeJSON(w, http.StatusOK, info)
}

func (a *App) listFeedbacks(w http.ResponseWriter, _ *http.Request) {
	feedbacks := a.store.List()
	writeJSON(w, http.StatusOK, map[string]any{"feedbacks": feedbacks, "total": len(feedbacks)})
}

func (a *App) updateFeedback(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Status != "new" && request.Status != "processing" && request.Status != "resolved" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "反馈状态无效"})
		return
	}
	feedback, err := a.store.UpdateStatus(r.PathValue("id"), request.Status)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "反馈不存在"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": feedback})
}

func (a *App) createAdminReply(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	content, err := normalizeReplyContent(request.Content)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	feedback, err := a.store.AddReply(r.PathValue("id"), "admin", "", content)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "反馈不存在"})
		return
	}
	if err != nil {
		slog.Error("save admin reply", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存回复失败"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feedback": feedback})
}

func (a *App) adminSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"bark_configured": a.store.BarkURL() != ""})
}

func (a *App) updateSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BarkURL string `json:"bark_url"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.BarkURL = strings.TrimSpace(request.BarkURL)
	if err := validateBarkURL(request.BarkURL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := a.store.SetBarkURL(request.BarkURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存 Bark 配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bark_configured": request.BarkURL != ""})
}

func (a *App) testNotification(w http.ResponseWriter, _ *http.Request) {
	if err := sendBark(a.client, a.store.BarkURL(), "留言反馈通知测试", "Bark 通知配置可用"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func main() {
	config := loadConfig()
	app, err := newApp(config)
	if err != nil {
		slog.Error("initialize feedback service", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: config.ListenAddr, Handler: app.routes(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		_ = server.Close()
	}()
	slog.Info("feedback service listening", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("feedback service stopped", "error", err)
		os.Exit(1)
	}
}
