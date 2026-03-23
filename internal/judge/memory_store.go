package judge

import "sync"

type SubmissionStore interface {
	Save(sub Submission) error
	Get(id string) (Submission, bool)
	Update(sub Submission) error
}

type MemorySubmissionStore struct {
	mu   sync.RWMutex
	data map[string]Submission
}

func NewMemorySubmissionStore() *MemorySubmissionStore {
	return &MemorySubmissionStore{
		data: make(map[string]Submission),
	}
}

func (s *MemorySubmissionStore) Save(sub Submission) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[sub.ID] = sub
	return nil
}

func (s *MemorySubmissionStore) Get(id string) (Submission, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sub, ok := s.data[id]
	return sub, ok
}

func (s *MemorySubmissionStore) Update(sub Submission) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[sub.ID] = sub
	return nil
}
