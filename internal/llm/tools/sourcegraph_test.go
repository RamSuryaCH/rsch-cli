package tools

import (
	"fmt"
	"strings"
	"testing"
)

func sourcegraphTestResult(count int) map[string]any {
	results := make([]any, count)
	for i := range results {
		results[i] = map[string]any{
			"__typename":  "FileMatch",
			"repository":  map[string]any{"name": "example/repo"},
			"file":        map[string]any{"path": fmt.Sprintf("file%d.go", i), "content": "first\nsecond\nthird"},
			"lineMatches": []any{map[string]any{"lineNumber": float64(2), "preview": "second"}},
		}
	}
	return map[string]any{"data": map[string]any{"search": map[string]any{"results": map[string]any{
		"matchCount": float64(count), "resultCount": float64(count), "results": results,
	}}}}
}

func TestSourcegraphGraphQLErrorsFailFormatting(t *testing.T) {
	_, err := formatSourcegraphResults(map[string]any{"errors": []any{map[string]any{"message": "rate limit exceeded"}}}, 10, 10)
	if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("GraphQL error was reported as success: %v", err)
	}
}

func TestSourcegraphMalformedLineNumbersDoNotPanic(t *testing.T) {
	for _, number := range []float64{-1, 1e100, 1.5} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			result := sourcegraphTestResult(1)
			data := result["data"].(map[string]any)["search"].(map[string]any)["results"].(map[string]any)
			file := data["results"].([]any)[0].(map[string]any)
			file["lineMatches"] = []any{map[string]any{"lineNumber": number, "preview": "invalid"}}
			if _, err := formatSourcegraphResults(result, 10, 10); err == nil {
				t.Fatal("malformed source line was accepted")
			}
		})
	}
}

func TestSourcegraphHonorsRequestedResultCount(t *testing.T) {
	for _, count := range []int{1, 10, 20, 30} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			output, err := formatSourcegraphResults(sourcegraphTestResult(25), 10, count)
			if err != nil {
				t.Fatal(err)
			}
			want := min(count, 20)
			if got := strings.Count(output, "## Result "); got != want {
				t.Fatalf("rendered %d results, want %d", got, want)
			}
			if !strings.Contains(output, fmt.Sprintf("Showing the first %d results.", want)) {
				t.Fatal("locally truncated results were not disclosed")
			}
		})
	}
}
