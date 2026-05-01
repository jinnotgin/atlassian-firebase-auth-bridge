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
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	clientsMu       sync.Mutex
	firebaseAuth    *auth.Client
	firestoreClient *firestore.Client
)

const (
	loginCodeTTL        = 2 * time.Minute
	oauthStateTTL       = 10 * time.Minute
	loginCodesCollName = "authLoginCodes"
)

type Config struct {
	AtlassianClientID     string
	AtlassianClientSecret string
	AtlassianRedirectURI  string
	FrontendBaseURL       string
	AllowedCORSOrigins    []string
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

type OAuthStatePayload struct {
	Nonce    string `json:"nonce"`
	Exp      int64  `json:"exp"`
	Redirect string `json:"redirect"`
}

type LoginCodeRecord struct {
	FirebaseCustomToken string    `firestore:"firebaseCustomToken"`
	UID                 string    `firestore:"uid"`
	Email               string    `firestore:"email"`
	AtlassianAccountID  string    `firestore:"atlassianAccountId"`
	Redirect            string    `firestore:"redirect"`
	ExpiresAt           time.Time `firestore:"expiresAt"`
	CreatedAt           time.Time `firestore:"createdAt"`
}

type ExchangeRequest struct {
	LoginCode string `json:"loginCode"`
}

type ExchangeResponse struct {
	FirebaseCustomToken string `json:"firebaseCustomToken"`
	UID                 string `json:"uid"`
	Email               string `json:"email"`
	Redirect            string `json:"redirect,omitempty"`
}

func Handler(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("config error: %v", err)
		http.Error(w, "server configuration error", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	authClient, fsClient, err := ensureClients(ctx, cfg)
	if err != nil {
		log.Printf("client initialization error: %v", err)
		http.Error(w, "server initialization error", http.StatusInternalServerError)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})

	case r.Method == http.MethodGet && r.URL.Path == "/auth/atlassian/start":
		handleStart(w, r, cfg)

	case r.Method == http.MethodGet && r.URL.Path == "/auth/atlassian/callback":
		handleCallback(w, r, cfg, authClient, fsClient)

	case (r.Method == http.MethodPost || r.Method == http.MethodOptions) &&
		r.URL.Path == "/auth/session/exchange":
		handleExchange(w, r, cfg, fsClient)

	default:
		http.NotFound(w, r)
	}
}

func loadConfig() (Config, error) {
	frontendBaseURL := strings.TrimRight(os.Getenv("FRONTEND_BASE_URL"), "/")

	cfg := Config{
		AtlassianClientID:     os.Getenv("ATLASSIAN_CLIENT_ID"),
		AtlassianClientSecret: os.Getenv("ATLASSIAN_CLIENT_SECRET"),
		AtlassianRedirectURI:  os.Getenv("ATLASSIAN_REDIRECT_URI"),
		FrontendBaseURL:       frontendBaseURL,
		AllowedCORSOrigins:    parseAllowedOrigins(frontendBaseURL, os.Getenv("ALLOWED_CORS_ORIGINS")),
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

func parseAllowedOrigins(frontendBaseURL string, raw string) []string {
	seen := map[string]bool{}
	var origins []string

	add := func(origin string) {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin == "" || seen[origin] {
			return
		}
		seen[origin] = true
		origins = append(origins, origin)
	}

	add(frontendBaseURL)

	for _, part := range strings.Split(raw, ",") {
		add(part)
	}

	return origins
}

func ensureClients(ctx context.Context, cfg Config) (*auth.Client, *firestore.Client, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	if firebaseAuth != nil && firestoreClient != nil {
		return firebaseAuth, firestoreClient, nil
	}

	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsFile(cfg.ServiceAccountPath))
	if err != nil {
		return nil, nil, fmt.Errorf("firebase app: %w", err)
	}

	authClient, err := app.Auth(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("firebase auth: %w", err)
	}

	fsClient, err := app.Firestore(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("firestore: %w", err)
	}

	firebaseAuth = authClient
	firestoreClient = fsClient

	return firebaseAuth, firestoreClient, nil
}

func handleStart(w http.ResponseWriter, r *http.Request, cfg Config) {
	redirectPath := safeRedirectPath(r.URL.Query().Get("redirect"))

	statePayload := OAuthStatePayload{
		Nonce:    randomString(32),
		Exp:      time.Now().Add(oauthStateTTL).Unix(),
		Redirect: redirectPath,
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

func handleCallback(
	w http.ResponseWriter,
	r *http.Request,
	cfg Config,
	authClient *auth.Client,
	fsClient *firestore.Client,
) {
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

	statePayload, err := verifyState(state, cfg.LoginCodeSecret)
	if err != nil {
		log.Printf("invalid oauth state: %v", err)
		redirectError(w, r, cfg, "invalid_state", "Invalid or expired login state.")
		return
	}

	redirectPath := safeRedirectPath(statePayload.Redirect)

	tokenResp, err := exchangeCodeForToken(ctx, cfg, code)
	if err != nil {
		log.Printf("atlassian token exchange failed: %v", err)
		redirectError(w, r, cfg, "token_exchange_failed", "Could not complete Atlassian sign-in.")
		return
	}

	me, err := fetchAtlassianMe(ctx, tokenResp.AccessToken)
	if err != nil {
		log.Printf("atlassian profile failed: %v", err)
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

	if err := upsertFirebaseUser(ctx, authClient, uid, me); err != nil {
		log.Printf("firebase user upsert failed: %v", err)
		redirectError(w, r, cfg, "firebase_user_failed", "Could not prepare your Firebase account.")
		return
	}

	claims := map[string]interface{}{
		"provider":           "atlassian",
		"atlassianAccountId": me.AccountID,
		"email":              me.Email,
	}

	customToken, err := authClient.CustomTokenWithClaims(ctx, uid, claims)
	if err != nil {
		log.Printf("firebase custom token failed: %v", err)
		redirectError(w, r, cfg, "firebase_token_failed", "Could not create Firebase login token.")
		return
	}

	loginCode := randomString(32)
	now := time.Now()

	record := LoginCodeRecord{
		FirebaseCustomToken: customToken,
		UID:                 uid,
		Email:               me.Email,
		AtlassianAccountID:  me.AccountID,
		Redirect:            redirectPath,
		ExpiresAt:           now.Add(loginCodeTTL),
		CreatedAt:           now,
	}

	if err := storeLoginCode(ctx, fsClient, loginCode, record); err != nil {
		log.Printf("store login code failed: %v", err)
		redirectError(w, r, cfg, "login_code_failed", "Could not create login session.")
		return
	}

	callbackURL := cfg.FrontendBaseURL +
		"/auth/callback?login_code=" + url.QueryEscape(loginCode) +
		"&redirect=" + url.QueryEscape(redirectPath)

	http.Redirect(w, r, callbackURL, http.StatusFound)
}

func upsertFirebaseUser(ctx context.Context, authClient *auth.Client, uid string, me AtlassianMe) error {
	update := (&auth.UserToUpdate{}).
		Email(me.Email)

	if strings.TrimSpace(me.Name) != "" {
		update = update.DisplayName(me.Name)
	}

	if strings.TrimSpace(me.Picture) != "" {
		update = update.PhotoURL(me.Picture)
	}

	_, err := authClient.UpdateUser(ctx, uid, update)
	if err == nil {
		return nil
	}

	if !auth.IsUserNotFound(err) {
		return err
	}

	create := (&auth.UserToCreate{}).
		UID(uid).
		Email(me.Email)

	if strings.TrimSpace(me.Name) != "" {
		create = create.DisplayName(me.Name)
	}

	if strings.TrimSpace(me.Picture) != "" {
		create = create.PhotoURL(me.Picture)
	}

	_, err = authClient.CreateUser(ctx, create)
	return err
}

func storeLoginCode(ctx context.Context, fsClient *firestore.Client, loginCode string, record LoginCodeRecord) error {
	_, err := fsClient.
		Collection(loginCodesCollName).
		Doc(loginCode).
		Set(ctx, record)

	return err
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

func handleExchange(w http.ResponseWriter, r *http.Request, cfg Config, fsClient *firestore.Client) {
	addCORS(w, r, cfg)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method_not_allowed",
		})
		return
	}

	var req ExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_json",
		})
		return
	}

	loginCode := strings.TrimSpace(req.LoginCode)
	if loginCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing_login_code",
		})
		return
	}

	record, err := consumeLoginCode(r.Context(), fsClient, loginCode)
	if err != nil {
		log.Printf("consume login code failed: %v", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "invalid_or_expired_login_code",
		})
		return
	}

	writeJSON(w, http.StatusOK, ExchangeResponse{
		FirebaseCustomToken: record.FirebaseCustomToken,
		UID:                 record.UID,
		Email:               record.Email,
		Redirect:            record.Redirect,
	})
}

