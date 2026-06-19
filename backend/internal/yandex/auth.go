package yandex

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	oauthBase     = "https://oauth.y" + "andex.ru"
	oauthAuthURL  = oauthBase + "/authorize"
	oauthTokenURL = oauthBase + "/token"

	// verificationRedirectURI is the fixed Yandex OAuth redirect for apps without a web callback.
	VerificationRedirectURI = "https://oauth.y" + "andex.ru/verification_code"
)

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// TokenManager holds OAuth tokens and keeps them fresh automatically.
type TokenManager struct {
	mu               sync.RWMutex
	accessToken      string
	expiresAt        time.Time
	refreshToken     string
	clientID         string
	clientSecret     string
	ready            bool
	keepAliveStarted bool
}

func NewTokenManager(clientID, clientSecret, refreshToken string) *TokenManager {
	tm := &TokenManager{
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
	}
	if refreshToken != "" {
		if err := tm.doRefresh(); err != nil {
			log.Printf("WARNING: initial token refresh failed: %v", err)
			log.Println("visit /auth/login to re-authorize")
		} else {
			tm.ready = true
			go tm.keepAlive()
			tm.keepAliveStarted = true
		}
	} else {
		log.Println("no refresh token set — visit /auth/login to authorize")
	}
	return tm
}

func (tm *TokenManager) IsReady() bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.ready
}

// Token returns a valid access token, refreshing if close to expiry.
func (tm *TokenManager) Token() (string, error) {
	tm.mu.RLock()
	if !tm.ready {
		tm.mu.RUnlock()
		return "", fmt.Errorf("не авторизован — откройте /auth/login")
	}
	if time.Now().Before(tm.expiresAt.Add(-2 * time.Minute)) {
		t := tm.accessToken
		tm.mu.RUnlock()
		return t, nil
	}
	tm.mu.RUnlock()
	return tm.refresh()
}

func (tm *TokenManager) refresh() (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if time.Now().Before(tm.expiresAt.Add(-2 * time.Minute)) {
		return tm.accessToken, nil
	}
	if err := tm.doRefresh(); err != nil {
		return "", err
	}
	return tm.accessToken, nil
}

func (tm *TokenManager) doRefresh() error {
	tr, err := postToken(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tm.refreshToken},
		"client_id":     {tm.clientID},
		"client_secret": {tm.clientSecret},
	})
	if err != nil {
		return err
	}
	tm.accessToken = tr.AccessToken
	if tr.RefreshToken != "" && tr.RefreshToken != tm.refreshToken {
		log.Printf("refresh token rotated — обновите .env: YANDEX_REFRESH_TOKEN=%s", tr.RefreshToken)
		tm.refreshToken = tr.RefreshToken
	}
	tm.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	log.Printf("токен обновлён, действует до %s", tm.expiresAt.Format("2006-01-02 15:04:05"))
	return nil
}

func (tm *TokenManager) keepAlive() {
	for {
		tm.mu.RLock()
		wake := tm.expiresAt.Add(-5 * time.Minute)
		tm.mu.RUnlock()
		if d := time.Until(wake); d > 0 {
			time.Sleep(d)
		}
		if _, err := tm.refresh(); err != nil {
			log.Printf("keepalive: не удалось обновить токен: %v — повтор через 30s", err)
			time.Sleep(30 * time.Second)
		}
	}
}

func (tm *TokenManager) AuthURL(redirectURI, state string) string {
	return oauthAuthURL + "?" + url.Values{
		"response_type": {"code"},
		"client_id":     {tm.clientID},
		"redirect_uri":  {redirectURI},
		"scope":         {"cloud_api:disk.write"},
		"state":         {state},
	}.Encode()
}

func (tm *TokenManager) ExchangeCode(code, redirectURI string) error {
	tr, err := postToken(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {tm.clientID},
		"client_secret": {tm.clientSecret},
		"redirect_uri":  {redirectURI},
	})
	if err != nil {
		return err
	}
	tm.mu.Lock()
	tm.accessToken = tr.AccessToken
	tm.refreshToken = tr.RefreshToken
	tm.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	tm.ready = true
	startKeepalive := !tm.keepAliveStarted
	tm.keepAliveStarted = true
	tm.mu.Unlock()

	log.Printf("=== АВТОРИЗАЦИЯ УСПЕШНА ===")
	log.Printf("Токен действует до %s", time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second).Format("2006-01-02 15:04:05"))
	log.Printf("Сохраните в .env для следующих запусков: YANDEX_REFRESH_TOKEN=%s", tr.RefreshToken)

	if startKeepalive {
		go tm.keepAlive()
	}
	return nil
}

func postToken(vals url.Values) (*tokenResp, error) {
	req, err := http.NewRequest(http.MethodPost, oauthTokenURL, strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var tr tokenResp
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("parse oauth response: %w", err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("%s: %s", tr.Error, tr.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth http %d: %s", resp.StatusCode, raw)
	}
	return &tr, nil
}

func randomState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthLoginHandler renders the OAuth authorization page. tm is nil in mock mode → 404.
func AuthLoginHandler(tm *TokenManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			http.Error(w, "mock mode — auth not needed", http.StatusNotFound)
			return
		}
		authURL := tm.AuthURL(VerificationRedirectURI, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, loginHTML, authURL)
	}
}

// AuthSubmitHandler exchanges the authorization code. tm is nil in mock mode → 404.
func AuthSubmitHandler(tm *TokenManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
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
		if err := tm.ExchangeCode(code, VerificationRedirectURI); err != nil {
			log.Printf("ExchangeCode: %v", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, errorHTML, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(successHTML))
	}
}

const loginHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Авторизация</title>
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
</div></body></html>`

const errorHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Ошибка</title>
<style>body{font-family:sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fdf8f2}
.box{padding:40px;background:#fff;border-radius:16px;box-shadow:0 4px 24px rgba(0,0,0,.08);text-align:center;max-width:420px}
h2{color:#b84a40}a{color:#c8896a}</style></head>
<body><div class="box"><h2>Ошибка авторизации</h2><p>%s</p><p><a href="/nikita/dasha/14062026/wedding/auth/love/login">← Попробовать снова</a></p></div></body></html>`

const successHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Готово</title>
<style>body{font-family:sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fdf8f2}
.box{padding:40px;background:#fff;border-radius:16px;box-shadow:0 4px 24px rgba(0,0,0,.08);text-align:center;max-width:420px}
h2{color:#3a2118}p{color:#9c7565}a{color:#c8896a}</style></head>
<body><div class="box"><h2>✓ Авторизация успешна</h2>
<p>Приложение готово к работе.<br>Refresh token сохранён в логах сервера.</p>
<p><a href="/">Открыть сайт →</a></p></div></body></html>`
