package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const stateVersion = 1

type Feedback struct {
	ID                    string    `json:"id"`
	Email                 string    `json:"email"`
	Content               string    `json:"content"`
	Status                string    `json:"status"`
	NotificationDelivered bool      `json:"notification_delivered"`
	NotificationError     string    `json:"notification_error,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type persistedState struct {
	Version   int        `json:"version"`
	BarkURL   string     `json:"bark_url,omitempty"`
	Feedbacks []Feedback `json:"feedbacks"`
}

type Store struct {
	mu        sync.RWMutex
	statePath string
	key       []byte
	state     persistedState
}

func NewStore(dataDir string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must contain exactly 32 bytes")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store := &Store{
		statePath: filepath.Join(dataDir, "feedback-state.enc"),
		key:       slices.Clone(key),
		state:     persistedState{Version: stateVersion, Feedbacks: []Feedback{}},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	ciphertext, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read feedback state: %w", err)
	}
	plaintext, err := decryptState(s.key, ciphertext)
	if err != nil {
		return fmt.Errorf("decrypt feedback state: %w", err)
	}
	var state persistedState
	if err := json.Unmarshal(plaintext, &state); err != nil {
		return fmt.Errorf("decode feedback state: %w", err)
	}
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported feedback state version %d", state.Version)
	}
	if state.Feedbacks == nil {
		state.Feedbacks = []Feedback{}
	}
	s.state = state
	return nil
}

func (s *Store) saveLocked() error {
	plaintext, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	ciphertext, err := encryptState(s.key, plaintext)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.statePath), ".feedback-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(ciphertext); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.statePath)
}

func encryptState(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, nil)...), nil
}

func decryptState(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("encrypted state is truncated")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
}

func feedbackID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "fb_" + hex.EncodeToString(raw), nil
}

func (s *Store) Create(email, content string) (Feedback, error) {
	id, err := feedbackID()
	if err != nil {
		return Feedback{}, err
	}
	now := time.Now().UTC()
	feedback := Feedback{ID: id, Email: email, Content: content, Status: "new", CreatedAt: now, UpdatedAt: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Feedbacks = append(s.state.Feedbacks, feedback)
	if err := s.saveLocked(); err != nil {
		s.state.Feedbacks = s.state.Feedbacks[:len(s.state.Feedbacks)-1]
		return Feedback{}, err
	}
	return feedback, nil
}

func (s *Store) List() []Feedback {
	s.mu.RLock()
	feedbacks := slices.Clone(s.state.Feedbacks)
	s.mu.RUnlock()
	slices.SortFunc(feedbacks, func(a, b Feedback) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return feedbacks
}

func (s *Store) UpdateStatus(id, status string) (Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Feedbacks {
		if s.state.Feedbacks[i].ID != id {
			continue
		}
		previous := s.state.Feedbacks[i]
		s.state.Feedbacks[i].Status = status
		s.state.Feedbacks[i].UpdatedAt = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			s.state.Feedbacks[i] = previous
			return Feedback{}, err
		}
		return s.state.Feedbacks[i], nil
	}
	return Feedback{}, os.ErrNotExist
}

func (s *Store) MarkNotification(id string, delivered bool, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Feedbacks {
		if s.state.Feedbacks[i].ID != id {
			continue
		}
		previous := s.state.Feedbacks[i]
		s.state.Feedbacks[i].NotificationDelivered = delivered
		s.state.Feedbacks[i].NotificationError = strings.TrimSpace(message)
		s.state.Feedbacks[i].UpdatedAt = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			s.state.Feedbacks[i] = previous
			return err
		}
		return nil
	}
	return os.ErrNotExist
}

func (s *Store) BarkURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.BarkURL
}

func (s *Store) SetBarkURL(value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.BarkURL
	s.state.BarkURL = strings.TrimSpace(value)
	if err := s.saveLocked(); err != nil {
		s.state.BarkURL = previous
		return err
	}
	return nil
}
