package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"
)

const maxLineBytes = 64 * 1024 * 1024

type Session struct {
	Client  string    `json:"client"`
	UA      string    `json:"ua,omitempty"`
	Records int       `json:"records"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

func scanLines(path string, fn func(line []byte, rec Record) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), maxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if err := fn(line, rec); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Export copies the NDJSON records at path to w byte for byte. A non-empty
// client keeps only the records of that session. It returns the number of
// records written.
func Export(path, client string, w io.Writer) (int, error) {
	n := 0
	err := scanLines(path, func(line []byte, rec Record) error {
		if client != "" && rec.Client != client {
			return nil
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// Sessions groups the records at path by client address, newest first.
func Sessions(path string) ([]Session, error) {
	byClient := map[string]*Session{}
	err := scanLines(path, func(_ []byte, rec Record) error {
		s := byClient[rec.Client]
		if s == nil {
			s = &Session{Client: rec.Client, UA: rec.UserAgent, First: rec.Timestamp}
			byClient[rec.Client] = s
		}
		s.Records++
		if rec.Timestamp.After(s.Last) {
			s.Last = rec.Timestamp
		}
		if rec.Timestamp.Before(s.First) {
			s.First = rec.Timestamp
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(byClient))
	for _, s := range byClient {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out, nil
}
