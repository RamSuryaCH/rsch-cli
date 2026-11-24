package visual

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeRejectsOversizedDimensionsBeforeDecodingPixels(t *testing.T) {
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
	for name, analyze := range map[string]func() (Report, error){
		"file":  func() (Report, error) { return Analyze(path, "") },
		"bytes": func() (Report, error) { return AnalyzeBytes(header) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := analyze(); err == nil || !strings.Contains(err.Error(), "pixel limit") {
				t.Fatalf("analyze error = %v, want pixel limit error before pixel decoding", err)
			}
		})
	}
}

func TestDominantColorsBreaksTiesByEveryChannel(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 12, 1))
	want := make([]Color, 8)
	for x := range 12 {
		img.SetRGBA(x, 0, color.RGBA{R: 10, G: 20, B: uint8(x), A: 255})
		if x < len(want) {
			want[x] = Color{R: 10, G: 20, B: uint8(x), Pixels: 1}
		}
	}
	for range 20 {
		if got := dominantColors(img); !reflect.DeepEqual(got, want) {
			t.Fatalf("dominantColors() = %+v, want stable RGB order %+v", got, want)
		}
	}
}

func TestAnalyzeBytesReportsImageEvidence(t *testing.T) {
	var buffer bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{G: 255, A: 255})
		}
	}
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	report, err := AnalyzeBytes(buffer.Bytes())
	if err != nil || report.Path != "<memory>" || report.Width != 2 || report.Height != 2 || len(report.Dominant) == 0 {
		t.Fatalf("unexpected in-memory visual report: %+v %v", report, err)
	}
}

func TestAnalyzeReportsImageEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "login.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	file.Close()
	report, err := Analyze(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != "png" || report.Width != 4 || report.Height != 3 || len(report.Dominant) == 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}
