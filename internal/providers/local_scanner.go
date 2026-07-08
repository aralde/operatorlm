package providers

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// LocalModel describes a GGUF model file discovered on disk.
type LocalModel struct {
	ID         string `json:"id"`                    // model id exposed to clients (without prefix)
	Path       string `json:"path"`                  // absolute path to the (first-shard) .gguf file
	SizeBytes  int64  `json:"size_bytes"`            // size of the .gguf file
	MMProjPath string `json:"mmproj_path,omitempty"` // multimodal projector (enables vision) when present
}

// HasVision reports whether the model ships a multimodal projector.
func (m LocalModel) HasVision() bool { return m.MMProjPath != "" }

// shardRe matches the split-GGUF suffix, e.g. "-00001-of-00003".
var shardRe = regexp.MustCompile(`(?i)-(\d{5})-of-\d{5}$`)

// mmprojRe identifies a multimodal projector file (e.g. "mmproj-model-f16.gguf").
// These are NOT chat models on their own; they pair with a base model via --mmproj.
var mmprojRe = regexp.MustCompile(`(?i)mmproj`)

// mmprojPrefixRe strips a leading "mmproj" token from a projector filename stem.
var mmprojPrefixRe = regexp.MustCompile(`(?i)^mmproj[-_.]?`)

// quantRe matches a trailing quantization / precision token, e.g. "-Q4_K_M",
// ".Q3_K_M", "-f16", "-MXFP4". Stripping it yields a model "family" key so a
// projector can be matched to its base model regardless of quant level.
var quantRe = regexp.MustCompile(`(?i)[-_.](q\d+(_[0-9a-z]+)*|iq\d+(_[0-9a-z]+)*|f16|f32|bf16|fp16|fp32|mxfp\d+)$`)

// familyKey reduces a filename stem to a quant-independent identity used to pair
// projectors with their base models (e.g. both "google_gemma-3-it-Q3_K_M" and
// "mmproj-google_gemma-3-it-f16" reduce to "google_gemma-3-it").
func familyKey(stem string) string {
	s := strings.ToLower(stem)
	s = mmprojPrefixRe.ReplaceAllString(s, "")
	s = quantRe.ReplaceAllString(s, "")
	return s
}

// ScanLocalModels walks dir recursively and returns one entry per usable GGUF
// model. Split models (multi-shard GGUF) collapse to their first shard, since
// llama-server loads the remaining shards automatically. Multimodal projector
// files (mmproj-*.gguf) are not listed as models; instead they are attached to
// the matching base model so vision can be enabled with --mmproj. Unreadable
// directories are skipped rather than aborting the whole scan.
func ScanLocalModels(dir string) ([]LocalModel, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}

	type fileEnt struct {
		stem string
		path string
		size int64
	}
	var models, projectors []fileEnt

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip permission errors etc., keep scanning siblings
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".gguf") {
			return nil
		}

		stem := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if m := shardRe.FindStringSubmatch(stem); m != nil {
			if m[1] != "00001" {
				return nil // only the first shard represents the model
			}
			stem = shardRe.ReplaceAllString(stem, "")
		}

		var size int64
		if info, ierr := d.Info(); ierr == nil {
			size = info.Size()
		}

		ent := fileEnt{stem: stem, path: path, size: size}
		if mmprojRe.MatchString(stem) {
			projectors = append(projectors, ent)
		} else {
			models = append(models, ent)
		}
		return nil
	})

	// Index projectors by family key (name match) and by directory (so a lone
	// projector in a model's own folder pairs even with a generic name).
	projByFamily := make(map[string]string, len(projectors))
	projByDir := make(map[string][]string)
	for _, p := range projectors {
		projByFamily[familyKey(p.stem)] = p.path
		d := filepath.Dir(p.path)
		projByDir[d] = append(projByDir[d], p.path)
	}
	modelsPerDir := make(map[string]int)
	for _, mdl := range models {
		modelsPerDir[filepath.Dir(mdl.path)]++
	}

	byID := make(map[string]LocalModel)
	for _, mdl := range models {
		id := mdl.stem
		// Disambiguate identical filenames in different folders.
		if existing, clash := byID[id]; clash && existing.Path != mdl.path {
			id = mdl.stem + "@" + filepath.Base(filepath.Dir(mdl.path))
		}
		lm := LocalModel{ID: id, Path: mdl.path, SizeBytes: mdl.size}
		dir := filepath.Dir(mdl.path)
		if proj, ok := projByFamily[familyKey(mdl.stem)]; ok {
			lm.MMProjPath = proj
		} else if len(projByDir[dir]) == 1 && modelsPerDir[dir] == 1 {
			// Fallback: a dedicated per-model folder containing exactly one model
			// and one projector (how catalog downloads are laid out). Only then is
			// pairing unambiguous, so text-only models in shared folders are safe.
			lm.MMProjPath = projByDir[dir][0]
		}
		byID[id] = lm
	}

	out := make([]LocalModel, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, walkErr
}
