package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type State struct {
	Version  int            `json:"version"`
	Origin   string         `json:"origin"`
	Cursor   string         `json:"cursor"`
	Schedule *ScheduleState `json:"schedule,omitempty"`
}

func LoadState(path, origin string) (State, error) {
	state := State{Version: 1, Origin: origin}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var saved State
	if err := json.Unmarshal(data, &saved); err != nil {
		return state, errors.New("corrupt reconciler state; preserved for inspection")
	}
	if saved.Version != 1 || saved.Origin != origin {
		return state, errors.New("reconciler state version/origin mismatch")
	}
	return saved, nil
}
func SaveState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".board-reconciler-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}
