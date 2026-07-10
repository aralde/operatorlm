package server

// POST /v1/audio/speech/realtime — simulated realtime voice: the request is a
// chat completion, the response is one SSE stream carrying both the text
// deltas (as the model generates them) and the synthesized audio (as each
// completed sentence comes back from the Piper sidecar). The client starts
// hearing the answer roughly one sentence after the model starts writing it,
// instead of after the full completion + full synthesis.
//
// Pipeline:
//
//	dispatch(chat, stream=true) ──▶ sseTextCollector ──▶ sentenceSplitter
//	                                      │ text deltas          │ sentences
//	                                      ▼                      ▼
//	                              response.text.delta   piper (response_format=pcm)
//	                                      events                 │ PCM chunks
//	                                                             ▼
//	                                                    speech.audio.delta events
//
// The chat leg reuses s.dispatch, so any routed model works (local/ or a
// cloud provider); only the synthesis leg requires the local Piper sidecar.
//
// Events (mirroring the shape of OpenAI's speech SSE where one exists):
//
//	speech.audio.start  {sample_rate}          — once, before the first audio
//	response.text.delta {delta}                — model text as it streams
//	speech.audio.delta  {audio}                — base64 16-bit LE mono PCM
//	error               {message}
//	speech.audio.done   {}                     — terminal event
import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unicode"

	"github.com/aralde/operatorlm/internal/providers"
)

type realtimeSpeechRequest struct {
	Model string `json:"model"`
	Voice string `json:"voice"`
	// Input is the one-shot prompt form; Messages (full chat history) wins
	// when both are present.
	Input        string            `json:"input"`
	Instructions string            `json:"instructions"`
	Messages     []json.RawMessage `json:"messages"`
	Temperature  *float64          `json:"temperature,omitempty"`
}

func (s *Server) handleRealtimeSpeech(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lm := s.cfg.GetLocalModels()
	eng := s.reg.LocalEngine()
	if !lm.Enabled || !lm.PiperEnabled || eng == nil {
		http.Error(w, "piper TTS is disabled: enable it in the Local models tab", http.StatusServiceUnavailable)
		return
	}

	limit := s.cfg.MaxRequestBodyBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var req realtimeSpeechRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, "model is required (any routed chat model, e.g. local/<id>)", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 && strings.TrimSpace(req.Input) == "" {
		http.Error(w, "provide input or messages", http.StatusBadRequest)
		return
	}

	chatBody, err := buildRealtimeChatBody(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Both the LLM goroutine (text deltas) and the TTS goroutine (audio
	// deltas) write to the same response; the mutex keeps events whole.
	var wmu sync.Mutex
	emit := func(event string, fields map[string]any) {
		if fields == nil {
			fields = map[string]any{}
		}
		fields["type"] = event
		b, err := json.Marshal(fields)
		if err != nil {
			return
		}
		wmu.Lock()
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		wmu.Unlock()
	}

	// One piper process serves the whole session: the worker feeds it
	// sentences as they complete while the model keeps generating, so the
	// two stages overlap and the .onnx voice model is loaded exactly once
	// (a per-sentence spawn would pay that load again every sentence).
	voicePath, sampleRate, verr := eng.PiperVoiceInfo(req.Voice)
	if verr != nil {
		emit("error", map[string]any{"message": verr.Error()})
	} else {
		emit("speech.audio.start", map[string]any{"sample_rate": sampleRate})
	}
	sentences := make(chan string, 16)
	var ttsDone sync.WaitGroup
	ttsDone.Add(1)
	go func() {
		defer ttsDone.Done()
		if verr != nil {
			for range sentences {
			} // drain so the splitter never blocks; text still streams
			return
		}
		err := eng.PiperSynthesizeStream(r.Context(), voicePath, sentences, func(pcm []byte) {
			emit("speech.audio.delta", map[string]any{
				"audio": base64.StdEncoding.EncodeToString(pcm),
			})
		})
		if err != nil && r.Context().Err() == nil {
			emit("error", map[string]any{"message": "tts: " + err.Error()})
			for range sentences {
			} // keep draining after a mid-session failure
		}
	}()

	splitter := &sentenceSplitter{emit: func(sentence string) {
		select {
		case sentences <- sentence:
		case <-r.Context().Done():
		}
	}}
	collector := &sseTextCollector{onDelta: func(delta string) {
		emit("response.text.delta", map[string]any{"delta": delta})
		splitter.Feed(delta)
	}}

	s.dispatch(collector, r, providers.KindChat, chatBody, req.Model, true)

	splitter.Close()
	close(sentences)
	ttsDone.Wait()

	if collector.status >= 400 {
		msg := strings.TrimSpace(collector.errBody.String())
		if msg == "" {
			msg = fmt.Sprintf("chat upstream returned status %d", collector.status)
		}
		emit("error", map[string]any{"message": msg, "status": collector.status})
	}
	emit("speech.audio.done", nil)
}

// buildRealtimeChatBody assembles the /v1/chat/completions payload for the
// text leg: messages win over input, instructions become the system message.
func buildRealtimeChatBody(req realtimeSpeechRequest) ([]byte, error) {
	msgs := make([]any, 0, len(req.Messages)+2)
	if req.Instructions != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.Instructions})
	}
	if len(req.Messages) > 0 {
		for _, m := range req.Messages {
			msgs = append(msgs, m)
		}
	} else {
		msgs = append(msgs, map[string]any{"role": "user", "content": req.Input})
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	return json.Marshal(body)
}

