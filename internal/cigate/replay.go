package cigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/d0ugal/graith/internal/atomicfile"
)

type ReplayKey struct {
	Kind  string
	Value string
}

type ReplayStore interface {
	Reserve(key ReplayKey) error
}

type MemoryReplayStore struct {
	mu   sync.Mutex
	seen map[ReplayKey]bool
}

type FileReplayStore struct {
	path string
	mu   sync.Mutex
}

func NewMemoryReplayStore() *MemoryReplayStore {
	return &MemoryReplayStore{seen: map[ReplayKey]bool{}}
}

func NewFileReplayStore(path string) *FileReplayStore {
	return &FileReplayStore{path: path}
}

func (store *MemoryReplayStore) Reserve(key ReplayKey) error {
	if store == nil {
		return errors.New("memory replay store is not initialised")
	}

	if key.Kind == "" || key.Value == "" {
		return fmt.Errorf("replay key is incomplete: %s/%s", key.Kind, key.Value)
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if store.seen[key] {
		return fmt.Errorf("replayed %s %s", key.Kind, key.Value)
	}

	store.seen[key] = true

	return nil
}

func (store *FileReplayStore) Reserve(key ReplayKey) error {
	if store == nil {
		return errors.New("file replay store is not initialised")
	}

	if key.Kind == "" || key.Value == "" {
		return fmt.Errorf("replay key is incomplete: %s/%s", key.Kind, key.Value)
	}

	if store.path == "" {
		return errors.New("file replay store path is required")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	return withFileReplayLock(store.path, func() error {
		state, err := readReplayState(store.path)
		if err != nil {
			return err
		}

		if state[key.Kind] == nil {
			state[key.Kind] = map[string]bool{}
		}

		if state[key.Kind][key.Value] {
			return fmt.Errorf("replayed %s %s", key.Kind, key.Value)
		}

		state[key.Kind][key.Value] = true

		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}

		return atomicfile.Write(store.path, append(data, '\n'), 0o600)
	})
}

func readReplayState(path string) (map[string]map[string]bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]map[string]bool{}, nil
	}

	if err != nil {
		return nil, err
	}

	var state map[string]map[string]bool
	if err := decodeStrict(path, data, &state); err != nil {
		return nil, err
	}

	if state == nil {
		state = map[string]map[string]bool{}
	}

	return state, nil
}

func withFileReplayLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create replay lock directory: %w", err)
	}

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open replay lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire replay lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	return fn()
}
