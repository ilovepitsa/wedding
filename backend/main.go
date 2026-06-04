package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxFileSize = 1 << 30

var allowedMIMEs = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

var allowedExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
	".heic": true,
	".heif": true,
}

var httpClient = &http.Client{Timeout: 600 * time.Second}

// tokens is nil in mock mode (YANDEX_API_BASE is set).
var tokens *TokenManager

// verificationRedirectURI is the fixed Yandex OAuth redirect for apps without a web callback.
const verificationRedirectURI = "https://oauth.y" + "andex.ru/verification_code"

func apiBase() string {
	if v := os.Getenv("YANDEX_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://cloud-api.y" + "andex" + ".net/v1/disk"
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func setupLogging() {
	logPath := getEnv("LOG_FILE", "/app/logs/backend.log")

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		log.Printf("WARNING: cannot create log dir: %v", err)
		return
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("WARNING: cannot open log file %s: %v", logPath, err)
		return
	}

	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
	log.Printf("logging to %s", logPath)
}

func main() {
	setupLogging()
	port := getEnv("PORT", "8080")

	mockMode := os.Getenv("YANDEX_API_BASE") != ""
	if mockMode {
		log.Printf("mock mode: API base = %s", os.Getenv("YANDEX_API_BASE"))
	} else {
		clientID := os.Getenv("YANDEX_CLIENT_ID")
		clientSecret := os.Getenv("YANDEX_CLIENT_SECRET")
		refreshToken := os.Getenv("YANDEX_REFRESH_TOKEN")

		if clientID == "" || clientSecret == "" {
			log.Fatal("YANDEX_CLIENT_ID и YANDEX_CLIENT_SECRET обязательны")
		}
		tokens = NewTokenManager(clientID, clientSecret, refreshToken)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/upload", uploadHandler)
	mux.HandleFunc("/nikita/dasha/14062026/wedding/auth/love/login", authLoginHandler)
	mux.HandleFunc("/nikita/dasha/14062026/wedding/auth/love/submit", authSubmitHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func authLoginHandler(w http.ResponseWriter, r *http.Request) {
	if tokens == nil {
		http.Error(w, "mock mode — auth not needed", http.StatusNotFound)
		return
	}

	authURL := tokens.AuthURL(verificationRedirectURI, "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Авторизация</title>
<style>
body{font-family:sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fdf8f2}
.box{max-width:480px;width:100%%;padding:40px;background:#fff;border-radius:16px;box-shadow:0 4px 24px rgba(0,0,0,.08);text-align:center}
h2{color:#3a2118;margin-bottom:8px}p{color:#9c7565;line-height:1.6}
.step{text-align:left;margin:24px 0;padding:20px;background:#fdf8f2;border-radius:12px}
.step b{display:block;margin-bottom:8px;color:#3a2118}
a.btn{display:inline-block;padding:12px 28px;background:#c8896a;color:#fff;border-radius:50px;text-decoration:none;font-size:.95rem}
a.btn:hover{background:#a86248}
input{width:100%%;box-sizing:border-box;padding:12px 16px;border:1px solid #e0d0c0;border-radius:8px;font-size:1rem;margin:8px 0}
button{padding:12px 28px;background:#3a2118;color:#fff;border:none;border-radius:50px;font-size:.95rem;cursor:pointer}
</style></head><body><div class="box">
<h2>Авторизация Яндекс.Диска</h2>
<p>Нужно один раз войти, чтобы приложение получило доступ к Диску.</p>
<div class="step">
  <b>Шаг 1 — откройте Яндекс и разрешите доступ</b>
  <a class="btn" href="%s" target="_blank">Открыть Яндекс →</a>
</div>
<div class="step">
  <b>Шаг 2 — введите код, который показал Яндекс</b>
  <form method="POST" action="/nikita/dasha/14062026/wedding/auth/love/submit">
    <input name="code" placeholder="Вставьте код сюда" autofocus autocomplete="off">
    <button type="submit">Подтвердить</button>
  </form>
</div>
</div></body></html>`, authURL)
}

func authSubmitHandler(w http.ResponseWriter, r *http.Request) {
	if tokens == nil {
		http.Error(w, "mock mode — auth not needed", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/nikita/dasha/14062026/wedding/auth/love/login", http.StatusFound)
		return
	}

	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		http.Redirect(w, r, "/nikita/dasha/14062026/wedding/auth/love/login", http.StatusFound)
		return
	}

	if err := tokens.ExchangeCode(code, verificationRedirectURI); err != nil {
		log.Printf("ExchangeCode: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Ошибка</title>
<style>body{font-family:sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fdf8f2}
.box{padding:40px;background:#fff;border-radius:16px;box-shadow:0 4px 24px rgba(0,0,0,.08);text-align:center;max-width:420px}
h2{color:#b84a40}a{color:#c8896a}</style></head>
<body><div class="box"><h2>Ошибка авторизации</h2><p>%s</p><p><a href="/nikita/dasha/14062026/wedding/auth/love/login">← Попробовать снова</a></p></div></body></html>`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Готово</title>
<style>body{font-family:sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fdf8f2}
.box{padding:40px;background:#fff;border-radius:16px;box-shadow:0 4px 24px rgba(0,0,0,.08);text-align:center;max-width:420px}
h2{color:#3a2118}p{color:#9c7565}a{color:#c8896a}</style></head>
<body><div class="box"><h2>✓ Авторизация успешна</h2>
<p>Приложение готово к работе.<br>Refresh token сохранён в логах сервера.</p>
<p><a href="/">Открыть сайт →</a></p></div></body></html>`))
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		jsonError(w, "Файл слишком большой (макс. 1 ГБ)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("photo")
	if err != nil {
		jsonError(w, "Поле «photo» обязательно", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		jsonError(w, "Ошибка чтения файла", http.StatusInternalServerError)
		return
	}

	mime := http.DetectContentType(data)
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedMIMEs[mime] && !allowedExts[ext] {
		jsonError(w, "Разрешены только изображения (JPEG, PNG, WEBP, HEIC)", http.StatusBadRequest)
		return
	}

	// Resolve token (empty string in mock mode — Yandex mock ignores auth).
	var token string
	if tokens != nil {
		token, err = tokens.Token()
		if err != nil {
			jsonError(w, "Сервис временно недоступен: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	folder := strings.TrimRight(getEnv("YANDEX_FOLDER", "/wedding/photos"), "/")
	filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), sanitize(header.Filename))
	remotePath := folder + "/" + filename

	uploadURL, err := getYandexUploadURL(token, remotePath)
	if err != nil {
		log.Printf("yd upload url: %v", err)
		jsonError(w, "Ошибка запроса URL для загрузки", http.StatusBadGateway)
		return
	}

	if err := putToYandex(uploadURL, data, mime); err != nil {
		log.Printf("yd put: %v", err)
		jsonError(w, "Ошибка загрузки на Яндекс.Диск", http.StatusBadGateway)
		return
	}

	log.Printf("uploaded %s (%d bytes)", remotePath, len(data))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func getYandexUploadURL(token, path string) (string, error) {
	apiURL := apiBase() + "/resources/upload?path=" + url.QueryEscape(path) + "&overwrite=false"

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "OAuth "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Href string `json:"href"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.Href, nil
}

func putToYandex(uploadURL string, data []byte, contentType string) error {
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", contentType)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put status %d: %s", resp.StatusCode, body)
	}
	return nil
}

func sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
