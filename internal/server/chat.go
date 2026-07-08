package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"github.com/aralde/operatorlm/internal/providers"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindChat, true)
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindImages, false)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindResponses, true)
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindEmbeddings, false)
}

func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindSpeech, false)
}

func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
	s.handleAudioMultipart(w, r, providers.KindTranscriptions)
}

func (s *Server) handleTranslations(w http.ResponseWriter, r *http.Request) {
	s.handleAudioMultipart(w, r, providers.KindTranslations)
}

func (s *Server) handleAudioMultipart(w http.ResponseWriter, r *http.Request, kind providers.Kind) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := s.cfg.MaxRequestBodyBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	model, err := extractMultipartModel(body, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.dispatch(w, r, kind, body, model, false)
}

func extractMultipartModel(body []byte, contentType string) (string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", fmt.Errorf("no boundary in content-type")
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if part.FormName() == "model" {
			val, err := io.ReadAll(part)
			if err != nil {
				return "", err
			}
			return string(bytes.TrimSpace(val)), nil
		}
	}
	return "", fmt.Errorf("model field not found in form data")
}

// handleAPI is the shared entry for /v1/chat/completions, /v1/images/generations,
// and /v1/responses. It enforces an upper bound on the request body, parses
// the meta envelope (model + stream flag), and hands off to dispatch.
// honorStream=false forces stream=false even if the client requested it (images).
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, kind providers.Kind, honorStream bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := s.cfg.MaxRequestBodyBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	model, stream, err := extractRequestMeta(body)
	if err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !honorStream {
		stream = false
	}
	s.dispatch(w, r, kind, body, model, stream)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := s.cfg.MaxRequestBodyBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	antBody, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	oaiBody, err := providers.TranslateAnthropicRequestToOpenAI(antBody)
	if err != nil {
		http.Error(w, "invalid anthropic json: "+err.Error(), http.StatusBadRequest)
		return
	}

	model, stream, err := extractRequestMeta(oaiBody)
	if err != nil {
		http.Error(w, "invalid translated json: "+err.Error(), http.StatusBadRequest)
		return
	}

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		translator := providers.NewAnthropicStreamTranslator(w)
		s.dispatch(translator, r, providers.KindChat, oaiBody, model, true)
	} else {
		recWriter := &responseRecorder{
			ResponseWriter: w,
			headers:        make(http.Header),
		}
		s.dispatch(recWriter, r, providers.KindChat, oaiBody, model, false)

		if recWriter.status >= 400 {
			for k, vv := range recWriter.headers {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(recWriter.status)
			_, _ = w.Write(recWriter.body.Bytes())
			return
		}

		antResp, err := providers.TranslateOpenAIResponseToAnthropic(recWriter.body.Bytes())
		if err != nil {
			http.Error(w, "failed to translate response: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(recWriter.status)
		_, _ = w.Write(antResp)
	}
}

func (s *Server) handleHealthGlobal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

type responseRecorder struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	headers http.Header
}

func (r *responseRecorder) Header() http.Header {
	return r.headers
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.body.Write(p)
}

func (r *responseRecorder) Flush() {
}
