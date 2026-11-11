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
