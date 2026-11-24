package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SQLLog appends and replays versioned events. Append-only storage makes
// recovery and audit inspection possible without coupling consumers to SQLite.
type SQLLog struct{ db *sql.DB }

func NewSQLLog(db *sql.DB) *SQLLog { return &SQLLog{db: db} }
func (l *SQLLog) Append(ctx context.Context, event Event) error {
	return l.AppendEvent(ctx, &event)
}
func (l *SQLLog) AppendEvent(ctx context.Context, event *Event) error {
	return appendEvent(ctx, l.db, event)
}

// AppendEventTx appends to the caller's transaction so domain changes and their
// event commit or roll back together. The assigned sequence is provisional
// until the caller successfully commits; callers must not broadcast on error.
func AppendEventTx(ctx context.Context, tx *sql.Tx, event *Event) error {
	if tx == nil {
		return errors.New("event transaction is required")
	}
	if event == nil {
		return errors.New("event is required")
	}
	return appendEvent(ctx, tx, event)
}

type eventExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendEvent(ctx context.Context, executor eventExecutor, event *Event) error {
	if event == nil {
		return nil
	}
	safeData := redactMap(event.Data)
	data, err := json.Marshal(safeData)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `INSERT INTO event_log(id,version,type,occurred_at,session_id,goal_id,task_id,actor,data_json) VALUES(?,?,?,?,?,?,?,?,?)`, event.ID, event.Version, event.Type, event.At.UnixMilli(), event.SessionID, event.GoalID, event.TaskID, event.Actor, string(data))
	if err != nil {
		return err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return err
	}
	event.Sequence = sequence
	event.Data = safeData
	return nil
}
func (l *SQLLog) Replay(ctx context.Context, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := l.db.QueryContext(ctx, `SELECT sequence,id,version,type,occurred_at,session_id,goal_id,task_id,actor,data_json FROM event_log WHERE sequence>? ORDER BY sequence LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var sequence int64
		var event Event
		var kind string
		var millis int64
		var data string
		if err := rows.Scan(&sequence, &event.ID, &event.Version, &kind, &millis, &event.SessionID, &event.GoalID, &event.TaskID, &event.Actor, &data); err != nil {
			return nil, err
		}
		event.Sequence = sequence
		event.Type = Type(kind)
		event.At = unixMilli(millis)
		if err := json.Unmarshal([]byte(data), &event.Data); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
func unixMilli(value int64) (t time.Time) { return time.UnixMilli(value).UTC() }
