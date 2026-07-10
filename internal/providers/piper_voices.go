package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// piperVoicesIndexURL is the official Piper voice index: every released voice
// with its language, region, quality and file listing.
const piperVoicesIndexURL = "https://huggingface.co/rhasspy/piper-voices/resolve/main/voices.json"

// piperVoicesBaseURL is the stable release the individual files are fetched from.
const piperVoicesBaseURL = "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/"

// PiperVoice is one downloadable TTS voice from the Piper index.
type PiperVoice struct {
	Key       string `json:"key"`      // e.g. es_AR-daniela-high
	Language  string `json:"language"` // e.g. es_AR
	LangName  string `json:"lang_name"`
	Country   string `json:"country"`
	Speaker   string `json:"speaker"`
	Quality   string `json:"quality"`
	SizeBytes int64  `json:"size_bytes"`

	Files []CatalogFile `json:"-"` // .onnx + .onnx.json download descriptors
}

// VoiceCatalogID is the Downloader state id / subfolder for a voice download.
func (v PiperVoice) VoiceCatalogID() string { return "piper-voice-" + v.Key }

// CatalogEntry adapts the voice to the generic catalog downloader.
func (v PiperVoice) CatalogEntry() CatalogEntry {
	return CatalogEntry{
		ID:      v.VoiceCatalogID(),
		Name:    v.Key,
		ModelID: v.Key + ".onnx",
		Files:   v.Files,
	}
}

var (
	piperVoicesMu     sync.Mutex
	piperVoicesCache  []PiperVoice
	piperVoicesExpiry time.Time
)

// FetchPiperVoices returns the parsed voice index, cached for an hour.
func FetchPiperVoices(ctx context.Context) ([]PiperVoice, error) {
	piperVoicesMu.Lock()
	defer piperVoicesMu.Unlock()
	if piperVoicesCache != nil && time.Now().Before(piperVoicesExpiry) {
		return piperVoicesCache, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, piperVoicesIndexURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch piper voice index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch piper voice index: status %d", resp.StatusCode)
	}

	var raw map[string]struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		Quality  string `json:"quality"`
		Language struct {
			Code           string `json:"code"`
			NameEnglish    string `json:"name_english"`
			CountryEnglish string `json:"country_english"`
		} `json:"language"`
		Files map[string]struct {
			SizeBytes int64 `json:"size_bytes"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse piper voice index: %w", err)
	}

	voices := make([]PiperVoice, 0, len(raw))
	for key, r := range raw {
		if r.Key == "" {
			r.Key = key
		}
		v := PiperVoice{
			Key:      r.Key,
			Language: r.Language.Code,
			LangName: r.Language.NameEnglish,
			Country:  r.Language.CountryEnglish,
			Speaker:  r.Name,
			Quality:  r.Quality,
		}
		for path, f := range r.Files {
			base := filepath.Base(path)
			var role string
			switch {
			case strings.HasSuffix(base, ".onnx"):
				role = "model"
			case strings.HasSuffix(base, ".onnx.json"):
				role = "config"
			default:
				continue // MODEL_CARD etc.
			}
			v.Files = append(v.Files, CatalogFile{
				URL:      piperVoicesBaseURL + path,
				Filename: base,
				Role:     role,
				Size:     f.SizeBytes,
			})
			v.SizeBytes += f.SizeBytes
		}
		if len(v.Files) == 0 {
			continue
		}
		// Deterministic file order: model first, then config.
		sort.Slice(v.Files, func(i, j int) bool { return v.Files[i].Role < v.Files[j].Role })
		voices = append(voices, v)
	}
	sort.Slice(voices, func(i, j int) bool {
		a, b := voices[i], voices[j]
		if a.LangName != b.LangName {
			return a.LangName < b.LangName
		}
		if a.Country != b.Country {
			return a.Country < b.Country
		}
		return a.Key < b.Key
	})

	piperVoicesCache = voices
	piperVoicesExpiry = time.Now().Add(time.Hour)
	return voices, nil
}

// PiperVoiceByKey finds a voice in the (cached) index.
func PiperVoiceByKey(ctx context.Context, key string) (PiperVoice, error) {
	voices, err := FetchPiperVoices(ctx)
	if err != nil {
		return PiperVoice{}, err
	}
	for _, v := range voices {
		if v.Key == key {
			return v, nil
		}
	}
	return PiperVoice{}, fmt.Errorf("unknown piper voice %q", key)
}

// InstalledPiperVoices walks dir for *.onnx files and returns their stems
// (voice ids usable in the OpenAI `voice` field), sorted.
func InstalledPiperVoices(dir string) []string {
	if dir == "" {
		return nil
	}
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".onnx") {
			seen[strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))] = true
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
