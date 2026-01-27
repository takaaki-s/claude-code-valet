package session

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Store handles session persistence
type Store struct {
	dataDir string
}

// NewStore creates a new store
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	return &Store{dataDir: dataDir}, nil
}

// Save persists a session
func (s *Store) Save(session *Session) error {
	path := filepath.Join(s.dataDir, session.ID+".json")
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Load loads a session by ID
func (s *Store) Load(id string) (*Session, error) {
	path := filepath.Join(s.dataDir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// LoadAll loads all sessions
func (s *Store) LoadAll() ([]*Session, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []*Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5] // Remove .json
		session, err := s.Load(id)
		if err != nil {
			continue // Skip invalid sessions
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// Delete removes a session file
func (s *Store) Delete(id string) error {
	path := filepath.Join(s.dataDir, id+".json")
	return os.Remove(path)
}
