package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/router"
)

type antigravity struct {
	cfg      config.Provider
	port     int
	listener net.Listener
}

func newAntigravity(cfg config.Provider) Provider {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("[antigravity] failed to start loopback listener: %v", err)
		return nil
	}
	port := listener.Addr().(*net.TCPAddr).Port
	a := &antigravity{
		cfg:      cfg,
		port:     port,
		listener: listener,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", a.handleChatCompletions)

	go func() {
		if err := http.Serve(listener, mux); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("[antigravity] loopback server error: %v", err)
		}
	}()

	log.Printf("[antigravity] started loopback server on 127.0.0.1:%d", port)
	return a
}

func (a *antigravity) Name() string     { return a.cfg.Name }
func (a *antigravity) Type() string     { return a.cfg.Type }
func (a *antigravity) Prefix() string   { return a.cfg.Prefix }
func (a *antigravity) Models() []string { return a.cfg.Models }

func (a *antigravity) BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, _ bool) (*http.Request, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/chat/completions", a.port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (a *antigravity) WriteResponse(w http.ResponseWriter, resp *http.Response, _ Kind, _ string, _ bool) error {
	// Replay loopback headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type newConversationResponse struct {
	Response struct {
		NewConversation struct {
			ConversationID string `json:"conversationId"`
		} `json:"newConversation"`
	} `json:"response"`
}

type step struct {
	StepIndex int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Content   string `json:"content"`
}

