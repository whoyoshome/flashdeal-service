package httpapi

// healthResponse is the body of GET /healthz and GET /readyz.
type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}
