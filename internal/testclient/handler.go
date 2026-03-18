package testclient

import (
	_ "embed"
	"net/http"
)

//go:embed testclient.html
var testClientHTML []byte

// HTMLHandler serves the static test client shell.
// The HTML is embedded at compile time — it never changes at runtime.
// All dynamic behaviour comes from fetching /testclient/spec.
func HTMLHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(testClientHTML) //nolint:errcheck
	}
}

// Mount registers all test client routes on the given mux.
// Only call this when testclient.Enabled() is true.
//
// Routes registered:
//   GET /testclient         → HTML shell
//   GET /testclient/spec    → JSON route specs
func Mount(mux *http.ServeMux, reg *Registry) {
	mux.HandleFunc("GET /testclient", HTMLHandler())
	mux.HandleFunc("GET /testclient/spec", reg.SpecHandler())
}