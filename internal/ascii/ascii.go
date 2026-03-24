package ascii

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const charRamp = " .:-=+*#%@"

var imageExts = map[string]bool{
	".gif":  true,
	".jpeg": true,
	".jpg":  true,
	".png":  true,
}

// Convert reads an image file and renders it as ASCII art.
func Convert(path string, width int) (string, error) {
	if width < 1 {
		return "", fmt.Errorf("width must be positive")
	}
	path = cleanPath(path)
	if strings.Contains(path, "\n") || !imageExts[strings.ToLower(filepath.Ext(path))] {
		return "", fmt.Errorf("not an image path")
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open image: %w", err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}

	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()
	if imgW == 0 || imgH == 0 {
		return "", fmt.Errorf("image has zero dimensions")
	}

	height := int(float64(width) * float64(imgH) / float64(imgW) * 0.5)
	if height < 1 {
		height = 1
	}

	var buf strings.Builder
	rampLen := len(charRamp)
	for y := 0; y < height; y++ {
		srcY := bounds.Min.Y + y*imgH/height
		for x := 0; x < width; x++ {
			srcX := bounds.Min.X + x*imgW/width
			gray := color.GrayModel.Convert(img.At(srcX, srcY)).(color.Gray).Y
			idx := int(gray) * (rampLen - 1) / 255
			buf.WriteByte(charRamp[idx])
		}
		if y < height-1 {
			buf.WriteByte('\n')
		}
	}

	return buf.String(), nil
}

// LooksLikeArt reports whether the text appears to be generated ASCII art
// using the package's luminance ramp.
func LooksLikeArt(text string) bool {
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	if len(lines) < 3 {
		return false
	}
	seenInk := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			return false
		}
		for _, r := range line {
			if !strings.ContainsRune(charRamp, r) {
				return false
			}
			if r != ' ' {
				seenInk = true
			}
		}
	}
	return seenInk
}

// Fit rescales ASCII art to fit the requested width and trims trailing spaces
// from each rendered line to avoid invisible wrap fragments in the viewport.
func Fit(text string, width int) string {
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	sourceWidth := 0
	grid := make([][]rune, len(lines))
	for i, line := range lines {
		grid[i] = []rune(line)
		if len(grid[i]) > sourceWidth {
			sourceWidth = len(grid[i])
		}
	}
	if sourceWidth == 0 {
		return ""
	}
	if width <= 0 {
		width = sourceWidth
	}
	if width > sourceWidth {
		width = sourceWidth
	}

	sourceHeight := len(grid)
	targetHeight := sourceHeight
	if width < sourceWidth {
		targetHeight = int(math.Round(float64(sourceHeight) * float64(width) / float64(sourceWidth)))
		if targetHeight < 1 {
			targetHeight = 1
		}
	}

	var out strings.Builder
	for y := 0; y < targetHeight; y++ {
		srcY := y * sourceHeight / targetHeight
		row := grid[srcY]
		var line strings.Builder
		for x := 0; x < width; x++ {
			srcX := x * sourceWidth / width
			if srcX < len(row) {
				line.WriteRune(row[srcX])
			} else {
				line.WriteByte(' ')
			}
		}
		rendered := strings.TrimRight(line.String(), " ")
		out.WriteString(rendered)
		if y < targetHeight-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if len(path) >= 2 {
		if (path[0] == '\'' && path[len(path)-1] == '\'') || (path[0] == '"' && path[len(path)-1] == '"') {
			path = path[1 : len(path)-1]
		}
	}
	path = strings.ReplaceAll(path, "\\ ", " ")
	if strings.HasPrefix(path, "file://") {
		if decoded, err := url.PathUnescape(strings.TrimPrefix(path, "file://")); err == nil {
			path = decoded
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}
