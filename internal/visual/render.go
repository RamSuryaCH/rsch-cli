package visual

import (
	"crypto/rand"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/intelligence"
)

const (
	renderColumns   = 4
	renderMaxGroups = 24
	renderBoxWidth  = 250
	renderBoxHeight = 112
	renderGap       = 22
	renderMargin    = 24
)

type renderGroup struct {
	Name  string
	Nodes int
}

// RenderArchitecture writes a deterministic, bounded PNG overview of the
// repository architecture graph. Files are grouped into modules so large
// repositories remain inspectable instead of producing one node per symbol.
func RenderArchitecture(root string, graph intelligence.Graph) (Report, error) {
	if strings.TrimSpace(root) == "" {
		return Report{}, errors.New("repository root is required")
	}
	groups, _, edgeGroups := architectureGroups(graph)
	if len(groups) == 0 {
		return Report{}, errors.New("architecture graph contains no file nodes")
	}
	rows := (len(groups) + renderColumns - 1) / renderColumns
	width := renderMargin*2 + renderColumns*renderBoxWidth + (renderColumns-1)*renderGap
	height := renderMargin*2 + rows*renderBoxHeight + (rows-1)*renderGap
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 15, G: 23, B: 42, A: 255}}, image.Point{}, draw.Src)

	positions := make(map[string]image.Rectangle, len(groups))
	for index, group := range groups {
		column := index % renderColumns
		row := index / renderColumns
		x := renderMargin + column*(renderBoxWidth+renderGap)
		y := renderMargin + row*(renderBoxHeight+renderGap)
		positions[group.Name] = image.Rect(x, y, x+renderBoxWidth, y+renderBoxHeight)
	}
	for _, edge := range edgeGroups {
		from, fromOK := positions[edge[0]]
		to, toOK := positions[edge[1]]
		if fromOK && toOK && edge[0] != edge[1] {
			drawLine(img, center(from), center(to), color.RGBA{R: 100, G: 116, B: 139, A: 150})
		}
	}
	maxNodes := 1
	for _, group := range groups {
		if group.Nodes > maxNodes {
			maxNodes = group.Nodes
		}
	}
	for _, group := range groups {
		box := positions[group.Name]
		base := groupColor(group.Name)
		draw.Draw(img, box, &image.Uniform{C: color.RGBA{R: base.R / 3, G: base.G / 3, B: base.B / 3, A: 255}}, image.Point{}, draw.Src)
		drawBorder(img, box, base)
		drawLabel(img, image.Point{X: box.Min.X + 16, Y: box.Min.Y + 16}, fmt.Sprintf("%s %d", group.Name, group.Nodes), color.RGBA{R: 230, G: 236, B: 245, A: 255})
		barWidth := (box.Dx() - 32) * group.Nodes / maxNodes
		if barWidth > 0 {
			bar := image.Rect(box.Min.X+16, box.Max.Y-30, box.Min.X+16+barWidth, box.Max.Y-16)
			draw.Draw(img, bar, &image.Uniform{C: base}, image.Point{}, draw.Src)
		}
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return Report{}, err
	}
	defer workspace.Close()
	relativePath := filepath.Join(".rsch", "visual", "architecture.png")
	path := filepath.Join(root, relativePath)
	if err := workspace.MkdirAll(filepath.Dir(relativePath), 0o700); err != nil {
		return Report{}, err
	}
	// Write through a workspace-confined handle, then replace the directory
	// entry. Truncating an existing output could overwrite a symlink or hardlink
	// target, and exposing partial PNGs makes concurrent readers unreliable.
	tempPath := filepath.Join(filepath.Dir(relativePath), ".architecture-"+rand.Text()+".png")
	file, err := workspace.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Report{}, err
	}
	defer workspace.Remove(tempPath)
	encodeErr := png.Encode(file, img)
	closeErr := file.Close()
	if encodeErr != nil {
		return Report{}, encodeErr
	}
	if closeErr != nil {
		return Report{}, closeErr
	}
	if err := workspace.Rename(tempPath, relativePath); err != nil {
		return Report{}, err
	}
	report, err := Analyze(path, root)
	if err != nil {
		return Report{}, fmt.Errorf("analyze rendered architecture image: %w", err)
	}
	return report, nil
}

