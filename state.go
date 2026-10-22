package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// State remembers where a post already went, so a rerun after one platform
// failed doesn't double-post on the others.
type State map[string]Posted

type Posted struct {
	Result
	At time.Time `json:"at"`
}

const stateFile = ".multipost.json"

func loadState(dir string) (State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return nil, err
	}
	st := State{}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return st, nil
}

func (s State) save(dir string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFile), raw, 0o644)
}
