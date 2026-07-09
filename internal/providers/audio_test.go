package providers

import (
	"bytes"
	"mime/multipart"
	"testing"
)

func TestAddWavHeader(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	sampleRate := 22050
	wav := addWavHeader(pcm, sampleRate)

	if len(wav) != 44+len(pcm) {
		t.Errorf("expected length %d, got %d", 44+len(pcm), len(wav))
	}
	if !bytes.Equal(wav[0:4], []byte("RIFF")) {
		t.Errorf("expected RIFF header")
	}
	if !bytes.Equal(wav[8:12], []byte("WAVE")) {
		t.Errorf("expected WAVE header")
	}
	if !bytes.Equal(wav[36:40], []byte("data")) {
		t.Errorf("expected data subchunk")
	}
}

func TestRewriteMultipartModel(t *testing.T) {
	// Create a mock multipart body
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	
	err := mw.WriteField("model", "old-model")
	if err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	err = mw.WriteField("file", "fake-audio-bytes")
	if err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	_ = mw.Close()

	body := buf.Bytes()
	contentType := mw.FormDataContentType()

	rewritten, newContentType, err := rewriteMultipartModel(body, contentType, "new-model", map[string]string{"language": "auto"})
	if err != nil {
		t.Fatalf("rewriteMultipartModel failed: %v", err)
	}

	// Verify that rewritten body contains "new-model" and not "old-model"
	if bytes.Contains(rewritten, []byte("old-model")) {
		t.Errorf("expected old-model to be replaced")
	}
	if !bytes.Contains(rewritten, []byte("new-model")) {
		t.Errorf("expected new-model to be present")
	}
	if newContentType == "" {
		t.Errorf("expected non-empty newContentType")
	}
	// Default fields are appended only when absent
	if !bytes.Contains(rewritten, []byte(`name="language"`)) || !bytes.Contains(rewritten, []byte("auto")) {
		t.Errorf("expected default language field to be appended")
	}
	again, _, err := rewriteMultipartModel(rewritten, newContentType, "new-model", map[string]string{"language": "es"})
	if err != nil {
		t.Fatalf("second rewrite failed: %v", err)
	}
	if bytes.Contains(again, []byte(`>es<`)) || bytes.Count(again, []byte(`name="language"`)) != 1 {
		t.Errorf("existing language field must not be duplicated or overridden")
	}
}
