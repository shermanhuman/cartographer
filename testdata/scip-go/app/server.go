package app

import (
	"context"
	"net/http"
	"strings"
)

type Server struct {
	store Store
	mem   *memStore
}

func (s *Server) f() {}

func register(fn func()) { fn() }

// run calls f once and passes the method value s.f once (one call, one ref),
// calls Save through the interface (dispatch) and on a concrete receiver.
func (s *Server) run(ctx context.Context) {
	s.f()
	register(s.f)
	s.store.Save(ctx, "a")
	s.mem.Save(ctx, "b")
	_ = Keys[string]("x", "y")
	_ = strings.ToUpper("external")
}

func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc        { return h }
func (s *Server) signedIn(h http.HandlerFunc) http.HandlerFunc     { return h }
func (s *Server) dialEvent(w http.ResponseWriter, r *http.Request) {}
func (s *Server) items(w http.ResponseWriter, r *http.Request)     {}

// Routes registers a wrapped handler (innermost function value), a plain one,
// a closure and a computed pattern (both not extracted).
func (s *Server) Routes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST /dial/events", s.guard(s.signedIn(s.dialEvent)))
	mux.Handle("/items/{id}", http.HandlerFunc(s.items))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc(prefix+"/x", s.items)
}