func (a *antigravity) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var prompt string
	if len(req.Messages) > 0 {
		prompt = req.Messages[len(req.Messages)-1].Content
	}

	// Resolve the model parameter to pass to agentapi
	modelArg := "flash"
	parts := strings.Split(req.Model, "/")
	if len(parts) > 1 {
		switch strings.ToLower(parts[1]) {
		case "pro":
			modelArg = "pro"
		case "flash_lite", "flash-lite", "lite":
			modelArg = "flash_lite"
		case "flash":
			modelArg = "flash"
		}
	}

	agentAPIPath, err := getAgentAPIPath()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to resolve agentapi path: %v", err), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	args := []string{"new-conversation", "--model=" + modelArg, prompt}
	cmd := prepareCommand(ctx, agentAPIPath, args...)
	addr, token, projectID := getActiveLSInfo()
	if a.cfg.ProjectID != "" {
		projectID = a.cfg.ProjectID
	}
	cmd.Env = append(os.Environ(),
		"ANTIGRAVITY_LS_ADDRESS="+addr,
		"ANTIGRAVITY_CSRF_TOKEN="+token,
		"ANTIGRAVITY_PROJECT_ID="+projectID,
	)

	// Run all proxy conversations in a dedicated workspace folder
	// to avoid polluting the user's active codebase/project history.
	if home, err := os.UserHomeDir(); err == nil {
		proxyWorkspace := filepath.Join(home, ".operatorlm", "proxy_workspace")
		_ = os.MkdirAll(proxyWorkspace, 0755)
		cmd.Dir = proxyWorkspace
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := stdout.String()
		hint := ""
		if strings.Contains(errStr, "desc = file does not exist") {
			hint = "\n[Hint: The Antigravity daemon rejected the project ID because the workspace is not currently open/active in your editor or Antigravity desktop app. Please open the workspace folder in your editor to activate it.]"
		} else if strings.Contains(errStr, "project_id is required") {
			hint = "\n[Hint: The Antigravity daemon requires a project ID. Please configure a Project ID for the antigravity provider in the OperatorLM Admin UI under Providers.]"
		}
		http.Error(w, fmt.Sprintf("agentapi failed: %v%s\nDetails: path: %s | ls_addr: %s | csrf_len: %d | dir: %s\nstdout: %q\nstderr: %q",
			err, hint, agentAPIPath, addr, len(token), cmd.Dir, errStr, stderr.String()), http.StatusInternalServerError)
		return
	}

	var newConv newConversationResponse
	if err := json.Unmarshal(stdout.Bytes(), &newConv); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse agentapi output: %v | stdout: %s", err, stdout.String()), http.StatusInternalServerError)
		return
	}

	convID := newConv.Response.NewConversation.ConversationID
	if convID == "" {
		http.Error(w, "empty conversation ID returned from agentapi", http.StatusInternalServerError)
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get user home dir: %v", err), http.StatusInternalServerError)
		return
	}

	transcriptPath := filepath.Join(homeDir, ".gemini", "antigravity", "brain", convID, ".system_generated", "logs", "transcript.jsonl")

	createdTime := time.Now().Unix()
	reqID := fmt.Sprintf("chatcmpl-agy-%d", createdTime)
	modelName := req.Model

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Transfer-Encoding", "chunked")

		flusher, _ := w.(http.Flusher)

		var sentOffset int
		var completed bool

		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.After(30 * time.Second)

		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout:
				log.Printf("[antigravity] timeout waiting for conversation %s", convID)
				return
			case <-ticker.C:
				data, err := os.ReadFile(transcriptPath)
				if err != nil {
					continue
				}

				dec := json.NewDecoder(bytes.NewReader(data))
				var lastContent string
				var isDone bool

				for {
					var s step
					if err := dec.Decode(&s); err != nil {
						break
					}
					if s.Source == "MODEL" && s.Type == "PLANNER_RESPONSE" {
						lastContent = s.Content
						if s.Status == "DONE" {
							isDone = true
							break
						}
					}
				}

				if len(lastContent) > sentOffset {
					deltaText := lastContent[sentOffset:]
					sentOffset = len(lastContent)

					chunk := struct {
						ID      string `json:"id"`
						Object  string `json:"object"`
						Created int64  `json:"created"`
						Model   string `json:"model"`
						Choices []struct {
							Index int `json:"index"`
							Delta struct {
								Content string `json:"content"`
							} `json:"delta"`
							FinishReason *string `json:"finish_reason"`
						} `json:"choices"`
					}{
						ID:      reqID,
						Object:  "chat.completion.chunk",
						Created: createdTime,
						Model:   modelName,
					}
					chunk.Choices = append(chunk.Choices, struct {
						Index int `json:"index"`
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						FinishReason *string `json:"finish_reason"`
					}{
						Index: 0,
						Delta: struct {
							Content string `json:"content"`
						}{
							Content: deltaText,
						},
					})

					chunkBytes, _ := json.Marshal(chunk)
					fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
					if flusher != nil {
						flusher.Flush()
					}
				}

				if isDone {
					completed = true
					break
				}
			}

			if completed {
				break
			}
		}

		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}

	} else {
		var modelContent string
		completed := false

		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.After(30 * time.Second)

		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout:
				http.Error(w, "timeout waiting for agent response", http.StatusGatewayTimeout)
				return
			case <-ticker.C:
				data, err := os.ReadFile(transcriptPath)
				if err != nil {
					continue
				}

				dec := json.NewDecoder(bytes.NewReader(data))
				for {
					var s step
					if err := dec.Decode(&s); err != nil {
						break
					}
					if s.Source == "MODEL" && s.Type == "PLANNER_RESPONSE" {
						modelContent = s.Content
						if s.Status == "DONE" {
							completed = true
							break
						}
					}
				}
			}

			if completed {
				break
			}
		}

		resp := struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}{
			ID:      reqID,
			Object:  "chat.completion",
			Created: createdTime,
			Model:   modelName,
		}

		resp.Choices = append(resp.Choices, struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			Index: 0,
			Message: struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{
				Role:    "assistant",
				Content: modelContent,
			},
			FinishReason: "stop",
		})

		w.Header().Set("Content-Type", "application/json")
		respBytes, _ := json.Marshal(resp)
		_, _ = w.Write(respBytes)
	}
}

func getAgentAPIPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// Try standard extensions/paths
	paths := []string{
		filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi.bat"),
		filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi.cmd"),
		filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Fallback to searching PATH if not found in the default location
	if p, err := exec.LookPath("agentapi"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("agentapi executable not found in default gemini folders or PATH")
}
