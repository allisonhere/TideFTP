// Package fakecredstore is an in-memory credstore.Store for tests, standing
// in for the OS keyring the same way fakefs and fakesession stand in for a
// real server.
package fakecredstore

import "tideftp/internal/credstore"

var _ credstore.Store = (*Store)(nil)

// Store keeps secrets in memory. Err, when set, is returned by every method
// instead of touching data, so tests can exercise what a user sees when the
// platform keyring is unavailable.
type Store struct {
	data map[string]string
	Err  error
}

// New returns an empty Store.
func New() *Store { return &Store{data: map[string]string{}} }

func (s *Store) Get(key string) (string, bool, error) {
	if s.Err != nil {
		return "", false, s.Err
	}
	password, ok := s.data[key]
	return password, ok, nil
}

func (s *Store) Set(key, password string) error {
	if s.Err != nil {
		return s.Err
	}
	if password == "" {
		delete(s.data, key)
		return nil
	}
	s.data[key] = password
	return nil
}

func (s *Store) Delete(key string) error {
	if s.Err != nil {
		return s.Err
	}
	delete(s.data, key)
	return nil
}
