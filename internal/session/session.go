package session

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/security"
)

type Session struct {
	ID               string  `json:"id"`
	ParentSessionID  string  `json:"parentSessionId,omitempty"`
	Title            string  `json:"title"`
	MessageCount     int64   `json:"messageCount"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	SummaryMessageID string  `json:"summaryMessageId,omitempty"`
	Cost             float64 `json:"cost"`
	CreatedAt        int64   `json:"createdAt"`
	UpdatedAt        int64   `json:"updatedAt"`
}

type Usage struct {
	ID           string        `json:"id"`
	SessionID    string        `json:"sessionId"`
	TaskID       string        `json:"taskId,omitempty"`
	AgentID      string        `json:"agentId,omitempty"`
	Model        string        `json:"model,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	InputTokens  int64         `json:"inputTokens"`
	OutputTokens int64         `json:"outputTokens"`
	Cost         float64       `json:"cost"`
	Duration     time.Duration `json:"duration"`
	StartedAt    time.Time     `json:"startedAt"`
	FinishedAt   time.Time     `json:"finishedAt"`
}

type Service interface {
	pubsub.Suscriber[Session]
	Create(ctx context.Context, title string) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	Resume(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]Session, error)
	Save(ctx context.Context, session Session) (Session, error)
	Delete(ctx context.Context, id string) error
	RecordUsage(ctx context.Context, usage Usage) error
	Usage(ctx context.Context, sessionID string) ([]Usage, error)
}

type service struct {
	*pubsub.Broker[Session]
	q  db.Querier
	db *sql.DB
}

func (s *service) Create(ctx context.Context, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:    uuid.New().String(),
		Title: security.RedactText(title),
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           security.RedactText(title),
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           "Generate a title",
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteSession(ctx, session.ID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, session)
	return nil
}

// Resume validates a durable session and closes any assistant messages whose
// process ended before writing a terminal state. The existing conversation is
// preserved; callers can safely continue it using the returned session ID.
func (s *service) Resume(ctx context.Context, id string) (Session, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if _, err := s.q.MarkInterruptedSessionMessages(ctx, id); err != nil {
		return Session{}, err
	}
	item, err = s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	s.Publish(pubsub.UpdatedEvent, item)
	return item, nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:               session.ID,
		Title:            security.RedactText(session.Title),
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
		SummaryMessageID: sql.NullString{
			String: session.SummaryMessageID,
			Valid:  session.SummaryMessageID != "",
		},
		Cost: session.Cost,
	})
	if err != nil {
		return Session{}, err
	}
	session = s.fromDBItem(dbSession)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

func (s *service) List(ctx context.Context) ([]Session, error) {
	dbSessions, err := s.q.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
	}
	return sessions, nil
}

func (s service) fromDBItem(item db.Session) Session {
	return Session{
		ID:               item.ID,
		ParentSessionID:  item.ParentSessionID.String,
		Title:            item.Title,
		MessageCount:     item.MessageCount,
		PromptTokens:     item.PromptTokens,
		CompletionTokens: item.CompletionTokens,
		SummaryMessageID: item.SummaryMessageID.String,
		Cost:             item.Cost,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func NewService(q db.Querier) Service {
	return NewServiceWithDB(q, nil)
}

func NewServiceWithDB(q db.Querier, database *sql.DB) Service {
	broker := pubsub.NewBroker[Session]()
	return &service{
		broker,
		q,
		database,
	}
}

// RecordUsage persists one model/provider observation without putting
// provider-specific telemetry into the session aggregate. It is optional for
// callers that use an in-memory or query-only session service.
func (s *service) RecordUsage(ctx context.Context, usage Usage) error {
	if s == nil || s.db == nil {
		return errors.New("usage database is unavailable")
	}
	if usage.SessionID == "" {
		return errors.New("usage session id is required")
	}
	if usage.ID == "" {
		usage.ID = uuid.NewString()
	}
	if usage.StartedAt.IsZero() {
		usage.StartedAt = time.Now().UTC().Add(-usage.Duration)
	}
	if usage.FinishedAt.IsZero() {
		usage.FinishedAt = usage.StartedAt.Add(usage.Duration)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_usage (id,session_id,task_id,agent_id,model,provider,input_tokens,output_tokens,cost,duration_ns,started_at,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, usage.ID, usage.SessionID, usage.TaskID, usage.AgentID, usage.Model, usage.Provider, usage.InputTokens, usage.OutputTokens, usage.Cost, int64(usage.Duration), usage.StartedAt.UnixMilli(), usage.FinishedAt.UnixMilli())
	return err
}

func (s *service) Usage(ctx context.Context, sessionID string) ([]Usage, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("usage database is unavailable")
	}
	query := `SELECT id,session_id,task_id,agent_id,model,provider,input_tokens,output_tokens,cost,duration_ns,started_at,finished_at FROM model_usage`
	args := []any{}
	if sessionID != "" {
		query += " WHERE session_id=?"
		args = append(args, sessionID)
	}
	query += " ORDER BY started_at,id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Usage{}
	for rows.Next() {
		var item Usage
		var duration, started, finished int64
		if err := rows.Scan(&item.ID, &item.SessionID, &item.TaskID, &item.AgentID, &item.Model, &item.Provider, &item.InputTokens, &item.OutputTokens, &item.Cost, &duration, &started, &finished); err != nil {
			return nil, err
		}
		item.Duration = time.Duration(duration)
		item.StartedAt = time.UnixMilli(started).UTC()
		item.FinishedAt = time.UnixMilli(finished).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}
