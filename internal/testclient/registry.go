package testclient

import (
	"encoding/json"
	"net/http"
	"os"
)

// FieldSpec describes a single field in a request body or query param.
type FieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`        // "string" | "number" | "boolean" | "object" | "array"
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Enum        []any  `json:"enum,omitempty"` // for select fields
}

// RouteSpec is the metadata for a single SDK endpoint.
// Registered alongside mux.Handle — one source of truth.
type RouteSpec struct {
	// Identity
	Method string `json:"method"` // "GET" | "POST" | "PATCH" | "DELETE"
	Path   string `json:"path"`   // e.g. "/sdk/wallets/{walletId}/sign/transaction/evm"

	// Display
	Group       string `json:"group"`       // "Wallets" | "Signing" | "Stablecoin" | "Sweep" | "Jobs" | "Balance"
	Name        string `json:"name"`        // short human name, e.g. "Sign EVM Transaction"
	Description string `json:"description"` // one sentence
	Async       bool   `json:"async"`       // true = returns job_id, poll until done

	// Cost
	Credits int `json:"credits"` // 0 = free

	// Parameters
	PathParams  []FieldSpec `json:"path_params,omitempty"`  // {walletId}, {chainType}, etc.
	QueryParams []FieldSpec `json:"query_params,omitempty"` // ?token=usdc&chain_id=137
	Body        []FieldSpec `json:"body,omitempty"`         // JSON body fields

	// Response shape hint for the result panel
	// Just the key field names the UI should highlight
	ResultFields []string `json:"result_fields,omitempty"`
}

// Registry holds all registered route specs.
// Built at startup in registerSDKRoutes — never mutated after that.
type Registry struct {
	routes []RouteSpec
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a route spec and returns the http.Handler unchanged.
// Use it as a wrapper around mux.Handle:
//
//	mux.Handle(pattern, reg.Register(spec, handler))
func (r *Registry) Register(spec RouteSpec, h http.Handler) http.Handler {
	r.routes = append(r.routes, spec)
	return h
}

// SpecHandler returns an http.HandlerFunc that serves the full route list as JSON.
// Mount at GET /testclient/spec.
func (r *Registry) SpecHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(r.routes) //nolint:errcheck
	}
}

// Enabled reports whether the test client should be served.
// Only true when ENABLE_TEST_UI=true — never in production.
func Enabled() bool {
	v := os.Getenv("ENABLE_TEST_UI")
	return v == "true" || v == "1"
}