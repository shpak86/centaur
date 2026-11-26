package tunnel

// registerTunnelRequest is the connector's registration payload: the models it
// announces to the tunnel pool.
type registerTunnelRequest struct {
	Models []string `json:"models"`
}

// registerTunnelResponse carries the token the connector uses to bind its
// connection to the registered tunnel.
type registerTunnelResponse struct {
	Token string `json:"token"`
}

// tagDto is one model entry of the Ollama-compatible GET /api/tags response.
// Name and Model hold the same value: Ollama clients read either field, and the
// pool has no per-model metadata (digest, size, quantization) to report, so only
// the model name is filled in.
type tagDto struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// tagsResponseDto is the GET /api/tags response: the models the pool can route to
// right now. Unlike the OpenAI list, Ollama's tags response has no top-level
// "object" field, so the field is optional and omitted while it is empty.
type tagsResponseDto struct {
	Object string   `json:"object,omitempty"`
	Models []tagDto `json:"models"`
}

// modelDto is one model entry of the OpenAI-compatible GET /v1/models response.
type modelDto struct {
	Id     string `json:"id"`
	Object string `json:"object"`
}

// modelsResponseDto is the GET /v1/models response: the same advertised models as
// GET /api/tags, wrapped in the OpenAI list envelope ("object":"list" and a
// "data" array).
type modelsResponseDto struct {
	Object string     `json:"object"`
	Data   []modelDto `json:"data"`
}
