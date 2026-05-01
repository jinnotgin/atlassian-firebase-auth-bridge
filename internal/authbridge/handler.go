package authbridge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

var firebaseAuth *auth.Client

type Config struct {
	AtlassianClientID     string
	AtlassianClientSecret string
	AtlassianRedirectURI  string
	FrontendBaseURL       string
	LoginCodeSecret       string
	ServiceAccountPath    string
}

type AtlassianTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType     string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type AtlassianMe struct {
	AccountID    string `json:"account_id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Picture     string `json:"picture"`
	AccountType string `json:"account_type"`
}

type LoginCodePayload struct {
	FirebaseCustomToken string `json:"firebaseCustomToken"`
	UID                 string `json:"uid"`
	Email               string `json:"email"`
	AtlassianAccountID  string `json:"atlassianAccountId"`
	Exp                 int64  `json:"exp"`
	Nonce               string `json:"nonce"`
}

type ExchangeRequest struct {
	LoginCode string `json:"loginCode"`
}

type ExchangeResponse struct {
	FirebaseCustomToken string `json:"firebaseCustomToken"`
	UID                 string `json:"uid"`
	Email               string `json:"email"`
}

func Handler(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfig()
	if err != nil {
		http.Error(w, "server configuration error", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	if firebaseAuth == nil {
		client, err := initFirebaseAuth(ctx, cfg.ServiceAccountPath)
		if err != nil {
			http.Error(w, "firebase initialization error", http.StatusInternalServerError)
			return
		}
		firebaseAuth = client
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})

	case r.Method == http.MethodGet && r.URL.Path == "/auth/atlassian/start":
		handleStart(w, r, cfg)

	case r.Method == http.MethodGet && r.URL.Path == "/auth/atlassian/callback":
		handleCallback(w, r, cfg, firebaseAuth)

	case r.Method == http.MethodPost && r.URL.Path == "/auth/session/exchange":
		handleExchange(w, r, cfg)

	default:
		http.NotFound(w, r)
	}
}

func loadConfig() (Config, error) {
	cfg := Config{
		AtlassianClientID:     os.Getenv("ATLASSIAN_CLIENT_ID"),
		AtlassianClientSecret: os.Getenv("ATLASSIAN_CLIENT_SECRET"),
		AtlassianRedirectURI:  os.Getenv("ATLASSIAN_REDIRECT_URI"),
		FrontendBaseURL:       strings.TrimRight(os.Getenv("FRONTEND_BASE_URL"), "/"),
		LoginCodeSecret:       os.Getenv("LOGIN_CODE_SECRET"),
		ServiceAccountPath:    os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
	}

	if cfg.ServiceAccountPath == "" {
		cfg.ServiceAccountPath = "/secrets/firebase-service-account.json"
	}

	if cfg.AtlassianClientID == "" ||
		cfg.AtlassianClientSecret == "" ||
		cfg.AtlassianRedirectURI == "" ||
		cfg.FrontendBaseURL == "" ||
		cfg.LoginCodeSecret == "" ||
		cfg.ServiceAccountPath == "" {
		return Config{}, errors.New("missing required configuration")
	}

	return cfg, nil
}

func initFirebaseAuth(ctx context.Context, serviceAccountPath string) (*auth.Client, error) {
	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsFile(serviceAccountPath))
	if err != nil {
		return nil, err
	}

	return app.Auth(ctx)
}

func handleStart(w http.ResponseWriter, r *http.Request, cfg Config) {
	statePayload := map[string]any{
		"nonce": randomString(32),
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
	}

	stateBytes, _ := json.Marshal(statePayload)
	state := signBlob(stateBytes, cfg.LoginCodeSecret)

	authURL, _ := url.Parse("https://auth.atlassian.com/authorize")
	q := authURL.Query()
	q.Set("audience", "api.atlassian.com")
	q.Set("client_id", cfg.AtlassianClientID)
	q.Set("scope", "read:me")
	q.Set("redirect_uri", cfg.AtlassianRedirectURI)
	q.Set("state", state)
	q.Set("response_type", "code")
	q.Set("prompt", "consent")
	authURL.RawQuery = q.Encode()

	http.Redirect(w, r, authURL.String(), http.StatusFound)
}

func handleCallback(w http.ResponseWriter, r *http.Request, cfg Config, authClient *auth.Client) {
	ctx := r.Context()

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	atlassianErr := r.URL.Query().Get("error")

	if atlassianErr != "" {
		redirectError(w, r, cfg, "atlassian_denied", "Atlassian sign-in was cancelled or denied.")
		return
	}

	if code == "" || state == "" {
		redirectError(w, r, cfg, "missing_code", "Missing Atlassian authorization code.")
		return
	}

	if err := verifyState(state, cfg.LoginCodeSecret); err != nil {
		redirectError(w, r, cfg, "invalid_state", "Invalid or expired login state.")
		return
	}

	tokenResp, err := exchangeCodeForToken(ctx, cfg, code)
	if err != nil {
		redirectError(w, r, cfg, "token_exchange_failed", "Could not complete Atlassian sign-in.")
		return
	}

	me, err := fetchAtlassianMe(ctx, tokenResp.AccessToken)
	if err != nil {
		redirectError(w, r, cfg, "profile_failed", "Could not read your Atlassian profile.")
		return
	}

	if strings.TrimSpace(me.AccountID) == "" {
		redirectError(w, r, cfg, "missing_account_id", "Atlassian did not return an account ID.")
		return
	}

	if strings.TrimSpace(me.Email) == "" {
		redirectError(
			w,
			r,
			cfg,
			"missing_email",
			"We cannot sign you in because Atlassian did not provide your email address. Please make your Atlassian email visible or contact support.",
		)
		return
	}

	uid := "atlassian:" + me.AccountID

	claims := map[string]interface{}{
		"provider":           "atlassian",
		"atlassianAccountId": me.AccountID,
		"email":              me.Email,
	}

	customToken, err := authClient.CustomTokenWithClaims(ctx, uid, claims)
	if err != nil {
		redirectError(w, r, cfg, "firebase_token_failed", "Could not create Firebase login token.")
		return
	}

	loginPayload := LoginCodePayload{
		FirebaseCustomToken: customToken,
		UID:                 uid,
		Email:               me.Email,
		AtlassianAccountID:  me.AccountID,
		Exp:                 time.Now().Add(2 * time.Minute).Unix(),
		Nonce:               randomString(32),
	}

	payloadBytes, _ := json.Marshal(loginPayload)
	loginCode := signBlob(payloadBytes, cfg.LoginCodeSecret)

	callbackURL := cfg.FrontendBaseURL + "/auth/callback?login_code=" + url.QueryEscape(loginCode)
	http.Redirect(w, r, callbackURL, http.StatusFound)
}

func exchangeCodeForToken(ctx context.Context, cfg Config, code string) (AtlassianTokenResponse, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     cfg.AtlassianClientID,
		"client_secret": cfg.AtlassianClientSecret,
		"code":          code,
		"redirect_uri":  cfg.AtlassianRedirectURI,
	}

	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://auth.atlassian.com/oauth/token",
		strings.NewReader(string(bodyBytes)),
	)
	if err != nil {
		return AtlassianTokenResponse{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AtlassianTokenResponse{}, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AtlassianTokenResponse{}, fmt.Errorf("token exchange failed: %s", string(respBytes))
	}

	var tokenResp AtlassianTokenResponse
	if err := json.Unmarshal(respBytes, &tokenResp); err != nil {
		return AtlassianTokenResponse{}, err
	}

	if tokenResp.AccessToken == "" {
		return AtlassianTokenResponse{}, errors.New("missing access token")
	}

	return tokenResp, nil
}

func fetchAtlassianMe(ctx context.Context, accessToken string) (AtlassianMe, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://api.atlassian.com/me",
		nil,
	)
	if err != nil {
		return AtlassianMe{}, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AtlassianMe{}, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AtlassianMe{}, fmt.Errorf("me failed: %s", string(respBytes))
	}

	var me AtlassianMe
	if err := json.Unmarshal(respBytes, &me); err != nil {
		return AtlassianMe{}, err
	}

	return me, nil
}

func handleExchange(w http.ResponseWriter, r *http.Request, cfg Config) {
	addCORS(w, cfg)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var req ExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	payloadBytes, err := verifySignedBlob(req.LoginCode, cfg.LoginCodeSecret)
	if err != nil {
		http.Error(w, "invalid or expired login code", http.StatusUnauthorized)
		return
	}

	var payload LoginCodePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		http.Error(w, "invalid login payload", http.StatusUnauthorized)
		return
	}

	if time.Now().Unix() > payload.Exp {
		http.Error(w, "expired login code", http.StatusUnauthorized)
		return
	}

	if payload.FirebaseCustomToken == "" || payload.UID == "" || payload.Email == "" {
		http.Error(w, "invalid login payload", http.StatusUnauthorized)
		return
	}

	writeJSON(w, http.StatusOK, ExchangeResponse{
		FirebaseCustomToken: payload.FirebaseCustomToken,
		UID:                 payload.UID,
		Email:               payload.Email,
	})
}

func verifyState(state string, secret string) error {
	payloadBytes, err := verifySignedBlob(state, secret)
	if err != nil {
		return err
	}

	var payload struct {
		Nonce string `json:"nonce"`
		Exp   int64  `json:"exp"`
	}

	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return err
	}

	if payload.Nonce == "" {
		return errors.New("missing nonce")
	}

	if time.Now().Unix() > payload.Exp {
		return errors.New("state expired")
	}

	return nil
}

func signBlob(payload []byte, secret string) string {
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadB64))
	sig := mac.Sum(nil)

	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return payloadB64 + "." + sigB64
}

func verifySignedBlob(token string, secret string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("bad token format")
	}

	payloadB64 := parts[0]
	sigB64 := parts[1]

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadB64))
	expectedSig := mac.Sum(nil)

	actualSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, err
	}

	if !hmac.Equal(actualSig, expectedSig) {
		return nil, errors.New("bad signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func redirectError(w http.ResponseWriter, r *http.Request, cfg Config, code string, message string) {
	u := cfg.FrontendBaseURL + "/login?error=" + url.QueryEscape(code) + "&message=" + url.QueryEscape(message)
	http.Redirect(w, r, u, http.StatusFound)
}

func randomString(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func addCORS(w http.ResponseWriter, cfg Config) {
	w.Header().Set("Access-Control-Allow-Origin", cfg.FrontendBaseURL)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Vary", "Origin")
}

func escape(s string) string {
	return html.EscapeString(s)
}