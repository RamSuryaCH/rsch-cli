package image

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestImagePreviewRejectsOversizedDimensionsBeforeDecodingPixels(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	header := data.Bytes()[:33]
	binary.BigEndian.PutUint32(header[16:20], 50_000)
	binary.BigEndian.PutUint32(header[20:24], 50_000)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
	path := filepath.Join(t.TempDir(), "oversized.png")
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImagePreview(80, path); err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("ImagePreview() error = %v, want pixel limit error before pixel decoding", err)
	}
}

func TestToStringBoundsExtremeAspectRatios(t *testing.T) {
	for _, tc := range []struct {
		name   string
		width  int
		bounds image.Rectangle
	}{
		{name: "tall", width: 80, bounds: image.Rect(0, 0, 1, 2000)},
		{name: "wide viewport", width: 100_000, bounds: image.Rect(0, 0, 100, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := ToString(tc.width, image.NewRGBA(tc.bounds))
			if output == "" {
				t.Fatal("nonempty image produced an empty preview")
			}
			lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
			if len(lines) > 512 {
				t.Fatalf("preview produced %d lines, want at most 512", len(lines))
			}
			for _, line := range lines {
				if width := ansi.StringWidth(line); width > min(tc.width, 512) {
					t.Fatalf("preview width = %d, exceeds bounded viewport", width)
				}
			}
		})
	}
}

func TestToStringHandlesEmptyInput(t *testing.T) {
	if got := ToString(80, nil); got != "" {
		t.Fatalf("nil image rendered %q", got)
	}
	if got := ToString(0, image.NewRGBA(image.Rect(0, 0, 2, 2))); got != "" {
		t.Fatalf("zero-width preview rendered %q", got)
	}
}
