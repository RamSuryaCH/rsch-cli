// Package interview creates concise, repository-aware clarification questions.
// It only asks about decisions that cannot be inferred from indexed evidence.
package interview

import (
	"context"
	"errors"
	"strings"

	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/intelligence"
)

type Question struct {
	ID      string   `json:"id"`
	Prompt  string   `json:"prompt"`
	Reason  string   `json:"reason"`
	Options []string `json:"options,omitempty"`
	Answer  string   `json:"answer,omitempty"`
}
type Session struct {
	Goal      string               `json:"goal"`
	Questions []Question           `json:"questions"`
	Evidence  []intelligence.Match `json:"evidence"`
}

func Start(ctx context.Context, root, goal string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if strings.TrimSpace(goal) == "" {
		return Session{}, errors.New("goal is required")
	}
	index, err := intelligence.New(root)
	if err != nil {
		return Session{}, err
	}
	if err := index.Build(ctx); err != nil {
		return Session{}, err
	}
	evidence, err := index.Search(ctx, goal, 10)
	if err != nil {
		return Session{}, err
	}
	questions := make([]Question, 0, 3)
	paths := index.Files()
	hasAuth, hasDB, hasUI := false, false, false
	for _, file := range paths {
		if err := ctx.Err(); err != nil {
			return Session{}, err
		}
		lower := strings.ToLower(file.Path)
		if strings.Contains(lower, "auth") || strings.Contains(lower, "oauth") {
			hasAuth = true
		}
		if strings.Contains(lower, "migration") || strings.Contains(lower, "schema") || strings.HasSuffix(lower, ".sql") {
			hasDB = true
		}
		if strings.Contains(lower, "frontend") || strings.Contains(lower, "web") || strings.HasSuffix(lower, ".tsx") {
			hasUI = true
		}
	}
	if !hasAuth {
		questions = append(questions, Question{ID: "auth-strategy", Prompt: "Which authentication or identity provider should this change use?", Reason: "No existing authentication boundary was found in repository evidence.", Options: []string{"Existing provider", "OAuth/OIDC", "Local credentials"}})
	}
	if hasDB {
		questions = append(questions, Question{ID: "migration-policy", Prompt: "What migration and rollback policy should apply to the data model change?", Reason: "Existing schema or migration files were detected.", Options: []string{"Backward-compatible", "Maintenance window", "No data migration"}})
	}
	if !hasUI {
		questions = append(questions, Question{ID: "interface-scope", Prompt: "Should this goal include a user-facing interface change?", Reason: "No frontend boundary was found in repository evidence.", Options: []string{"Backend/API only", "CLI/TUI", "Add web UI"}})
	}
	return Session{Goal: goal, Questions: questions, Evidence: evidence.Matches}, nil
}
func (s *Session) Answer(id, answer string) error {
	for i := range s.Questions {
		if s.Questions[i].ID == id {
			s.Questions[i].Answer = strings.TrimSpace(answer)
			return nil
		}
	}
	return errors.New("unknown interview question")
}
func (s Session) Requirements() []engineering.Requirement {
	requirements := make([]engineering.Requirement, 0, len(s.Questions))
	for _, question := range s.Questions {
		answer := strings.TrimSpace(question.Answer)
		if answer == "" {
			continue
		}
		requirements = append(requirements, engineering.Requirement{ID: question.ID, Statement: question.Prompt + " Answer: " + answer, Priority: "high", Status: engineering.StatusPending, Confirmed: true})
	}
	return requirements
}
