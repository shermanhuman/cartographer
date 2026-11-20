package app

import "testing"

func TestRun(t *testing.T) {
	s := &Server{store: diskStore{}, mem: &memStore{}}
	s.run(t.Context())
}
