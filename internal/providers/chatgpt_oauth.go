package providers

// OAuth+PKCE flow for "Sign in with ChatGPT" using the public Codex CLI client.
// Mirrors what opencode-openai-codex-auth and the official Codex CLI do:
// authorize on auth.openai.com, exchange code for tokens, then call
// chatgpt.com/backend-api/codex/responses with the resulting bearer token.
//
// ── DISCLAIMER ──────────────────────────────────────────────────────────────
// The chatgptClientID below is OpenAI's public OAuth client_id for their
// Codex CLI. It is NOT registered to this project and its reuse here is NOT
// endorsed by OpenAI; depending on interpretation it may violate OpenAI's
// Terms of Service. OpenAI can rotate or revoke this client_id at any
// moment, which would silently break the chatgpt-codex provider.
//
// Use at your own risk. Users who want a supported integration should use
// the `openai` provider with their own API key.
// ────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	chatgptClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatgptAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	chatgptTokenURL     = "https://auth.openai.com/oauth/token"
	chatgptRedirectURI  = "http://localhost:1455/auth/callback"
	chatgptScope        = "openid profile email offline_access"
	chatgptOriginator   = "codex_cli_rs"
	chatgptAPIBase      = "https://chatgpt.com/backend-api/codex"
)

// ChatGPTTokens is the JSON blob persisted in the keyring under api_key_ref.
type ChatGPTTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccountID    string    `json:"account_id"`
}

// PKCEPair is a PKCE verifier+challenge pair.
type PKCEPair struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE generates a fresh PKCE verifier and S256 challenge.
func GeneratePKCE() (PKCEPair, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCEPair{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return PKCEPair{Verifier: verifier, Challenge: challenge}, nil
}

// RandomState returns a cryptographically random hex string for OAuth state.
func RandomState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf), nil
}

// BuildChatGPTAuthorizeURL constructs the authorization URL the user opens in the browser.
func BuildChatGPTAuthorizeURL(challenge, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", chatgptClientID)
	q.Set("redirect_uri", chatgptRedirectURI)
	q.Set("scope", chatgptScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", chatgptOriginator)
	return chatgptAuthorizeURL + "?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func postForm(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	if tr.AccessToken == "" || tr.RefreshToken == "" || tr.ExpiresIn == 0 {
		return nil, fmt.Errorf("token response missing fields")
	}
	return &tr, nil
}

// ExchangeChatGPTCode exchanges an authorization code for tokens.
func ExchangeChatGPTCode(ctx context.Context, code, verifier string) (ChatGPTTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", chatgptClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", chatgptRedirectURI)

	tr, err := postForm(ctx, form)
	if err != nil {
		return ChatGPTTokens{}, err
	}
	accountID, _ := parseAccountIDFromJWT(tr.AccessToken)
	return ChatGPTTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		AccountID:    accountID,
	}, nil
}

// RefreshChatGPTTokens uses a refresh token to get a fresh access token.
func RefreshChatGPTTokens(ctx context.Context, refreshToken string) (ChatGPTTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", chatgptClientID)
	form.Set("refresh_token", refreshToken)

	tr, err := postForm(ctx, form)
	if err != nil {
		return ChatGPTTokens{}, err
	}
	accountID, _ := parseAccountIDFromJWT(tr.AccessToken)
	return ChatGPTTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		AccountID:    accountID,
	}, nil
}

// parseAccountIDFromJWT extracts the chatgpt_account_id claim from the access token.
// The claim path is `https://api.openai.com/auth.chatgpt_account_id`.
func parseAccountIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// some tokens use standard base64 with padding
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", err
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if id, ok := auth["chatgpt_account_id"].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("chatgpt_account_id not found in token")
}
