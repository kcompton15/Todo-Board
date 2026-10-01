package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kcompton15/Todo-Board/internal/store"
)

const maxInboxFileSize = 1 << 20

type Watcher struct {
	directory string
	interval  time.Duration
	store     *store.Store
	onChange  func()
	logger    *slog.Logger
}

func New(directory string, interval time.Duration, taskStore *store.Store, onChange func(), logger *slog.Logger) *Watcher {
	if interval <= 0 {
		interval = time.Second
	}
	if onChange == nil {
		onChange = func() {}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{
		directory: directory,
		interval:  interval,
		store:     taskStore,
		onChange:  onChange,
		logger:    logger,
	}
}

func (w *Watcher) Run(ctx context.Context) {
	w.scan()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.scan()
		}
	}
}

func (w *Watcher) scan() {
	entries, err := os.ReadDir(w.directory)
	if err != nil {
		w.logger.Error("read inbox", "path", w.directory, "error", err)
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		w.process(name)
	}
}

var errInboxReplay = errors.New("inbox action already applied")

func (w *Watcher) process(name string) {
	source := filepath.Join(w.directory, name)
	processing := filepath.Join(w.directory, ".processing-"+name)
	if err := os.Rename(source, processing); err != nil {
		w.logger.Error("claim inbox file", "path", source, "error", err)
		return
	}

	applyErr := w.apply(processing)
	if applyErr != nil && !errors.Is(applyErr, errInboxReplay) {
		if archiveErr := w.archiveFailure(processing, name, applyErr); archiveErr != nil {
			w.logger.Error("quarantine inbox file", "path", processing, "error", archiveErr)
		}
		return
	}
	if !errors.Is(applyErr, errInboxReplay) {
		w.onChange()
	}
	if _, err := w.archive(processing, filepath.Join(w.directory, ".processed"), name); err != nil {
		w.logger.Error("archive processed inbox file", "path", processing, "error", err)
		return
	}
	w.logger.Info("processed inbox file", "name", name)
}

func (w *Watcher) apply(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat payload: %w", err)
	}
	if info.Size() > maxInboxFileSize {
		return fmt.Errorf("payload exceeds %d bytes", maxInboxFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	if err := store.ValidateNoNullFields(data); err != nil {
		return fmt.Errorf("validate payload: %w", err)
	}

	var shape any
	if err := json.Unmarshal(data, &shape); err != nil {
		return fmt.Errorf("parse payload: %w", err)
	}
	switch shape.(type) {
	case []any:
		var inputs []store.TaskInput
		if err := json.Unmarshal(data, &inputs); err != nil {
			return fmt.Errorf("parse create list: %w", err)
		}
		if _, err := w.store.CreateMany(inputs, "inbox"); err != nil {
			return err
		}
		return nil
	case map[string]any:
		var envelope store.OpsEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			return fmt.Errorf("parse op envelope: %w", err)
		}
		if envelope.Source == "" {
			envelope.Source = "inbox"
		}
		_, replayed, err := w.store.ApplyEnvelope(envelope)
		if err != nil {
			return err
		}
		if replayed {
			return errInboxReplay
		}
		return nil
	default:
		return errors.New("payload must be a task array or op envelope")
	}
}

func (w *Watcher) archiveFailure(processing, originalName string, cause error) error {
	destination, err := w.archive(processing, filepath.Join(w.directory, ".failed"), originalName)
	if err != nil {
		return err
	}
	message := []byte(cause.Error() + "\n")
	if err := os.WriteFile(destination+".error.txt", message, 0o600); err != nil {
		return fmt.Errorf("write failure reason: %w", err)
	}
	w.logger.Warn("rejected inbox file", "name", originalName, "error", cause)
	return nil
}

func (w *Watcher) archive(source, directory, originalName string) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create archive directory: %w", err)
	}
	prefix := time.Now().UTC().Format("20060102T150405.000000000Z")
	destination := filepath.Join(directory, prefix+"-"+originalName)
	if err := os.Rename(source, destination); err != nil {
		return "", fmt.Errorf("move to archive: %w", err)
	}
	return destination, nil
}
