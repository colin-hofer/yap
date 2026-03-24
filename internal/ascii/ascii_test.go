package ascii

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertRendersImageAtRequestedWidth(t *testing.T) {
	t.Parallel()

	path := writeTestPNG(t, "sample.png", 2, 2, color.Gray{Y: 255})
	art, err := Convert(path, 4)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if art != "@@@@\n@@@@" {
		t.Fatalf("Convert() = %q, want %q", art, "@@@@\n@@@@")
	}
}

func TestConvertRejectsNonImagePath(t *testing.T) {
	t.Parallel()

	if _, err := Convert("/tmp/not-an-image.txt", 40); err == nil {
		t.Fatal("Convert() error = nil, want rejection for non-image path")
	}
}

func TestCleanPathUnwrapsQuotedEscapedAndFileURIPaths(t *testing.T) {
	t.Parallel()

	if got := cleanPath(`"file:///tmp/My%20Image.png"`); got != "/tmp/My Image.png" {
		t.Fatalf("cleanPath() = %q, want %q", got, "/tmp/My Image.png")
	}
}

func TestLooksLikeArt(t *testing.T) {
	t.Parallel()

	art := "@@@\n***\n..."
	if !LooksLikeArt(art) {
		t.Fatalf("LooksLikeArt() = false, want true")
	}
	if LooksLikeArt("hello\nworld\nagain") {
		t.Fatalf("LooksLikeArt() = true, want false")
	}
}

func TestFitRescalesArtAndTrimsTrailingSpaces(t *testing.T) {
	t.Parallel()

	art := strings.Join([]string{
		"@@@@    ",
		"####    ",
		"****    ",
		"....    ",
	}, "\n")
	got := Fit(art, 4)
	want := "@@\n**"
	if got != want {
		t.Fatalf("Fit() = %q, want %q", got, want)
	}
}

func writeTestPNG(t *testing.T, name string, width int, height int, fill color.Gray) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetGray(x, y, fill)
		}
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return path
}
