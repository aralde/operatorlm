package server

import (
	"bytes"
	"encoding/json"
	"net/http"
)

const usageScanLimit = 8 << 20

// usageWriter passes every byte through unchanged and keeps the last usage
// block seen in the reply: the whole body for JSON replies, or each SSE
// "data:" line for streams.
type usageWriter struct {
	http.ResponseWriter
	stream     bool
	buf        bytes.Buffer
	overflow   bool
	found      bool
	prompt     int64
	completion int64
}

func (u *usageWriter) Write(p []byte) (int, error) {
	if u.stream {
		u.scanStream(p)
	} else if !u.overflow {
		if u.buf.Len()+len(p) > usageScanLimit {
			u.overflow = true
			u.buf.Reset()
		} else {
			u.buf.Write(p)
		}
	}
	return u.ResponseWriter.Write(p)
}

func (u *usageWriter) Flush() {
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (u *usageWriter) scanStream(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if u.buf.Len()+len(p) > usageScanLimit {
				u.buf.Reset()
				u.overflow = true
			} else if !u.overflow {
				u.buf.Write(p)
			}
			return
		}
		if !u.overflow {
			u.buf.Write(p[:i])
			u.parseLine(u.buf.Bytes())
		}
		u.buf.Reset()
		u.overflow = false
		p = p[i+1:]
	}
}

func (u *usageWriter) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	u.parse(bytes.TrimSpace(data))
}

// result returns the token counts once the reply is complete.
func (u *usageWriter) result() (prompt, completion int64, ok bool) {
	if u.stream {
		if !u.overflow && u.buf.Len() > 0 {
			u.parseLine(u.buf.Bytes())
		}
	} else if !u.overflow {
		u.parse(u.buf.Bytes())
	}
	return u.prompt, u.completion, u.found
}

type usageBlock struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
}

func (u *usageWriter) parse(data []byte) {
	if len(data) == 0 || data[0] != '{' || !bytes.Contains(data, []byte(`"usage"`)) {
		return
	}
	var env struct {
		Usage    *usageBlock `json:"usage"`
		Response *struct {
			Usage *usageBlock `json:"usage"`
		} `json:"response"`
		Message *struct {
			Usage *usageBlock `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &env) != nil {
		return
	}
	ub := env.Usage
	if ub == nil && env.Response != nil {
		ub = env.Response.Usage
	}
	if ub == nil && env.Message != nil {
		ub = env.Message.Usage
	}
	if ub == nil {
		return
	}
	prompt, hasPrompt := pick(ub.PromptTokens, ub.InputTokens)
	completion, hasCompletion := pick(ub.CompletionTokens, ub.OutputTokens)
	if !hasPrompt && !hasCompletion {
		return
	}
	if hasPrompt {
		u.prompt = prompt
	}
	if hasCompletion {
		u.completion = completion
	}
	u.found = true
}

func pick(a, b *int64) (int64, bool) {
	if a != nil {
		return *a, true
	}
	if b != nil {
		return *b, true
	}
	return 0, false
}
