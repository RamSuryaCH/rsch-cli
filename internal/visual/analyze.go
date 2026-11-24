// Package visual provides deterministic image evidence extraction for
// screenshots and UI artifacts. It does not infer facts that the decoder
// cannot verify.
package visual

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/intelligence"
)

// A small compressed file may describe an enormous decoded image. Check the
// header before allocating pixels, allowing ordinary screenshots up to 8K.
const maxImagePixels = 32 * 1024 * 1024

type Color struct {
	R      uint8 `json:"r"`
	G      uint8 `json:"g"`
	B      uint8 `json:"b"`
	Pixels int   `json:"pixels"`
}
type Report struct {
	Path     string   `json:"path"`
	Format   string   `json:"format"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Bytes    int64    `json:"bytes"`
	Dominant []Color  `json:"dominantColors"`
	Hints    []string `json:"componentHints,omitempty"`
}

func Analyze(path, repository string) (Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Report{}, err
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		return Report{}, fmt.Errorf("decode image: %w", err)
	}
	if err := validateImageDimensions(config); err != nil {
		return Report{}, err
	}
	report := Report{Path: path, Format: format, Width: config.Width, Height: config.Height, Bytes: info.Size()}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Report{}, err
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return Report{}, fmt.Errorf("decode image pixels: %w", err)
	}
	report.Dominant = dominantColors(decoded)
	if strings.TrimSpace(repository) != "" {
		report.Hints = componentHints(repository, path)
	}
	return report, nil
}

// AnalyzeBytes decodes an image already held by a capability provider, such
// as a WebDriver screenshot. It returns the same deterministic evidence as
// Analyze without requiring the provider to persist a temporary image file.
func AnalyzeBytes(data []byte) (Report, error) {
	if len(data) == 0 {
		return Report{}, errors.New("image data is empty")
	}
	reader := bytes.NewReader(data)
	config, format, err := image.DecodeConfig(reader)
	if err != nil {
		return Report{}, fmt.Errorf("decode image: %w", err)
	}
	if err := validateImageDimensions(config); err != nil {
		return Report{}, err
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return Report{}, err
	}
	decoded, _, err := image.Decode(reader)
	if err != nil {
		return Report{}, fmt.Errorf("decode image pixels: %w", err)
	}
	return Report{Path: "<memory>", Format: format, Width: config.Width, Height: config.Height, Bytes: int64(len(data)), Dominant: dominantColors(decoded)}, nil
}

func validateImageDimensions(config image.Config) error {
	if config.Width <= 0 || config.Height <= 0 {
		return errors.New("image has invalid dimensions")
	}
	if config.Width > maxImagePixels/config.Height {
		return fmt.Errorf("image exceeds the %d pixel limit", maxImagePixels)
	}
	return nil
}

func dominantColors(img image.Image) []Color {
	counts := map[[3]uint8]int{}
	bounds := img.Bounds()
	step := 1
	if bounds.Dx()*bounds.Dy() > 1_000_000 {
		step = 4
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			key := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
			counts[key]++
		}
	}
	colors := make([]Color, 0, len(counts))
	for key, count := range counts {
		colors = append(colors, Color{R: key[0], G: key[1], B: key[2], Pixels: count})
	}
	sort.Slice(colors, func(i, j int) bool {
		if colors[i].Pixels == colors[j].Pixels {
			if colors[i].R == colors[j].R {
				if colors[i].G == colors[j].G {
					return colors[i].B < colors[j].B
				}
				return colors[i].G < colors[j].G
			}
			return colors[i].R < colors[j].R
		}
		return colors[i].Pixels > colors[j].Pixels
	})
	if len(colors) > 8 {
		colors = colors[:8]
	}
	return colors
}

func componentHints(repository, imagePath string) []string {
	index, err := intelligence.New(repository)
	if err != nil {
		return nil
	}
	if err := index.Build(context.Background()); err != nil {
		return nil
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath)))
	hints := []string{}
	for _, file := range index.Files() {
		name := strings.ToLower(filepath.Base(file.Path))
		if base != "" && (strings.Contains(name, base) || strings.Contains(base, strings.TrimSuffix(name, filepath.Ext(name)))) {
			hints = append(hints, file.Path)
		}
	}
	sort.Strings(hints)
	if len(hints) > 20 {
		hints = hints[:20]
	}
	return hints
}
