package observability

import (
	"net/http"
	"net/http/pprof"
)

// NewAdminHandler serves the profiling endpoints under /debug/pprof/. It is
// meant for a separate admin listener that is never exposed publicly:
// profiles reveal internals and a CPU profile costs real CPU. The handlers
// are mounted on a private mux rather than through net/http/pprof's
// registration on http.DefaultServeMux, which is global state.
func NewAdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}
