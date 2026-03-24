package ascii

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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
