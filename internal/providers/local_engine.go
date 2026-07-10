package providers

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aralde/operatorlm/internal/config"
)

// loadTimeout bounds how long we wait for llama-server to load a model and
// answer /health. Large quantized models (20B+) can take a while on first load.
const loadTimeout = 180 * time.Second

// LocalEngine manages a single llama-server child process, swapping the loaded
// model on demand. Because llama-server serves one model per process, switching
// models means stopping the current process and starting a new one. This mirrors
// how Ollama works internally, but keeps everything inside operatorlm.
type LocalEngine struct {
	dataMu sync.RWMutex
	cfg    config.LocalModelsConfig
	models []LocalModel
	byID   map[string]LocalModel

	procMu       sync.Mutex
	cmd          *exec.Cmd
	cancel       context.CancelFunc
	done         chan struct{}
	currentModel string
	baseURL      string

	// Whisper process management
	whisperCmd    *exec.Cmd
	whisperCancel context.CancelFunc
	whisperDone   chan struct{}
	currentWspMod string

	// Piper server
	piperSrv *http.Server
}

func NewLocalEngine(cfg config.LocalModelsConfig) *LocalEngine {
	return &LocalEngine{cfg: cfg.WithDefaults(), byID: map[string]LocalModel{}}
}

// Reconfigure swaps in new settings. If a process-affecting setting changed and
// a server is running, it is stopped so the next request restarts it cleanly.
func (e *LocalEngine) Reconfigure(cfg config.LocalModelsConfig) {
	cfg = cfg.WithDefaults()
	e.dataMu.Lock()
	old := e.cfg
	e.cfg = cfg
	e.dataMu.Unlock()
	if old.Port != cfg.Port ||
		old.LlamaServerPath != cfg.LlamaServerPath ||
		old.ContextSize != cfg.ContextSize ||
		old.NGPULayers != cfg.NGPULayers ||
		old.NoThinking != cfg.NoThinking ||
		strings.Join(old.ExtraArgs, "\n") != strings.Join(cfg.ExtraArgs, "\n") {
		e.StopChat()
	}

	e.manageWhisper(cfg)
	e.managePiper(cfg)
}

