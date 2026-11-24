package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/security"
)

type CreateMessageParams struct {
	Role  MessageRole
	Parts []ContentPart
	Model models.ModelID
}

type Service interface {
	pubsub.Suscriber[Message]
	Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error)
	Update(ctx context.Context, message Message) error
	Get(ctx context.Context, id string) (Message, error)
	List(ctx context.Context, sessionID string) ([]Message, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionMessages(ctx context.Context, sessionID string) error
}

type service struct {
	*pubsub.Broker[Message]
	q db.Querier
}

func NewService(q db.Querier) Service {
	return &service{
		Broker: pubsub.NewBroker[Message](),
		q:      q,
	}
}

func (s *service) Delete(ctx context.Context, id string) error {
	message, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteMessage(ctx, message.ID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, message)
	return nil
}

func (s *service) Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) {
	if params.Role != Assistant {
		params.Parts = append(params.Parts, Finish{
			Reason: "stop",
		})
	}
	partsJSON, err := marshallRedactedParts(params.Parts)
	if err != nil {
		return Message{}, err
	}
	dbMessage, err := s.q.CreateMessage(ctx, db.CreateMessageParams{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      string(params.Role),
		Parts:     string(partsJSON),
		Model:     sql.NullString{String: string(params.Model), Valid: true},
	})
	if err != nil {
		return Message{}, err
	}
	message, err := s.fromDBItem(dbMessage)
	if err != nil {
		return Message{}, err
	}
	s.Publish(pubsub.CreatedEvent, message)
	return message, nil
}

func (s *service) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	messages, err := s.List(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := s.q.DeleteSessionMessages(ctx, sessionID); err != nil {
		return err
	}
	for _, message := range messages {
		s.Publish(pubsub.DeletedEvent, message)
	}
	return nil
}

func (s *service) Update(ctx context.Context, message Message) error {
	parts, err := marshallRedactedParts(message.Parts)
	if err != nil {
		return err
	}
	canonicalParts, err := unmarshallParts(parts)
	if err != nil {
		return fmt.Errorf("failed to decode redacted message parts: %w", err)
	}
	message.Parts = canonicalParts
	finishedAt := sql.NullInt64{}
	if f := message.FinishPart(); f != nil {
		finishedAt.Int64 = f.Time
		finishedAt.Valid = true
		message.State = StateComplete
	}
	err = s.q.UpdateMessage(ctx, db.UpdateMessageParams{
		ID:         message.ID,
		Parts:      string(parts),
		Model:      sql.NullString{String: string(message.Model), Valid: message.Model != ""},
		FinishedAt: finishedAt,
	})
	if err != nil {
		return err
	}
	message.UpdatedAt = time.Now().Unix()
	s.Publish(pubsub.UpdatedEvent, message)
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Message, error) {
	dbMessage, err := s.q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

func (s *service) List(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) fromDBItem(item db.Message) (Message, error) {
	parts, err := unmarshallParts([]byte(item.Parts))
	if err != nil {
		return Message{}, err
	}
	state := State(item.State)
	if state == StateInterrupted {
		finished := false
		for _, part := range parts {
			if _, ok := part.(Finish); ok {
				finished = true
				break
			}
		}
		if !finished {
			parts = append(parts, Finish{Reason: FinishReasonError, Time: item.UpdatedAt})
		}
	}
	return Message{
		ID:        item.ID,
		SessionID: item.SessionID,
		Role:      MessageRole(item.Role),
		Parts:     parts,
		Model:     models.ModelID(item.Model.String),
		Sequence:  item.Sequence,
		State:     state,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}, nil
}

type partType string

const (
	reasoningType  partType = "reasoning"
	textType       partType = "text"
	imageURLType   partType = "image_url"
	binaryType     partType = "binary"
	toolCallType   partType = "tool_call"
	toolResultType partType = "tool_result"
	finishType     partType = "finish"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

// Redact text before JSON escaping so nested tool inputs remain valid JSON and
// their quoted credentials cannot evade matching. Copy parts to avoid mutating
// the caller's message; binary payloads are not text and must remain unchanged.
func marshallRedactedParts(parts []ContentPart) ([]byte, error) {
	redacted := make([]ContentPart, len(parts))
	for index, part := range parts {
		switch value := part.(type) {
		case ReasoningContent:
			value.Thinking = security.RedactText(value.Thinking)
			part = value
		case TextContent:
			value.Text = security.RedactText(value.Text)
			part = value
		case ImageURLContent:
			value.URL = security.RedactText(value.URL)
			value.Detail = security.RedactText(value.Detail)
			part = value
		case BinaryContent:
			part = redactBinaryMetadata(value)
		case ToolCall:
			value.ID = security.RedactText(value.ID)
			value.Name = security.RedactText(value.Name)
			value.Input = security.RedactText(value.Input)
			value.Type = security.RedactText(value.Type)
			part = value
		case ToolResult:
			value.ToolCallID = security.RedactText(value.ToolCallID)
			value.Name = security.RedactText(value.Name)
			value.Content = security.RedactText(value.Content)
			value.Metadata = security.RedactText(value.Metadata)
			if value.Media != nil {
				value.Media = append([]BinaryContent{}, value.Media...)
				for i := range value.Media {
					value.Media[i] = redactBinaryMetadata(value.Media[i])
				}
			}
			part = value
		case Finish:
			value.Reason = FinishReason(security.RedactText(string(value.Reason)))
			part = value
		}
		redacted[index] = part
	}
	return marshallParts(redacted)
}

func redactBinaryMetadata(value BinaryContent) BinaryContent {
	value.Path = security.RedactText(value.Path)
	value.MIMEType = security.RedactText(value.MIMEType)
	return value
}

func marshallParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningContent:
			typ = reasoningType
		case TextContent:
			typ = textType
		case ImageURLContent:
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall:
			typ = toolCallType
		case ToolResult:
			typ = toolResultType
		case Finish:
			typ = finishType
		default:
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

func unmarshallParts(data []byte) ([]ContentPart, error) {
	temp := []json.RawMessage{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0)

	for _, rawPart := range temp {
		var wrapper struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(rawPart, &wrapper); err != nil {
			return nil, err
		}

		switch wrapper.Type {
		case reasoningType:
			part := ReasoningContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case textType:
			part := TextContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case imageURLType:
			part := ImageURLContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case binaryType:
			part := BinaryContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolCallType:
			part := ToolCall{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolResultType:
			part := ToolResult{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case finishType:
			part := Finish{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		default:
			return nil, fmt.Errorf("unknown part type: %s", wrapper.Type)
		}

	}

	return parts, nil
}
