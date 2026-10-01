package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrHistoryConflict = errors.New("history conflict")
var ErrActionConflict = errors.New("action receipt conflict")
var errAlreadyApplied = errors.New("action already applied")

func ActionHash(envelope OpsEnvelope) (string, error) {
	hash, _, err := prepareAction(envelope, nil)
	return hash, err
}

func checkHistoryHeads(history []HistoryRecord, expected map[string]string) error {
	for taskID, head := range expected {
		actual := ""
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].TaskID == taskID {
				actual = history[i].ID
				break
			}
		}
		if head != actual {
			return fmt.Errorf("%w: task %s history changed", ErrHistoryConflict, taskID)
		}
	}
	return nil
}

func actionDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func findAction(history []HistoryRecord, taskID, actionID, hash string) (*WorkLogEntry, error) {
	for _, record := range history {
		if record.WorkLog != nil && record.TaskID == taskID && record.WorkLog.ActionID == actionID {
			if record.WorkLog.ActionHash != hash {
				return nil, ErrActionConflict
			}
			receipt := *record.WorkLog
			return &receipt, nil
		}
	}
	return nil, nil
}

func prepareAction(envelope OpsEnvelope, history []HistoryRecord) (string, *WorkLogEntry, error) {
	count := 0
	taskID, actionID := "", ""
	for _, op := range envelope.Ops {
		if op.WorkLog != nil && op.WorkLog.ActionID != "" {
			count++
			taskID = op.ID
			actionID = op.WorkLog.ActionID
		}
	}
	if count == 0 && envelope.Source != "board-reconciler" {
		return "", nil, nil
	}
	if count != 1 {
		return "", nil, errors.New("action envelope requires exactly one action-tagged work log")
	}
	if _, err := validateText("actionId", actionID, 128, true); err != nil || strings.TrimSpace(actionID) != actionID {
		return "", nil, errors.New("invalid actionId")
	}
	if _, ok := envelope.ExpectedHistoryHeads[taskID]; !ok || len(envelope.ExpectedHistoryHeads) != 1 {
		return "", nil, errors.New("action envelope requires exactly its card history head")
	}
	var revision uint64
	normalized := envelope
	normalized.Ops = append([]Operation{}, envelope.Ops...)
	for i, op := range envelope.Ops {
		if op.ID != taskID || op.ExpectedRevision == nil || *op.ExpectedRevision == 0 {
			return "", nil, errors.New("action operations require one card and expectedRevision")
		}
		if i == 0 {
			revision = *op.ExpectedRevision
		}
		if *op.ExpectedRevision != revision {
			return "", nil, errors.New("action revisions must match")
		}
		if op.Op == "" || op.Op == "create" || op.Op == "delete" {
			return "", nil, errors.New("action envelope cannot create or delete cards")
		}
		if op.WorkLog != nil {
			entry := *op.WorkLog
			if op.Op != "work_log" || entry.ActionID == "" || entry.Kind != WorkLogProgress || entry.NeedsReview {
				return "", nil, errors.New("action envelope requires one finalized progress audit")
			}
			entry.ActionID = ""
			entry.Text = strings.TrimSpace(entry.Text)
			entry.TaskID = taskID
			normalized.Ops[i].WorkLog = &entry
		}
	}
	hash, err := actionDigest(normalized)
	if err != nil {
		return "", nil, err
	}
	receipt, err := findAction(history, taskID, actionID, hash)
	return hash, receipt, err
}
