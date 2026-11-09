package tunnel

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestModelExtractsModel(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{"generate", "/api/generate", `{"model":"llama3:8b","prompt":"hi"}`, "llama3:8b"},
		{"chat", "/api/chat", `{"model":"qwen2.5:7b","messages":[]}`, "qwen2.5:7b"},
		{"embed", "/api/embed", `{"model":"nomic-embed-text","input":"hi"}`, "nomic-embed-text"},
		{"embeddings", "/api/embeddings", `{"model":"nomic","prompt":"hi"}`, "nomic"},
		{"v1 chat completions", "/v1/chat/completions", `{"model":"gpt-oss:20b","messages":[]}`, "gpt-oss:20b"},
		{"v1 completions", "/v1/completions", `{"model":"gpt-oss:20b","prompt":"hi"}`, "gpt-oss:20b"},
		{"v1 embeddings", "/v1/embeddings", `{"model":"nomic","input":"hi"}`, "nomic"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))

			model, err := getModel(req)
			if err != nil {
				t.Fatalf("requestModel() error = %v", err)
			}
			if model != tc.want {
				t.Fatalf("requestModel() = %q, want %q", model, tc.want)
			}

			assertRestoredBody(t, req, tc.body)
		})
	}
}

func TestRequestModelSkipsOtherRequests(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"get tags", http.MethodGet, "/api/tags", ""},
		{"get ps", http.MethodGet, "/api/ps", ""},
		{"pull is not a routing endpoint", http.MethodPost, "/api/pull", `{"model":"llama3:8b"}`},
		{"chat with get", http.MethodGet, "/api/chat", `{"model":"llama3:8b"}`},
		{"root path", http.MethodPost, "/", `{"model":"llama3:8b"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))

			model, err := getModel(req)
			if err != nil {
				t.Fatalf("requestModel() error = %v", err)
			}
			if model != "" {
				t.Fatalf("requestModel() = %q, want empty", model)
			}
			if got := readBody(t, req); got != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestRequestModelEmptyAndNilBody(t *testing.T) {
	tests := []struct {
		name string
		body io.Reader
	}{
		{"empty body", strings.NewReader("")},
		{"nil body", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/chat", tc.body)

			model, err := getModel(req)
			if err != nil {
				t.Fatalf("requestModel() error = %v", err)
			}
			if model != "" {
				t.Fatalf("requestModel() = %q, want empty", model)
			}
		})
	}
}

func TestRequestModelSkipsCompressedBody(t *testing.T) {
	const body = `{"model":"llama3:8b"}`

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Encoding", "gzip")

	model, err := getModel(req)
	if err != nil {
		t.Fatalf("requestModel() error = %v", err)
	}
	if model != "" {
		t.Fatalf("requestModel() = %q, want empty", model)
	}
	if got := readBody(t, req); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestRequestModelBadJSON(t *testing.T) {
	const body = `{"model":`

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))

	_, err := getModel(req)
	if !errors.Is(err, errBadRequestBody) {
		t.Fatalf("requestModel() error = %v, want errBadRequestBody", err)
	}

	// The body is restored even on error: after a 400 it does not go into the
	// tunnel, but req.Body stays readable for whoever closes the request.
	if got := readBody(t, req); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestRequestModelUnknownLengthSetsContentLength(t *testing.T) {
	const body = `{"model":"llama3:8b"}`

	// An io.ReadCloser of an unknown httptest type — ContentLength = -1,
	// like a request with a chunked body.
	req := httptest.NewRequest(http.MethodPost, "/api/chat", io.NopCloser(strings.NewReader(body)))
	if req.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1", req.ContentLength)
	}

	model, err := getModel(req)
	if err != nil {
		t.Fatalf("requestModel() error = %v", err)
	}
	if model != "llama3:8b" {
		t.Fatalf("requestModel() = %q, want %q", model, "llama3:8b")
	}

	assertRestoredBody(t, req, body)
}

func TestRequestModelLargeBodyStaysStreamable(t *testing.T) {
	// Valid JSON with a model, but over the limit: the model is not extracted,
	// while the body must be passed on in full, as a stream.
	body := `{"model":"llama3:8b","prompt":"` + strings.Repeat("x", maxModelBodyBytes) + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))

	model, err := getModel(req)
	if err != nil {
		t.Fatalf("requestModel() error = %v", err)
	}
	if model != "" {
		t.Fatalf("requestModel() = %q, want empty", model)
	}
	if req.ContentLength != int64(len(body)) {
		t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len(body))
	}

	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(got) != len(body) || string(got) != body {
		t.Fatalf("restored body length = %d, want %d", len(got), len(body))
	}
}

// assertRestoredBody checks that requestModel put the body back into req.Body
// consistently with ContentLength and GetBody.
func assertRestoredBody(t *testing.T, req *http.Request, want string) {
	t.Helper()

	if req.ContentLength != int64(len(want)) {
		t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len(want))
	}
	if got := readBody(t, req); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	if req.GetBody == nil {
		t.Fatal("GetBody is nil")
	}
	body, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody() error = %v", err)
	}
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read GetBody body: %v", err)
	}
	if string(got) != want {
		t.Fatalf("GetBody body = %q, want %q", got, want)
	}
}

func readBody(t *testing.T, req *http.Request) string {
	t.Helper()

	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(got)
}
