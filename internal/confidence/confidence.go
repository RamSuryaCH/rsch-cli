// Package confidence calculates completion confidence only from persisted,
// inspectable evidence. Unobserved dimensions are never treated as passing.
package confidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/evidence"
)

type Dimension struct {
	Name     string   `json:"name"`
	Observed bool     `json:"observed"`
	Passed   bool     `json:"passed"`
	Weight   float64  `json:"weight"`
	Reason   string   `json:"reason,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type Report struct {
	Version        int         `json:"version"`
	Score          *float64    `json:"score,omitempty"`
	ObservedWeight float64     `json:"observedWeight"`
	TotalWeight    float64     `json:"totalWeight"`
	Complete       bool        `json:"complete"`
	Dimensions     []Dimension `json:"dimensions"`
}

type latestEvidence map[string]evidence.Record

func Build(root string) (Report, error) {
	if strings.TrimSpace(root) == "" {
		return Report{}, errors.New("confidence root is required")
	}
	store, err := evidence.New(root)
	if err != nil {
		return Report{}, err
	}
	items, err := store.List(0)
	if err != nil {
		return Report{}, err
	}
	latest := latestEvidence{}
	for _, item := range items {
		if current, ok := latest[item.Kind]; !ok || current.CapturedAt.Before(item.CapturedAt) {
			latest[item.Kind] = item
		}
	}
	report := Report{Version: 1, Dimensions: make([]Dimension, 0)}
	add := func(name string, weight float64, observed, passed bool, reason string, refs ...string) {
		report.TotalWeight += weight
		dimension := Dimension{Name: name, Weight: weight, Observed: observed, Passed: passed, Reason: reason, Evidence: unique(refs)}
		report.Dimensions = append(report.Dimensions, dimension)
		if observed {
			report.ObservedWeight += weight
		}
	}
	addRecord := func(name string, weight float64, item evidence.Record, passed bool, reason string) {
		observed := item.Status == evidence.Observed || item.Status == evidence.Failed
		if item.Status != evidence.Observed {
			passed = false
			reason = item.Reason
			if reason == "" {
				reason = item.Summary
			}
			if reason == "" {
				reason = "evidence status: " + string(item.Status)
			}
		}
		add(name, weight, observed, passed, reason, item.ID)
	}

	if item, ok := latest["requirements"]; ok {
		var audit struct {
			UntrackedSections *int `json:"untrackedSections"`
			TotalSections     *int `json:"totalSections"`
		}
		if err := json.Unmarshal(item.Data, &audit); err == nil && audit.UntrackedSections != nil && audit.TotalSections != nil && *audit.TotalSections > 0 && *audit.UntrackedSections >= 0 && *audit.UntrackedSections <= *audit.TotalSections {
			addRecord("requirements", 2, item, *audit.UntrackedSections == 0, fmt.Sprintf("%d of %d sections have registry coverage", *audit.TotalSections-*audit.UntrackedSections, *audit.TotalSections))
		} else {
			addRecord("requirements", 2, item, false, "requirements audit evidence is malformed or contains no sections")
		}
	} else {
		add("requirements", 2, false, false, "no requirements audit evidence recorded")
	}

	if item, ok := latest["evaluation"]; ok {
		var evaluation struct {
			RepositoryQualityPassed *bool `json:"repositoryQualityPassed"`
			TaskSuccess             *bool `json:"taskSuccess"`
		}
		if err := json.Unmarshal(item.Data, &evaluation); err == nil && evaluation.RepositoryQualityPassed != nil {
			addRecord("evaluation", 2, item, *evaluation.RepositoryQualityPassed, "repository-quality evaluation result")
		} else if err == nil && evaluation.TaskSuccess != nil {
			addRecord("evaluation", 2, item, *evaluation.TaskSuccess, "legacy evaluation result")
		} else {
			addRecord("evaluation", 2, item, false, "evaluation evidence is malformed")
		}
	} else {
		add("evaluation", 2, false, false, "no evaluation evidence recorded")
	}
	for _, name := range []string{"security", "review", "release", "documentation", "performance", "dependencies", "infrastructure", "database", "browser", "visual", "experiment"} {
		if item, ok := latest[name]; ok {
			reason := item.Summary
			if reason == "" {
				reason = string(item.Status)
			}
			addRecord(name, 1, item, true, reason)
		} else {
			add(name, 1, false, false, "no evidence recorded")
		}
	}
	sort.Slice(report.Dimensions, func(i, j int) bool { return report.Dimensions[i].Name < report.Dimensions[j].Name })
	if report.ObservedWeight > 0 {
		score := 0.0
		for _, dimension := range report.Dimensions {
			if dimension.Observed && dimension.Passed {
				score += dimension.Weight
			}
		}
		score = score / report.ObservedWeight * 100
		report.Score = &score
	}
	report.Complete = report.ObservedWeight == report.TotalWeight
	for _, dimension := range report.Dimensions {
		if dimension.Observed && !dimension.Passed {
			report.Complete = false
		}
	}
	return report, nil
}

func Save(root string, report Report) error {
	path := filepath.Join(root, ".rsch", "confidence", "latest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".confidence-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
