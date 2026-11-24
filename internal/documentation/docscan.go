package docindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const maxDocumentBytes = 8 << 20

type Document struct {
	Path     string   `json:"path"`
	Title    string   `json:"title,omitempty"`
	Headings []string `json:"headings,omitempty"`
	Links    []string `json:"links,omitempty"`
}
type BrokenLink struct {
	From   string `json:"from"`
	Link   string `json:"link"`
	Reason string `json:"reason"`
}
type Report struct {
	Root        string       `json:"root"`
	Documents   []Document   `json:"documents"`
	BrokenLinks []BrokenLink `json:"brokenLinks,omitempty"`
}

// Impact describes documentation areas that should be reviewed for a set of
// changed repository paths. It is intentionally conservative: it reports
// review obligations, rather than claiming that a document is sufficient.
type Impact struct {
	Changed  []string            `json:"changed"`
	Required []string            `json:"required"`
	Covered  map[string][]string `json:"covered"`
	Missing  []string            `json:"missing,omitempty"`
}

// Assess maps changed source/configuration paths to documentation areas and
// finds existing documents whose path or title indicates that area.
func Assess(report Report, changed []string) Impact {
	impact := Impact{Changed: append([]string{}, changed...), Covered: map[string][]string{}}
	seen := map[string]bool{}
	for _, raw := range changed {
		path := strings.ToLower(filepath.ToSlash(strings.TrimSpace(raw)))
		if path == "" {
			continue
		}
		areas := []string{}
		switch {
		case strings.Contains(path, "api") || strings.Contains(path, "handler") || strings.Contains(path, "route"):
			areas = append(areas, "API docs")
		case strings.HasSuffix(path, ".json") || strings.Contains(path, "config") || strings.Contains(path, ".env"):
			areas = append(areas, "configuration documentation")
		}
		if strings.Contains(path, "migration") || strings.Contains(path, "schema") || strings.Contains(path, "database") {
			areas = append(areas, "migration guides")
		}
		if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx") || strings.HasSuffix(path, ".py") {
			areas = append(areas, "developer documentation")
		}
		if strings.Contains(path, "architecture") || strings.Contains(path, "design") {
			areas = append(areas, "architecture docs")
		}
		for _, area := range areas {
			if !seen[area] {
				impact.Required = append(impact.Required, area)
				seen[area] = true
			}
		}
	}
	for _, doc := range report.Documents {
		candidate := strings.ToLower(doc.Path + " " + doc.Title)
		for _, area := range impact.Required {
			if matchesArea(candidate, area) {
				impact.Covered[area] = append(impact.Covered[area], doc.Path)
			}
		}
	}
	for _, area := range impact.Required {
		if len(impact.Covered[area]) == 0 {
			impact.Missing = append(impact.Missing, area)
		}
	}
	sort.Strings(impact.Required)
	sort.Strings(impact.Missing)
	return impact
}

func matchesArea(candidate, area string) bool {
	switch area {
	case "API docs":
		return strings.Contains(candidate, "api") || strings.Contains(candidate, "openapi")
	case "configuration documentation":
		return strings.Contains(candidate, "config") || strings.Contains(candidate, "configuration")
	case "migration guides":
		return strings.Contains(candidate, "migration") || strings.Contains(candidate, "upgrade")
	case "architecture docs":
		return strings.Contains(candidate, "architecture") || strings.Contains(candidate, "design")
	case "developer documentation":
		return strings.Contains(candidate, "develop") || strings.Contains(candidate, "contribut") || strings.Contains(candidate, "guide") || strings.Contains(candidate, "readme")
	default:
		return false
	}
}

func Scan(ctx context.Context, root string) (Report, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, errors.New("documentation root is not a directory")
	}
	workspace, err := os.OpenRoot(absolute)
	if err != nil {
		return Report{}, err
	}
	defer workspace.Close()
	report := Report{Root: absolute, Documents: []Document{}, BrokenLinks: []BrokenLink{}}
	err = fs.WalkDir(workspace.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "." && ignored(filepath.Base(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks and special files are not documents. Rooted opens also
		// prevent a replacement symlink from escaping the workspace.
		if !entry.Type().IsRegular() {
			return nil
		}
		if filepath.Ext(path) != ".md" && filepath.Ext(path) != ".mdx" {
			return nil
		}
		doc, parseErr := parse(workspace, path)
		if parseErr != nil {
			return parseErr
		}
		report.Documents = append(report.Documents, doc)
		for _, link := range doc.Links {
			if broken := checkLocalLink(workspace, path, link); broken != nil {
				report.BrokenLinks = append(report.BrokenLinks, *broken)
			}
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Documents, func(i, j int) bool { return report.Documents[i].Path < report.Documents[j].Path })
	sort.Slice(report.BrokenLinks, func(i, j int) bool {
		if report.BrokenLinks[i].From == report.BrokenLinks[j].From {
			return report.BrokenLinks[i].Link < report.BrokenLinks[j].Link
		}
		return report.BrokenLinks[i].From < report.BrokenLinks[j].From
	})
	return report, nil
}
func parse(root *os.Root, path string) (Document, error) {
	file, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return Document{}, err
	}
	defer file.Close()
	source, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return Document{}, err
	}
	if len(source) > maxDocumentBytes {
		return Document{}, fmt.Errorf("documentation file %q exceeds %d bytes", path, maxDocumentBytes)
	}
	doc := Document{Path: filepath.ToSlash(path), Headings: []string{}, Links: []string{}}
	node := goldmark.DefaultParser().Parse(text.NewReader(source))
	err = ast.Walk(node, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.Heading:
			heading := string(node.Text(source))
			if doc.Title == "" {
				doc.Title = heading
			}
			doc.Headings = append(doc.Headings, heading)
		case *ast.Link:
			doc.Links = append(doc.Links, string(node.Destination))
		case *ast.Image:
			doc.Links = append(doc.Links, string(node.Destination))
		}
		return ast.WalkContinue, nil
	})
	return doc, err
}
func checkLocalLink(root *os.Root, from, link string) *BrokenLink {
	// Resolve CommonMark escapes and character references as the Markdown
	// renderer does before interpreting URL path, query, and fragment syntax.
	parsed, err := url.Parse(string(util.URLEscape([]byte(link), true)))
	if err != nil {
		return &BrokenLink{From: filepath.ToSlash(from), Link: link, Reason: "invalid URL"}
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(link, "#") {
		return nil
	}
	target := parsed.Path
	if target == "" {
		return nil
	}
	full := filepath.Join(filepath.Dir(from), filepath.FromSlash(target))
	if strings.HasPrefix(target, "/") {
		full = filepath.FromSlash(strings.TrimPrefix(target, "/"))
	}
	if !filepath.IsLocal(full) {
		return &BrokenLink{From: filepath.ToSlash(from), Link: link, Reason: "target is outside the workspace"}
	}
	if _, err := root.Stat(full); err != nil {
		reason := "target is inaccessible or outside the workspace"
		if errors.Is(err, fs.ErrNotExist) {
			reason = "target does not exist"
		}
		return &BrokenLink{From: filepath.ToSlash(from), Link: link, Reason: reason}
	}
	return nil
}
func ignored(name string) bool {
	switch name {
	case ".git", ".rsch", "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}