// sseTextCollector is the http.ResponseWriter handed to dispatch for the chat
// leg. It parses the OpenAI SSE stream and forwards each content delta to
// onDelta instead of writing anything to the real client. Non-2xx responses
// are captured in errBody for a terminal error event.
type sseTextCollector struct {
	onDelta func(string)
	status  int
	header  http.Header
	errBody bytes.Buffer
	partial bytes.Buffer
}

func (c *sseTextCollector) Header() http.Header {
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}

func (c *sseTextCollector) WriteHeader(status int) { c.status = status }

func (c *sseTextCollector) Flush() {}

func (c *sseTextCollector) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.status >= 400 {
		if c.errBody.Len() < 8192 {
			c.errBody.Write(p)
		}
		return len(p), nil
	}
	c.partial.Write(p)
	for {
		line, err := c.partial.ReadString('\n')
		if err != nil {
			// Incomplete line: keep it for the next Write.
			c.partial.Reset()
			c.partial.WriteString(line)
			break
		}
		c.consumeLine(strings.TrimRight(line, "\r\n"))
	}
	return len(p), nil
}

func (c *sseTextCollector) consumeLine(line string) {
	const prefix = "data: "
	if !strings.HasPrefix(line, prefix) {
		return
	}
	payload := strings.TrimSpace(line[len(prefix):])
	if payload == "" || payload == "[DONE]" {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != "" && c.onDelta != nil {
			c.onDelta(ch.Delta.Content)
		}
	}
}

// sentenceSplitter cuts a token stream into sentences for synthesis. A
// sentence ends at .!?…\n — but terminal punctuation only counts once the
// *next* rune is whitespace, so decimals ("3.14") and version numbers stay
// whole. Overlong runs are force-flushed so a paragraph without punctuation
// still produces audio.
type sentenceSplitter struct {
	emit       func(string)
	buf        strings.Builder
	pendingEnd bool
}

const (
	splitterMinRunes = 2    // ignore "empty" sentences like a stray period
	splitterMaxBytes = 400  // force a cut on punctuation-free walls of text
)

func (sp *sentenceSplitter) Feed(delta string) {
	for _, r := range delta {
		if sp.pendingEnd && unicode.IsSpace(r) {
			sp.flush()
			if r == '\n' {
				continue // don't start the next sentence with a newline
			}
		}
		sp.pendingEnd = false
		sp.buf.WriteRune(r)
		switch r {
		case '.', '!', '?', '…':
			sp.pendingEnd = true
		case '\n':
			sp.flush()
		}
		if sp.buf.Len() >= splitterMaxBytes {
			sp.flush()
		}
	}
}

// Close flushes whatever remains when the model stops generating.
func (sp *sentenceSplitter) Close() { sp.flush() }

func (sp *sentenceSplitter) flush() {
	sp.pendingEnd = false
	s := strings.TrimSpace(sp.buf.String())
	sp.buf.Reset()
	if len([]rune(s)) < splitterMinRunes {
		return
	}
	if sp.emit != nil {
		sp.emit(s)
	}
}