func architectureGroups(graph intelligence.Graph) ([]renderGroup, map[string]string, [][2]string) {
	counts := map[string]int{}
	rawNodeGroups := map[string]string{}
	for _, node := range graph.Nodes {
		if node.Kind == "symbol" || node.Path == "" {
			continue
		}
		group := architectureGroup(node.Path)
		rawNodeGroups[node.ID] = group
		counts[group]++
	}
	orderedNames := make([]string, 0, len(counts))
	for name := range counts {
		orderedNames = append(orderedNames, name)
	}
	sort.Slice(orderedNames, func(i, j int) bool {
		if counts[orderedNames[i]] == counts[orderedNames[j]] {
			return orderedNames[i] < orderedNames[j]
		}
		return counts[orderedNames[i]] > counts[orderedNames[j]]
	})
	if len(orderedNames) > renderMaxGroups {
		selected := orderedNames[:renderMaxGroups-1]
		keep := map[string]bool{}
		for _, name := range selected {
			keep[name] = true
		}
		// The overflow bucket must not share an identity with a real module,
		// including real modules that are themselves being collapsed.
		overflowName := "other"
		for suffix := 2; counts[overflowName] != 0; suffix++ {
			overflowName = fmt.Sprintf("other-%d", suffix)
		}
		overflowCount := 0
		for name, count := range counts {
			if !keep[name] {
				overflowCount += count
			}
		}
		counts[overflowName] = overflowCount
		orderedNames = append(append([]string{}, selected...), overflowName)
		for nodeID, name := range rawNodeGroups {
			if !keep[name] {
				rawNodeGroups[nodeID] = overflowName
			}
		}
	}
	groups := make([]renderGroup, 0, len(orderedNames))
	for _, name := range orderedNames {
		groups = append(groups, renderGroup{Name: name, Nodes: counts[name]})
	}
	edges := map[[2]string]bool{}
	for _, edge := range graph.Edges {
		if edge.Kind != "imports" {
			continue
		}
		from, fromOK := rawNodeGroups[edge.From]
		to, toOK := rawNodeGroups[edge.To]
		if fromOK && toOK && from != to {
			edges[[2]string{from, to}] = true
		}
	}
	orderedEdges := make([][2]string, 0, len(edges))
	for edge := range edges {
		orderedEdges = append(orderedEdges, edge)
	}
	sort.Slice(orderedEdges, func(i, j int) bool {
		if orderedEdges[i][0] == orderedEdges[j][0] {
			return orderedEdges[i][1] < orderedEdges[j][1]
		}
		return orderedEdges[i][0] < orderedEdges[j][0]
	})
	return groups, rawNodeGroups, orderedEdges
}

func architectureGroup(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) >= 2 && parts[0] == "internal" {
		return parts[0] + "/" + parts[1]
	}
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}
	return "root"
}

func center(rect image.Rectangle) image.Point {
	return image.Point{X: (rect.Min.X + rect.Max.X) / 2, Y: (rect.Min.Y + rect.Max.Y) / 2}
}

func drawBorder(img *image.RGBA, rect image.Rectangle, border color.RGBA) {
	for x := rect.Min.X; x < rect.Max.X; x++ {
		img.SetRGBA(x, rect.Min.Y, border)
		img.SetRGBA(x, rect.Max.Y-1, border)
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		img.SetRGBA(rect.Min.X, y, border)
		img.SetRGBA(rect.Max.X-1, y, border)
	}
}

func drawLine(img *image.RGBA, from, to image.Point, line color.RGBA) {
	dx := abs(to.X - from.X)
	dy := -abs(to.Y - from.Y)
	sx, sy := 1, 1
	if from.X > to.X {
		sx = -1
	}
	if from.Y > to.Y {
		sy = -1
	}
	err := dx + dy
	for {
		if image.Pt(from.X, from.Y).In(img.Bounds()) {
			img.SetRGBA(from.X, from.Y, line)
		}
		if from == to {
			return
		}
		double := 2 * err
		if double >= dy {
			err += dy
			from.X += sx
		}
		if double <= dx {
			err += dx
			from.Y += sy
		}
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func groupColor(name string) color.RGBA {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(name))
	value := hasher.Sum32()
	return color.RGBA{R: uint8(80 + value%120), G: uint8(100 + (value/7)%120), B: uint8(120 + (value/13)%110), A: 255}
}

// labelFont is a tiny dependency-free 5x7 bitmap font. Keeping the renderer
// in the standard library avoids adding a font dependency solely for evidence
// artifacts while still making generated diagrams self-identifying.
var labelFont = map[rune][]string{
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01111", "10000", "10000", "10000", "10000", "10000", "01111"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01111", "10000", "10000", "10111", "10001", "10001", "01111"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'I': {"11111", "00100", "00100", "00100", "00100", "00100", "11111"},
	'J': {"00111", "00010", "00010", "00010", "10010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"01110", "10000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00001", "01110"},
	'/': {"00001", "00010", "00010", "00100", "01000", "01000", "10000"},
	'_': {"00000", "00000", "00000", "00000", "00000", "00000", "11111"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	'.': {"00000", "00000", "00000", "00000", "00000", "00110", "00110"},
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
}

func drawLabel(img *image.RGBA, origin image.Point, text string, foreground color.RGBA) {
	maxPixels := renderBoxWidth - 32
	x := origin.X
	for _, character := range strings.ToUpper(text) {
		glyph, ok := labelFont[character]
		if !ok {
			glyph = labelFont[' ']
		}
		if x+5 > origin.X+maxPixels {
			break
		}
		for row, pattern := range glyph {
			for column, pixel := range pattern {
				if pixel == '1' {
					point := image.Point{X: x + column, Y: origin.Y + row}
					if point.In(img.Bounds()) {
						img.SetRGBA(point.X, point.Y, foreground)
					}
				}
			}
		}
		x += 6
	}
}
