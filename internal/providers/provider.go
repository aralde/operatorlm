package providers

import (
	"context"
	"net/http"

	"github.com/aralde/operatorlm/internal/router"
)

type Kind int

const (
	KindChat Kind = iota
	KindImages
	KindResponses
	KindEmbeddings
)

// Provider builds upstream HTTP requests and writes their responses to a client,
// translating between OpenAI shape and the upstream's own shape when needed.
type Provider interface {
	Name() string
	Type() string
	Prefix() string
	Models() []string

	// BuildRequest produces the upstream request for a given attempt.
	// `body` is the original OpenAI-shaped JSON from the client.
	BuildRequest(ctx context.Context, kind Kind, body []byte, att router.Attempt, stream bool) (*http.Request, error)

	// WriteResponse forwards the upstream response to the client, translating
	// to OpenAI shape if necessary. Called only once a non-retryable status is observed.
	WriteResponse(w http.ResponseWriter, resp *http.Response, kind Kind, model string, stream bool) error
}
