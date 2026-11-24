package image

import (
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/disintegration/imaging"
	"github.com/lucasb-eyer/go-colorful"
)

// Bound both decoded input and rendered output: compressed file size and
// terminal width alone do not constrain memory use for very tall images.
const (
	maxImagePixels   = 32 * 1024 * 1024
	maxPreviewWidth  = 512
	maxPreviewHeight = 1024
)

func ValidateFileSize(filePath string, sizeLimit int64) (bool, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return false, fmt.Errorf("error getting file info: %w", err)
	}

	if fileInfo.Size() > sizeLimit {
		return true, nil
	}

	return false, nil
}

func ToString(width int, img image.Image) string {
	if img == nil || width <= 0 || img.Bounds().Empty() {
		return ""
	}
	width = min(width, maxPreviewWidth)
	height := float64(width) * float64(img.Bounds().Dy()) / float64(img.Bounds().Dx())
	if height > maxPreviewHeight {
		width = max(1, int(math.Round(float64(width)*maxPreviewHeight/height)))
		height = maxPreviewHeight
	}
	img = imaging.Resize(img, width, max(1, int(math.Round(height))), imaging.Lanczos)
	b := img.Bounds()
	imageWidth := b.Max.X
	h := b.Max.Y
	str := strings.Builder{}

	for heightCounter := 0; heightCounter < h; heightCounter += 2 {
		for x := range imageWidth {
			c1, _ := colorful.MakeColor(img.At(x, heightCounter))
			color1 := lipgloss.Color(c1.Hex())

			var color2 lipgloss.Color
			if heightCounter+1 < h {
				c2, _ := colorful.MakeColor(img.At(x, heightCounter+1))
				color2 = lipgloss.Color(c2.Hex())
			} else {
				color2 = color1
			}

			str.WriteString(lipgloss.NewStyle().Foreground(color1).
				Background(color2).Render("▀"))
		}

		str.WriteString("\n")
	}

	return str.String()
}

func ImagePreview(width int, filename string) (string, error) {
	if width <= 0 {
		return "", nil
	}
	imageContent, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer imageContent.Close()

	config, _, err := image.DecodeConfig(imageContent)
	if err != nil {
		return "", err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxImagePixels/config.Height {
		return "", fmt.Errorf("image exceeds the %d pixel limit or has invalid dimensions", maxImagePixels)
	}
	if _, err := imageContent.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	img, _, err := image.Decode(imageContent)
	if err != nil {
		return "", err
	}

	imageString := ToString(width, img)

	return imageString, nil
}
