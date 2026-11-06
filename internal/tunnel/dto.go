package tunnel

type registerTunnelRequest struct {
	Models []string `json:"models"`
}

type registerTunnelResponse struct {
	Token string `json:"token"`
}
