// Package audit provides append-only, inspectable local audit records.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/security"
)

type Entry struct {
	ID       string            `json:"id"`
	At       time.Time         `json:"at"`
	Actor    string            `json:"actor"`
	Action   string            `json:"action"`
	Resource string            `json:"resource,omitempty"`
	Decision string            `json:"decision,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
type Log struct {
	path string
	mu   sync.Mutex
}

func New(root string) (*Log, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("audit root is required")
	}
	path := filepath.Join(root, ".rsch", "audit", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Log{path: path}, nil
}
func (l *Log) Append(entry Entry) error {
	if l == nil {
		return errors.New("audit log is required")
	}
	if entry.Action == "" {
		return errors.New("audit action is required")
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	if entry.At.IsZero() {
		entry.At = time.Now().UTC()
	}
	safe := map[string]string{}
	for key, value := range entry.Metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "password") {
			safe[key] = "[REDACTED]"
		} else {
			safe[key] = security.RedactText(value)
		}
	}
	entry.Resource = security.RedactText(entry.Resource)
	entry.Action = security.RedactText(entry.Action)
	entry.Metadata = safe
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}
func (l *Log) List(limit int) ([]Entry, error) {
	if l == nil {
		return nil, errors.New("audit log is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := []Entry{}
	reader := bufio.NewReader(file)
	for {
		// Append accepts entries of any size. Read complete JSONL records
		// without Scanner's default 64 KiB token limit so every accepted entry
		// and the records after it remain inspectable.
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			break
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}
