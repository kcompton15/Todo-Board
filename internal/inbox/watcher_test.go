package inbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestInboxTrustTransactions(t *testing.T) {
	dir := t.TempDir()
	s, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.CreateMany([]store.TaskInput{{Title: "Inbox outcome", Lane: "today", Subtasks: store.SubtaskInputs{{Title: "Step"}}}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]
	broadcasts := 0
	w := New(filepath.Join(dir, "inbox"), time.Second, s, func() { broadcasts++ }, nil)
	writeAtomic(t, w.directory, "guard.json", fmt.Sprintf(`{"source":"chat-claude","ops":[{"op":"patch","id":%q,"lane":"done","expectedRevision":1}]}`, task.ID))
	w.scan()
	if broadcasts != 0 || len(s.Activity(task.ID)) != 1 || countFiles(t, filepath.Join(w.directory, ".failed"), ".json") != 1 {
		t.Fatal("rejected completion was not quarantined atomically")
	}
	writeAtomic(t, w.directory, "finish.json", fmt.Sprintf(`{"source":"chat-claude","ops":[{"op":"check","id":%q,"subtaskId":%q,"expectedRevision":1},{"op":"patch","id":%q,"lane":"done","expectedRevision":1}]}`, task.ID, task.Subtasks[0].ID, task.ID))
	w.scan()
	got, _ := s.Get(task.ID)
	if broadcasts != 1 || got.Revision != 2 || got.Lane != "done" || s.Activity(task.ID)[0].Actor != "chat-claude" {
		t.Fatalf("successful batch: broadcasts=%d task=%#v", broadcasts, got)
	}
	writeAtomic(t, w.directory, "stale.json", fmt.Sprintf(`{"ops":[{"op":"patch","id":%q,"notes":"stale","expectedRevision":1}]}`, task.ID))
	w.scan()
	got, _ = s.Get(task.ID)
	if broadcasts != 1 || got.Notes != "" || len(s.Activity(task.ID)) != 2 {
		t.Fatal("stale inbox write changed state")
	}
}

func TestWatcherProcessesAndQuarantinesWithoutStopping(t *testing.T) {
	dataDir := t.TempDir()
	taskStore, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	inboxDir := filepath.Join(dataDir, "inbox")
	var broadcasts atomic.Int32
	watcher := New(inboxDir, 10*time.Millisecond, taskStore, func() {
		broadcasts.Add(1)
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watcher.Run(ctx)

	if err := os.WriteFile(filepath.Join(inboxDir, "001-invalid.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAtomic(t, inboxDir, "002-valid.json", `[{"title":"from chat","project":"Web","kind":"probe"}]`)

	waitFor(t, func() bool {
		return len(taskStore.List(store.Filter{})) == 1 && countFiles(t, filepath.Join(inboxDir, ".processed"), ".json") == 1 && countFiles(t, filepath.Join(inboxDir, ".failed"), ".json") == 1
	})
	if broadcasts.Load() != 1 {
		t.Fatalf("expected one successful broadcast, got %d", broadcasts.Load())
	}
	if got := taskStore.List(store.Filter{}); len(got) != 1 || got[0].Kind != "probe" {
		t.Fatalf("inbox kind was not preserved: %#v", got)
	}
	failedEntries, err := os.ReadDir(filepath.Join(inboxDir, ".failed"))
	if err != nil {
		t.Fatal(err)
	}
	foundReason := false
	for _, entry := range failedEntries {
		if strings.HasSuffix(entry.Name(), ".error.txt") {
			data, readErr := os.ReadFile(filepath.Join(inboxDir, ".failed", entry.Name()))
			if readErr != nil {
				t.Fatal(readErr)
			}
			foundReason = strings.Contains(string(data), "unexpected end of JSON input")
		}
	}
	if !foundReason {
		t.Fatal("failed inbox file does not have a readable reason")
	}
}

func TestWatcherSkipsStagingFiles(t *testing.T) {
	dataDir := t.TempDir()
	taskStore, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	inboxDir := filepath.Join(dataDir, "inbox")
	if err := os.WriteFile(filepath.Join(inboxDir, ".staging-task.json"), []byte(`[{"title":"not yet"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher := New(inboxDir, time.Second, taskStore, nil, nil)
	watcher.scan()
	if got := taskStore.List(store.Filter{}); len(got) != 0 {
		t.Fatalf("staging file was processed: %#v", got)
	}
}

func writeAtomic(t *testing.T, directory, name, contents string) {
	t.Helper()
	staging := filepath.Join(directory, ".staging-"+name)
	if err := os.WriteFile(staging, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}
}

func countFiles(t *testing.T, directory, suffix string) int {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), suffix) {
			count++
		}
	}
	return count
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
