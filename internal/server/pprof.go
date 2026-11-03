package server

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
)

// NewPprofServer builds the opt-in Go profiling listener.
//
// It is deliberately a separate http.Server: none of these handlers are
// mounted on the public API mux, and the address must be loopback-only. The
// loopback check is repeated here (not just in config.Validate) so a caller
// that builds a Config programmatically cannot expose heap and goroutine
// state to the network.
func NewPprofServer(addr string) (*http.Server, error) {
	if !config.IsLoopbackAddr(addr) {
		return nil, fmt.Errorf("pprof addr %q is not a loopback address; refusing to expose profiling endpoints", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// A heap profile of a multi-gigabyte process can take minutes to
		// serialize; do not cut it off mid-download.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}, nil
}