func consumeLoginCode(ctx context.Context, fsClient *firestore.Client, loginCode string) (LoginCodeRecord, error) {
	docRef := fsClient.Collection(loginCodesCollName).Doc(loginCode)

	var record LoginCodeRecord

	err := fsClient.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(docRef)
		if err != nil {
			return err
		}

		if err := snap.DataTo(&record); err != nil {
			return err
		}

		if record.FirebaseCustomToken == "" || record.UID == "" || record.Email == "" {
			return errors.New("invalid login code record")
		}

		if time.Now().After(record.ExpiresAt) {
			// Delete expired code as cleanup.
			if err := tx.Delete(docRef); err != nil {
				return err
			}
			return errors.New("expired login code")
		}

		// Critical: delete inside the transaction so this code is one-time-use.
		if err := tx.Delete(docRef); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		if status.Code(err) == codes.NotFound {
			return LoginCodeRecord{}, errors.New("login code not found")
		}
		return LoginCodeRecord{}, err
	}

	return record, nil
}

func verifyState(state string, secret string) (OAuthStatePayload, error) {
	payloadBytes, err := verifySignedBlob(state, secret)
	if err != nil {
		return OAuthStatePayload{}, err
	}

	var payload OAuthStatePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return OAuthStatePayload{}, err
	}

	if payload.Nonce == "" {
		return OAuthStatePayload{}, errors.New("missing nonce")
	}

	if time.Now().Unix() > payload.Exp {
		return OAuthStatePayload{}, errors.New("state expired")
	}

	payload.Redirect = safeRedirectPath(payload.Redirect)

	return payload, nil
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
	u := cfg.FrontendBaseURL +
		"/login?error=" + url.QueryEscape(code) +
		"&message=" + url.QueryEscape(message)

	http.Redirect(w, r, u, http.StatusFound)
}

func safeRedirectPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "/overview"
	}

	if !strings.HasPrefix(raw, "/") {
		return "/overview"
	}

	if strings.HasPrefix(raw, "//") {
		return "/overview"
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "/overview"
	}

	if u.IsAbs() || u.Host != "" {
		return "/overview"
	}

	return raw
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

func addCORS(w http.ResponseWriter, r *http.Request, cfg Config) {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")

	for _, allowed := range cfg.AllowedCORSOrigins {
		if origin == allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			break
		}
	}

	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Vary", "Origin")
}