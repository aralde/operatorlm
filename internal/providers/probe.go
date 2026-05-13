package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ProbeOptions carries provider-type-specific knobs for ProbeModels.
type ProbeOptions struct {
	// ApiVersion is required for azure-openai.
	ApiVersion string
}

// ProbeModels fetches the list of available models from the upstream
// using the given credentials. It serves as both API-key validation and
// model discovery. Returns sorted, deduped model IDs.
func ProbeModels(ctx context.Context, providerType, baseURL, apiKey string, opts ProbeOptions) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	switch providerType {
	case "openai", "openrouter", "groq", "opencode-zen", "nvidia-nim", "mistral", "bedrock", "custom":
		return probeOpenAILike(ctx, baseURL, apiKey)
	case "azure-openai":
		return probeAzureOpenAI(ctx, baseURL, apiKey, opts.ApiVersion)
	case "gemini":
		return probeGemini(ctx, baseURL, apiKey)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

func probeOpenAILike(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return dedupeSorted(out), nil
}

func probeAzureOpenAI(ctx context.Context, baseURL, apiKey, apiVersion string) ([]string, error) {
	if apiVersion == "" {
		apiVersion = defaultAzureApiVersion
	}
	// Lists deployments (what users care about — model is bound to deployment).
	url := strings.TrimRight(baseURL, "/") + "/openai/deployments?api-version=" + apiVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return dedupeSorted(out), nil
}

func probeGemini(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	url := strings.TrimRight(baseURL, "/") + "/models?key=" + apiKey
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var parsed struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if !supportsGenerate(m.SupportedGenerationMethods) {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		if id != "" {
			out = append(out, id)
		}
	}
	return dedupeSorted(out), nil
}

func supportsGenerate(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" {
			return true
		}
	}
	return len(methods) == 0
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
