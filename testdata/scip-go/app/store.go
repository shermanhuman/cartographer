package app

import "context"

// Store is implemented by memStore and diskStore: calls through it dispatch.
type Store interface {
	Save(ctx context.Context, key string) error
}

type memStore struct{}

// Save on memStore and Save on diskStore have the same name: distinct ids.
func (m *memStore) Save(ctx context.Context, key string) error { return nil }

type diskStore struct{ dir string }

func (d diskStore) Save(ctx context.Context, key string) error { return nil }

// Keys is generic and variadic: arity 1.
func Keys[T any](xs ...T) []T { return xs }

// helper shares its name with the external test package's helper.
func helper() int { return 0 }