// Refresh rescans the configured models directory.
func (e *LocalEngine) Refresh() {
	e.dataMu.RLock()
	dir := e.cfg.ModelsDir
	e.dataMu.RUnlock()

	models, err := ScanLocalModels(dir)
	if err != nil {
		log.Printf("local engine: scan %q: %v", dir, err)
	}
	byID := make(map[string]LocalModel, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	e.dataMu.Lock()
	e.models = models
	e.byID = byID
	e.dataMu.Unlock()
}

func (e *LocalEngine) Models() []LocalModel {
	e.dataMu.RLock()
	defer e.dataMu.RUnlock()
	out := make([]LocalModel, len(e.models))
	copy(out, e.models)
	return out
}

func (e *LocalEngine) ModelIDs() []string {
	e.dataMu.RLock()
	defer e.dataMu.RUnlock()
	out := make([]string, len(e.models))
	for i, m := range e.models {
		out[i] = m.ID
	}
	return out
}

// Current returns the model currently loaded, if the server is alive.
func (e *LocalEngine) Current() (string, bool) {
	e.procMu.Lock()
	defer e.procMu.Unlock()
	if e.currentModel != "" && e.isAlive() {
		return e.currentModel, true
	}
	return "", false
}

// EnsureRunning guarantees a llama-server is serving modelID and returns its
// OpenAI-compatible base URL (".../v1"). It starts or swaps the process as
// needed; concurrent callers serialize on procMu.
func (e *LocalEngine) EnsureRunning(ctx context.Context, modelID string) (string, error) {
	e.dataMu.RLock()
	cfg := e.cfg
	m, ok := e.byID[modelID]
	e.dataMu.RUnlock()
	if !ok {
		return "", fmt.Errorf("local model %q not found under %q (run a rescan if you just added it)", modelID, cfg.ModelsDir)
	}

	e.procMu.Lock()
	defer e.procMu.Unlock()

	if e.currentModel == modelID && e.isAlive() {
		return e.baseURL, nil
	}
	e.stopLocked()

	// Per-model GPU offload: catalog models carry a recommended value so the
	// CPU-only pick stays on CPU (ngl 0) even when the engine's global default
	// is higher. Non-catalog models fall back to the engine setting.
	ngl := cfg.NGPULayers
	if cNGL, _, ok := CatalogSettingsFor(m.ID); ok {
		ngl = cNGL
	}
	err := e.startLocked(ctx, cfg, m, ngl)
	if err != nil && ngl > 0 {
		// Some models crash llama.cpp when the graph is split between CPU and
		// GPU (e.g. Gemma E4B's GGML_SCHED_MAX_SPLIT_INPUTS assert). Rather
		// than failing the request, retry once fully on CPU.
		log.Printf("local engine: %q failed with -ngl %d (%v); retrying on CPU (-ngl 0)", modelID, ngl, err)
		err = e.startLocked(ctx, cfg, m, 0)
	}
	if err != nil {
		return "", err
	}
	return e.baseURL, nil
}

// Stop terminates any running server.
func (e *LocalEngine) Stop() {
	e.procMu.Lock()
	defer e.procMu.Unlock()
	e.stopLocked()
	e.stopWhisperLocked()
	e.stopPiperLocked()
}

// StopChat terminates the llama-server process but leaves the whisper and
// piper sidecars alone (they are gated by their own enabled flags).
func (e *LocalEngine) StopChat() {
	e.procMu.Lock()
	defer e.procMu.Unlock()
	e.stopLocked()
}

// --- internals (caller must hold procMu) ---

func (e *LocalEngine) isAlive() bool {
	if e.cmd == nil || e.done == nil {
		return false
	}
	select {
	case <-e.done:
		return false
	default:
		return true
	}
}

func (e *LocalEngine) startLocked(ctx context.Context, cfg config.LocalModelsConfig, m LocalModel, ngl int) error {
	bin := cfg.LlamaServerPath
	if !fileExists(bin) {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("llama-server not found (%q): install llama.cpp or set local_models.llama_server_path", bin)
		}
	}

	args := []string{
		"-m", m.Path,
		// --alias controls the model name llama-server echoes back in REST
		// responses. Without it the response "model" is the full .gguf path;
		// with it the clean model id is returned, matching how other providers
		// echo their upstream model name.
		"--alias", m.ID,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.Port),
		"-c", strconv.Itoa(cfg.ContextSize),
		"-np", "1",
		"-sps", "0.0",
		"--no-cache-prompt",
	}
	// Multimodal: when the model ships a projector, load it so the server
	// accepts image inputs (OpenAI image_url content). Without --mmproj the
	// same model loads text-only.
	if m.MMProjPath != "" {
		args = append(args, "--mmproj", m.MMProjPath)
	}
	if ngl > 0 {
		args = append(args, "-ngl", strconv.Itoa(ngl))
	}
	// Disable model "thinking": without this, reasoning-tuned models can spend
	// the entire request timeout emitting reasoning_content before any answer.
	if cfg.NoThinking {
		args = append(args, "--reasoning-budget", "0")
	}
	args = append(args, cfg.ExtraArgs...)

	// Detach from the request context: the server must outlive the request
	// that started it. Lifecycle is controlled via e.cancel / Stop().
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := prepareCommand(procCtx, bin, args...)
	cmd.Stdout = logWriter{}
	cmd.Stderr = logWriter{}

	// Stop any other running engines before we spawn this one
	registerRunningEngine(e)

	if err := startCommand(cmd); err != nil {
		cancel()
		unregisterRunningEngine(e)
		return fmt.Errorf("start llama-server: %w", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	e.cmd = cmd
	e.cancel = cancel
	e.done = done
	e.currentModel = m.ID
	e.baseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.Port)

	// Wait for the model to load on a context independent of the request that
	// triggered the start. A per-attempt request timeout (often shorter than a
	// cold model load) must not abort — and thereby kill — a server that other
	// requests will reuse. The cold-start request itself may still time out at
	// the dispatch layer; the dispatcher's retry then finds the model warm.
	healthCtx, healthCancel := context.WithTimeout(context.Background(), loadTimeout)
	defer healthCancel()
	if err := e.waitHealthy(healthCtx, cfg.Port, done); err != nil {
		e.stopLocked()
		return err
	}
	log.Printf("local engine: serving %q on %s", m.ID, e.baseURL)
	return nil
}

func (e *LocalEngine) waitHealthy(ctx context.Context, port int, done <-chan struct{}) error {
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(loadTimeout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return fmt.Errorf("llama-server exited before becoming healthy (check the model file and the log)")
		default:
		}

		resp, err := client.Get(healthURL)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("llama-server did not become healthy on port %d within %s", port, loadTimeout)
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return fmt.Errorf("llama-server exited before becoming healthy (check the model file and the log)")
		}
	}
}

