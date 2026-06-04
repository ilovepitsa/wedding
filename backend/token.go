package main

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
	// re-check after acquiring write lock
	if time.Now().Before(tm.expiresAt.Add(-2 * time.Minute)) {
		return tm.accessToken, nil
	}
	if err := tm.doRefresh(); err != nil {
		return "", err
	}
	return tm.accessToken, nil
}

// doRefresh must be called with mu write-locked (or before goroutines start).
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

// AuthURL builds the Yandex OAuth authorization URL for the given redirect URI and state.
func (tm *TokenManager) AuthURL(redirectURI, state string) string {
	return oauthAuthURL + "?" + url.Values{
		"response_type": {"code"},
		"client_id":     {tm.clientID},
		"redirect_uri":  {redirectURI},
		"scope":         {"cloud_api:disk.write"},
		"state":         {state},
	}.Encode()
}

// ExchangeCode exchanges an authorization code for tokens and marks the manager as ready.
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
