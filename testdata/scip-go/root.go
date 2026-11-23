// Package fixture is the module root package. Its method config.Load must
// not share an id with config/'s Load, and its route is the root
// package's.
package fixture

import "net/http"

type config struct{}

func (config) Load() error { return nil }

func Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", health)
}

func health(w http.ResponseWriter, r *http.Request) {}
