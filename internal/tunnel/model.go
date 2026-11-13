package tunnel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// modelPaths are the endpoints whose request body carries the model name.
var modelPaths = map[string]struct{}{
	"/api/generate":        {},
	"/api/chat":            {},
	"/api/embed":           {},
	"/api/embeddings":      {},
	"/v1/chat/completions": {},
	"/v1/completions":      {},
	"/v1/embeddings":       {},
}

// modelBody is the minimal part of a request body needed for routing.
type modelBody struct {
	Model string `json:"model"`
}

// getModel reads the request body on model endpoints and returns the model name.
// The body is not lost: it is put back into req.Body in full, because RoundTrip
// writes it to the tunnel afterwards through req.Write.
//
// An empty string with a nil error means the request has no model: the endpoint
// is not a model endpoint, the body is empty/compressed, or the JSON has no
// "model" field. An error is returned when the body cannot be read or parsed as
// JSON.
func getModel(req *http.Request) (string, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return "", nil
	}
	if _, ok := modelPaths[req.URL.Path]; !ok {
		return "", nil
	}
	// A compressed body cannot be parsed without decompressing it, and it must
	// be forwarded as is.
	if req.Header.Get("Content-Encoding") != "" {
		return "", nil
	}

	buf, err := io.ReadAll(req.Body)
	if err != nil {
		return "", fmt.Errorf("read request body: %w", err)
	}
	if len(buf) == 0 {
		return "", nil
	}

	// Restore the body: req.Write writes it in RoundTrip.
	// ContentLength is required: Request.Write takes the length from it, and
	// with zero and a non-empty body the request would go out chunked. GetBody
	// is kept valid so the request stays correct for a resend.
	req.ContentLength = int64(len(buf))
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}

	var parsed modelBody
	if err := json.Unmarshal(buf, &parsed); err != nil {
		return "", fmt.Errorf("%w: %w", errBadRequestBody, err)
	}
	return parsed.Model, nil
}
