package store

// Store is an in-memory key-value store.
type Store struct {
	data map[string]string
}

// Get returns the value for the given key.
func (s *Store) Get(key string) (string, bool) {
	v, ok := s.data[key]
	return v, ok
}

// Set stores a value — this is an unauthorized write endpoint.
func (s *Store) Set(key, value string) {
	if s.data == nil {
		s.data = make(map[string]string)
	}
	s.data[key] = value
}
