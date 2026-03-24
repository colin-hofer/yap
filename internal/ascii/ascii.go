package ascii

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
)

// Character ramp from dark to light (10 levels).
// Reference: http://paulbourke.net/dataformats/asciiart/
const charRamp = " .:-=+*#%@"

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
}

// Convert reads an image file and returns an ASCII art string.
// width is the desired character width; height is derived from the
// image aspect ratio with a 0.5 correction for terminal characters.
// Returns an error if the path does not look like a supported image file.
func Convert(path string, width int) (string, error) {
	path = cleanPath(path)
	if strings.Contains(path, "\n") || !imageExts[strings.ToLower(filepath.Ext(path))] {
		return "", fmt.Errorf("not an image path")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open image: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}

	bounds := img.Bounds()
	imgW := bounds.Dx()
	imgH := bounds.Dy()
	if imgW == 0 || imgH == 0 {
		return "", fmt.Errorf("image has zero dimensions")
	}

	// Calculate height preserving aspect ratio.
	// Terminal chars are roughly twice as tall as wide, so halve the height.
	height := int(float64(width) * float64(imgH) / float64(imgW) * 0.5)
	if height < 1 {
		height = 1
	}

	var buf strings.Builder
	rampLen := len(charRamp)

	for y := 0; y < height; y++ {
		// Map y back to source image coordinate.
		srcY := bounds.Min.Y + y*imgH/height
		for x := 0; x < width; x++ {
			srcX := bounds.Min.X + x*imgW/width
			gray := color.GrayModel.Convert(img.At(srcX, srcY)).(color.Gray).Y
			// Map 0-255 to ramp index.
			idx := int(gray) * (rampLen - 1) / 255
			buf.WriteByte(charRamp[idx])
		}
		if y < height-1 {
			buf.WriteByte('\n')
		}
	}

	return buf.String(), nil
}

// cleanPath strips surrounding quotes and expands a leading ~ to the user's
// home directory. Terminals often quote or escape paths when drag-dropping.
func cleanPath(p string) string {
	// Strip surrounding single or double quotes.
	if len(p) >= 2 {
		if (p[0] == '\'' && p[len(p)-1] == '\'') || (p[0] == '"' && p[len(p)-1] == '"') {
			p = p[1 : len(p)-1]
		}
	}
	// Remove backslash escapes (e.g. "/path/to/my\ image.png").
	p = strings.ReplaceAll(p, "\\ ", " ")
	// Expand leading ~.
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return p
}
