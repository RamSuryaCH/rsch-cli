// Package requirementaudit turns the numbered master specification into a
// machine-readable coverage report. It deliberately reports gaps instead of
// treating a matching feature name as proof of implementation.
package requirementaudit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/feature"
)

type Section struct {
	Number           int      `json:"number"`
	Title            string   `json:"title"`
	Status           string   `json:"status"`
	CandidateFeature []string `json:"candidateFeatures,omitempty"`
	Evidence         []string `json:"evidence,omitempty"`
	Gaps             []string `json:"gaps,omitempty"`
}

type Report struct {
	Version               int       `json:"version"`
	Prompt                string    `json:"prompt"`
	GeneratedAt           time.Time `json:"generatedAt"`
	TotalSections         int       `json:"totalSections"`
	UniqueNumbers         int       `json:"uniqueNumbers"`
	DuplicateNumbers      []int     `json:"duplicateNumbers,omitempty"`
	TrackedSections       int       `json:"trackedSections"`
	EvidenceSections      int       `json:"evidenceSections"`
	UntrackedSections     int       `json:"untrackedSections"`
	NotApplicableSections int       `json:"notApplicableSections"`
	Sections              []Section `json:"sections"`
}

var sectionPattern = regexp.MustCompile(`(?m)^#\s+([0-9]+)\.\s+(.+?)\s*$`)

// Explicit mappings cover specification sections whose prose title is not
// the same as the registry feature name. They are reviewed source mappings,
// not completion claims: concrete verification references are still required.
var sectionAliases = map[int][]string{
	4:   {"policy-execution", "platform-sandbox", "evidence-aggregation", "process-boundary"},
	13:  {"event-bus", "policy-execution", "process-boundary", "platform-sandbox"},
	29:  {"confidence-engine"},
	30:  {"independent-review"},
	34:  {"autonomy-policy"},
	53:  {"repository-intelligence"},
	55:  {"repository-intelligence"},
	68:  {"smart-commits", "architecture-impact", "git-safety"},
	70:  {"git-safety"},
	72:  {"visual-intelligence"},
	74:  {"infrastructure-intelligence"},
	77:  {"workflow-tui-surface", "terminal-command-surface"},
	78:  {"terminal-command-surface"},
	80:  {"explain-why"},
	81:  {"explain-why"},
	82:  {"workflow-tui-surface"},
	83:  {"workflow-tui-surface"},
	84:  {"agent-persistence"},
	85:  {"terminal-command-surface"},
	87:  {"event-bus", "enterprise-audit"},
	92:  {"configuration-hierarchy"},
	94:  {"ci-verification"},
	95:  {"policy-execution", "error-recovery"},
	97:  {"privacy-security", "enterprise-audit"},
	99:  {"monorepo-support"},
	101: {"performance-budgets"},
	105: {"enterprise-audit"},
	108: {"process-boundary", "performance-budgets"},
	111: {"cache-indexing"},
	116: {"verification-engine", "evidence-aggregation", "confidence-engine"},
	119: {"feature-registry", "evidence-aggregation"},
	127: {"confidence-engine", "evidence-aggregation"},
}

var notApplicable = map[int]string{
	10: "the master prompt contains a placeholder and no separate rsCLI source was supplied",
	39: "no Codex/OpenAI-derived Seatbelt profile-generation component is incorporated",
}

func Run(promptPath, root string) (Report, error) {
	if strings.TrimSpace(promptPath) == "" || strings.TrimSpace(root) == "" {
		return Report{}, errors.New("prompt path and repository root are required")
	}
	data, err := os.ReadFile(promptPath)
	if err != nil {
		return Report{}, err
	}
	registry, err := feature.Load(root)
	if err != nil {
		return Report{}, err
	}
	matches := sectionPattern.FindAllStringSubmatch(string(data), -1)
	report := Report{Version: 1, Prompt: promptPath, GeneratedAt: time.Now().UTC(), Sections: make([]Section, 0, len(matches))}
	numbers := map[int]int{}
	for _, match := range matches {
		number, _ := strconv.Atoi(match[1])
		numbers[number]++
		section := Section{Number: number, Title: strings.TrimSpace(match[2]), Status: "untracked"}
		if reason, ok := notApplicable[number]; ok {
			section.Status = "not-applicable"
			section.Gaps = append(section.Gaps, reason)
			report.NotApplicableSections++
			report.Sections = append(report.Sections, section)
			continue
		}
		aliases := map[string]bool{}
		for _, alias := range sectionAliases[number] {
			aliases[alias] = true
		}
		for _, item := range registry.Features {
			if score(section.Title, item.Name) == 0 && !aliases[item.ID] {
				continue
			}
			section.CandidateFeature = append(section.CandidateFeature, item.ID)
			for _, reference := range item.Verification {
				if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(reference))); statErr == nil {
					section.Evidence = append(section.Evidence, reference)
				}
			}
		}
		sort.Strings(section.CandidateFeature)
		section.Evidence = uniqueSorted(section.Evidence)
		if len(section.CandidateFeature) > 0 {
			section.Status = "tracked"
			report.TrackedSections++
		} else {
			section.Gaps = append(section.Gaps, "no registry feature matched this specification section")
			report.UntrackedSections++
		}
		if len(section.Evidence) > 0 {
			report.EvidenceSections++
		} else {
			section.Gaps = append(section.Gaps, "no concrete verification reference matched this section")
		}
		report.Sections = append(report.Sections, section)
	}
	sort.Slice(report.Sections, func(i, j int) bool { return report.Sections[i].Number < report.Sections[j].Number })
	report.TotalSections = len(report.Sections)
	report.UniqueNumbers = len(numbers)
	for number, count := range numbers {
		if count > 1 {
			report.DuplicateNumbers = append(report.DuplicateNumbers, number)
		}
	}
	sort.Ints(report.DuplicateNumbers)
	return report, nil
}

func Save(root string, report Report) error {
	path := filepath.Join(root, ".rsch", "requirements", "audit.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".audit-*.tmp")
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

func score(left, right string) int {
	leftWords := words(left)
	rightWords := words(right)
	count := 0
	for word := range leftWords {
		if rightWords[word] {
			count++
		}
	}
	return count
}

func words(value string) map[string]bool {
	value = strings.ToLower(value)
	value = strings.NewReplacer("/", " ", "-", " ", "_", " ", ":", " ", ",", " ").Replace(value)
	stop := map[string]bool{"and": true, "the": true, "of": true, "with": true, "for": true, "system": true, "engine": true, "support": true, "intelligence": true}
	out := map[string]bool{}
	for _, word := range strings.Fields(value) {
		if len(word) >= 4 && !stop[word] {
			word = strings.TrimSuffix(word, "s")
			out[word] = true
		}
	}
	return out
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
