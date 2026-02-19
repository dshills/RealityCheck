package handler

// SessionStore persists session data — this violates the stateless constraint.
type SessionStore struct {
	sessions map[string]string
}

// Save persists a session value.
func (s *SessionStore) Save(id, data string) {
	if s.sessions == nil {
		s.sessions = make(map[string]string)
	}
	s.sessions[id] = data
}

// Handler processes requests.
type Handler struct {
	store *SessionStore
}