func (e *LocalEngine) stopLocked() {
	unregisterRunningEngine(e)
	if e.cancel != nil {
		e.cancel()
	}
	if e.done != nil {
		select {
		case <-e.done:
		case <-time.After(5 * time.Second):
			if e.cmd != nil && e.cmd.Process != nil {
				_ = e.cmd.Process.Kill()
			}
		}
	}
	e.cmd = nil
	e.cancel = nil
	e.done = nil
	e.currentModel = ""
	e.baseURL = ""
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// findModelFile locates a model file by name: absolute paths are used as-is,
// otherwise dir/name is tried, then dir is walked recursively for the first
// filename match (catalog downloads land in per-entry subfolders).
func findModelFile(dir, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if filepath.IsAbs(name) {
		return name, fileExists(name)
	}
	if direct := filepath.Join(dir, name); fileExists(direct) {
		return direct, true
	}
	if dir == "" {
		return "", false
	}
	var found string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// logWriter forwards stdout/stderr lines to the standard logger,
// which setupLogging mirrors into ~/.operatorlm/operatorlm.log.
type logWriter struct {
	prefix string
}

func (l logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			prefix := l.prefix
			if prefix == "" {
				prefix = "llama-server"
			}
			log.Printf("[%s] %s", prefix, line)
		}
	}
	return len(p), nil
}

var (
	globalEngineMu sync.Mutex
	runningEngines = make(map[*LocalEngine]bool)
)

func registerRunningEngine(e *LocalEngine) {
	globalEngineMu.Lock()
	var toStop []*LocalEngine
	for other := range runningEngines {
		if other != e {
			toStop = append(toStop, other)
		}
	}
	runningEngines[e] = true
	globalEngineMu.Unlock()

	for _, other := range toStop {
		other.Stop()
	}
}

func unregisterRunningEngine(e *LocalEngine) {
	globalEngineMu.Lock()
	delete(runningEngines, e)
	globalEngineMu.Unlock()
}

func (e *LocalEngine) manageWhisper(cfg config.LocalModelsConfig) {
	e.procMu.Lock()
	defer e.procMu.Unlock()

	if !cfg.WhisperEnabled {
		e.stopWhisperLocked()
		return
	}

	// If whisper server is already running, check if config changed (port or model)
	if e.whisperCmd != nil && (e.cfg.WhisperPort != cfg.WhisperPort || e.currentWspMod != cfg.WhisperModel) {
		e.stopWhisperLocked()
	}

	if e.whisperCmd == nil {
		go e.startWhisperLocked(cfg)
	}
}

func (e *LocalEngine) stopWhisperLocked() {
	if e.whisperCancel != nil {
		e.whisperCancel()
		e.whisperCancel = nil
	}
	if e.whisperCmd != nil {
		// Wait for command exit
		if e.whisperCmd.Process != nil {
			_ = e.whisperCmd.Process.Kill()
		}
		if e.whisperDone != nil {
			<-e.whisperDone
			e.whisperDone = nil
		}
		e.whisperCmd = nil
		e.currentWspMod = ""
	}
}

func (e *LocalEngine) startWhisperLocked(cfg config.LocalModelsConfig) {
	bin := cfg.WhisperServerPath
	if !fileExists(bin) {
		if _, err := exec.LookPath(bin); err != nil {
			log.Printf("local engine: whisper-server not found (%q): install it or set whisper_server_path", bin)
			return
		}
	}

	// Resolve the model path
	modelFile := cfg.WhisperModel
	if modelFile == "" {
		modelFile = "ggml-base.bin" // Default fallback
	}
	modelPath, ok := findModelFile(cfg.ModelsDir, modelFile)
	if !ok {
		log.Printf("local engine: whisper model file %q not found under %s", modelFile, cfg.ModelsDir)
		return
	}

	args := []string{
		"-m", modelPath,
		"--port", strconv.Itoa(cfg.WhisperPort),
		"--host", "127.0.0.1",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := prepareCommand(ctx, bin, args...)
	cmd.Stdout = logWriter{prefix: "whisper-server"}
	cmd.Stderr = logWriter{prefix: "whisper-server"}

	if err := cmd.Start(); err != nil {
		log.Printf("local engine: failed to start whisper-server: %v", err)
		cancel()
		return
	}

	e.whisperCmd = cmd
	e.whisperCancel = cancel
	e.currentWspMod = cfg.WhisperModel
	done := make(chan struct{})
	e.whisperDone = done

	log.Printf("local engine: whisper-server started on port %d with model %s", cfg.WhisperPort, modelFile)

	go func() {
		defer close(done)
		_ = cmd.Wait()
		log.Printf("local engine: whisper-server stopped")
	}()
}

func (e *LocalEngine) managePiper(cfg config.LocalModelsConfig) {
	e.procMu.Lock()
	defer e.procMu.Unlock()

	if !cfg.PiperEnabled {
		e.stopPiperLocked()
		return
	}

	// If piper server is already running, check if port changed
	if e.piperSrv != nil && e.cfg.PiperPort != cfg.PiperPort {
		e.stopPiperLocked()
	}

	if e.piperSrv == nil {
		_ = e.startPiperServer(cfg.PiperPort, cfg.PiperPath, cfg.ModelsDir)
	}
}

func (e *LocalEngine) stopPiperLocked() {
	if e.piperSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.piperSrv.Shutdown(ctx)
		e.piperSrv = nil
		log.Printf("local engine: piper server stopped")
	}
}

