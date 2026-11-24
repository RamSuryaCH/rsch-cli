// Package risk derives a conservative, explainable risk level from action
// metadata. It does not make authorization decisions; policy remains the
// enforcement boundary.
package risk

import "strings"

type Level string

const (
	Low      Level = "low"
	Medium   Level = "medium"
	High     Level = "high"
	Critical Level = "critical"
)

type Input struct {
	Tool         string
	Path         string
	Command      []string
	Capabilities []string
	Network      bool
	Mutating     bool
}
type Assessment struct {
	Level   Level    `json:"level"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

func Assess(input Input) Assessment {
	assessment := Assessment{Level: Low, Reasons: []string{}}
	lower := strings.ToLower(input.Tool + " " + input.Path + " " + strings.Join(input.Command, " ") + " " + strings.Join(input.Capabilities, " "))
	add := func(score int, reason string) {
		assessment.Score += score
		assessment.Reasons = append(assessment.Reasons, reason)
	}
	if input.Mutating {
		add(2, "action mutates workspace or external state")
	}
	if input.Network || strings.Contains(lower, "network") || strings.Contains(lower, "http") || strings.Contains(lower, "browser") {
		add(3, "network-capable action")
	}
	if strings.Contains(lower, "auth") || strings.Contains(lower, "security") || strings.Contains(lower, "credential") || strings.Contains(lower, "secret") {
		add(3, "security-sensitive resource")
	}
	if strings.Contains(lower, "production") || strings.Contains(lower, "deploy") || strings.Contains(lower, "delete") || strings.Contains(lower, "rm ") {
		add(4, "production or destructive operation")
	}
	if strings.Contains(lower, "shell") || strings.Contains(lower, "exec") || len(input.Command) > 0 {
		add(1, "process execution")
	}
	switch {
	case assessment.Score >= 7:
		assessment.Level = Critical
	case assessment.Score >= 4:
		assessment.Level = High
	case assessment.Score >= 2:
		assessment.Level = Medium
	}
	return assessment
}