func (e *LocalEngine) startPiperServer(port int, piperBin, modelsDir string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Model string `json:"model"`
			Input string `json:"input"`
			Voice string `json:"voice"`
			// OpenAI's response_format: "wav" (default, buffered, exact
			// Content-Length) or "pcm" (raw 16-bit LE mono streamed as it is
			// synthesized — the format has no length header, so it can be
			// flushed chunk by chunk).
			ResponseFormat string `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		fullModelPath, ok := e.resolvePiperVoice(req.Voice, req.Model)
		if !ok {
			http.Error(w, fmt.Sprintf("no piper voice found: none of voice=%q, model=%q or the configured default exist under %s", req.Voice, req.Model, modelsDir), http.StatusBadRequest)
			return
		}

		sampleRate := piperSampleRate(fullModelPath)

		if strings.EqualFold(req.ResponseFormat, "pcm") {
			streamPiperPCM(w, piperBin, fullModelPath, req.Input, sampleRate)
			return
		}

		wavBytes, err := runPiperCLI(piperBin, fullModelPath, req.Input, sampleRate)
		if err != nil {
			http.Error(w, "piper failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", strconv.Itoa(len(wavBytes)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wavBytes)
	})

	srv := &http.Server{
		Addr:    "127.0.0.1:" + strconv.Itoa(port),
		Handler: mux,
	}
	e.piperSrv = srv

	go func() {
		log.Printf("local engine: piper server listening on http://%s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("local engine: piper server error: %v", err)
		}
	}()
	return nil
}

// resolvePiperVoice locates a voice model, OpenAI-style: each candidate in
// order (the request's `voice` field first — an installed .onnx stem like
// es_AR-daniela-high — then the model name), then the configured default.
// OpenAI voice names like "alloy" won't resolve to a file and fall through
// to the default.
func (e *LocalEngine) resolvePiperVoice(candidates ...string) (string, bool) {
	e.dataMu.RLock()
	modelsDir := e.cfg.ModelsDir
	defModel := e.cfg.PiperModel
	e.dataMu.RUnlock()
	for _, cand := range append(candidates, defModel) {
		if cand == "" {
			continue
		}
		if !strings.HasSuffix(cand, ".onnx") {
			cand += ".onnx"
		}
		if p, ok := findModelFile(modelsDir, cand); ok {
			return p, true
		}
	}
	return "", false
}

// PiperVoiceInfo resolves a voice name to its model path and sample rate, for
// callers that need the rate before any audio exists (e.g. the realtime SSE
// stream announces it in its first event).
func (e *LocalEngine) PiperVoiceInfo(voice string) (string, int, error) {
	modelPath, ok := e.resolvePiperVoice(voice)
	if !ok {
		e.dataMu.RLock()
		dir := e.cfg.ModelsDir
		e.dataMu.RUnlock()
		return "", 0, fmt.Errorf("no piper voice found: neither %q nor the configured default exist under %s", voice, dir)
	}
	return modelPath, piperSampleRate(modelPath), nil
}

// PiperSynthesizeStream runs one piper process for a whole session: sentences
// are written to stdin as they arrive on the channel and raw PCM is handed to
// onAudio as it is produced. One process — and one .onnx load — per session,
// instead of per sentence, which is what makes LLM→TTS pipelining actually
// overlap (a per-sentence spawn pays the voice-model load again every time).
// Cancelling ctx kills the process.
func (e *LocalEngine) PiperSynthesizeStream(ctx context.Context, modelPath string, sentences <-chan string, onAudio func([]byte)) error {
	e.dataMu.RLock()
	piperBin := e.cfg.PiperPath
	e.dataMu.RUnlock()

	if !fileExists(piperBin) {
		if _, err := exec.LookPath(piperBin); err != nil {
			return fmt.Errorf("piper binary not found (%q)", piperBin)
		}
	}

	cmd := prepareCommand(ctx, piperBin, "--model", modelPath, "--output-raw")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		defer stdin.Close() // EOF lets piper finish the tail and exit
		for s := range sentences {
			// One line per sentence: piper synthesizes each stdin line in
			// order. Sentences never contain newlines (the splitter cuts on
			// them), but normalize defensively.
			line := strings.ReplaceAll(s, "\n", " ")
			if _, err := io.WriteString(stdin, line+"\n"); err != nil {
				return // process died; the read loop reports it
			}
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 && onAudio != nil {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			onAudio(chunk)
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("piper: %w | stderr: %s", err, stderr.String())
	}
	return nil
}

// piperSampleRate reads audio.sample_rate from the voice's .onnx.json config
// (downloaded alongside the model). Piper voices are not all 22.05 kHz — "low"
// quality voices are 16 kHz — so a hardcoded rate plays them at the wrong speed.
func piperSampleRate(modelPath string) int {
	const fallback = 22050
	b, err := os.ReadFile(modelPath + ".json")
	if err != nil {
		return fallback
	}
	var cfg struct {
		Audio struct {
			SampleRate int `json:"sample_rate"`
		} `json:"audio"`
	}
	if json.Unmarshal(b, &cfg) != nil || cfg.Audio.SampleRate <= 0 {
		return fallback
	}
	return cfg.Audio.SampleRate
}

// startPiperProc spawns piper with text on stdin and returns its raw-PCM
// stdout plus a wait func that surfaces stderr on failure. The caller must
// drain stdout to EOF before calling wait.
func startPiperProc(piperBin, modelPath, text string) (io.Reader, func() error, error) {
	if !fileExists(piperBin) {
		if _, err := exec.LookPath(piperBin); err != nil {
			return nil, nil, fmt.Errorf("piper binary not found (%q)", piperBin)
		}
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, nil, fmt.Errorf("piper model file not found (%q)", modelPath)
	}

	cmd := prepareCommand(context.Background(), piperBin, "--model", modelPath, "--output-raw")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	_, _ = io.WriteString(stdin, text+"\n")
	_ = stdin.Close()

	wait := func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("wait: %w | stderr: %s", err, stderr.String())
		}
		return nil
	}
	return stdout, wait, nil
}

func runPiperCLI(piperBin, modelPath, text string, sampleRate int) ([]byte, error) {
	stdout, wait, err := startPiperProc(piperBin, modelPath, text)
	if err != nil {
		return nil, err
	}
	pcmData, readErr := io.ReadAll(stdout)
	if err := wait(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	return addWavHeader(pcmData, sampleRate), nil
}

// streamPiperPCM copies piper's stdout to the client as it is synthesized:
// no Content-Length, one Flush per chunk (Go switches to chunked transfer
// automatically). The client hears the first sentence while the rest is
// still being generated instead of waiting for the whole input.
func streamPiperPCM(w http.ResponseWriter, piperBin, modelPath, text string, sampleRate int) {
	stdout, wait, err := startPiperProc(piperBin, modelPath, text)
	if err != nil {
		http.Error(w, "piper failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("audio/L16; rate=%d; channels=1", sampleRate))
	// Non-standard convenience header: lets clients configure playback
	// without parsing the MIME parameters.
	w.Header().Set("X-Sample-Rate", strconv.Itoa(sampleRate))
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	clientGone := false
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 && !clientGone {
			if _, werr := w.Write(buf[:n]); werr != nil {
				// Client hung up. Keep draining stdout to EOF so piper can
				// finish and exit — abandoning the pipe would fill its buffer
				// and deadlock wait() below.
				clientGone = true
			} else if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	if err := wait(); err != nil {
		// Headers are already sent, so the error can only be logged.
		log.Printf("local engine: piper stream: %v", err)
	}
}

func addWavHeader(pcm []byte, sampleRate int) []byte {
	numChannels := 1
	bitsPerSample := 16
	subchunk2Size := len(pcm)
	chunkSize := 36 + subchunk2Size
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	buf := make([]byte, 44+subchunk2Size)
	
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(chunkSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], uint16(numChannels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(buf[34:36], uint16(bitsPerSample))
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(subchunk2Size))
	copy(buf[44:], pcm)
	
	return buf
}
