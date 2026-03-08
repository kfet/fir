// Ported from: packages/coding-agent/src/utils/image-resize.ts
// Upstream hash: 1caadb2e
package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"

	// Register decoders for common formats
	_ "image/gif"

	_ "golang.org/x/image/webp"

	"golang.org/x/image/draw"
)

const (
	defaultMaxWidth  = 2000
	defaultMaxHeight = 2000
	defaultMaxBytes  = 4.5 * 1024 * 1024 // 4.5 MB, below Anthropic's 5MB limit
	defaultJPEGQual  = 80
)

// ResizedImage contains the result of an image resize operation.
type ResizedImage struct {
	Data           string // base64
	MimeType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
	WasResized     bool
}

// ResizeImageOptions configures image resizing.
type ResizeImageOptions struct {
	MaxWidth    int
	MaxHeight   int
	MaxBytes    int
	JPEGQuality int
}

func (o *ResizeImageOptions) withDefaults() ResizeImageOptions {
	out := ResizeImageOptions{
		MaxWidth:    defaultMaxWidth,
		MaxHeight:   defaultMaxHeight,
		MaxBytes:    int(defaultMaxBytes),
		JPEGQuality: defaultJPEGQual,
	}
	if o != nil {
		if o.MaxWidth > 0 {
			out.MaxWidth = o.MaxWidth
		}
		if o.MaxHeight > 0 {
			out.MaxHeight = o.MaxHeight
		}
		if o.MaxBytes > 0 {
			out.MaxBytes = o.MaxBytes
		}
		if o.JPEGQuality > 0 {
			out.JPEGQuality = o.JPEGQuality
		}
	}
	return out
}

// ResizeImage resizes an image to fit within the max dimensions and file size.
// Returns the original image unchanged if it already fits.
func ResizeImage(b64Data, mimeType string, options *ResizeImageOptions) ResizedImage {
	opts := options.withDefaults()

	raw, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return ResizedImage{Data: b64Data, MimeType: mimeType}
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return ResizedImage{Data: b64Data, MimeType: mimeType}
	}

	bounds := img.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	// Check if already within all limits
	if origW <= opts.MaxWidth && origH <= opts.MaxHeight && len(raw) <= opts.MaxBytes {
		return ResizedImage{
			Data:           b64Data,
			MimeType:       mimeType,
			OriginalWidth:  origW,
			OriginalHeight: origH,
			Width:          origW,
			Height:         origH,
			WasResized:     false,
		}
	}

	// Calculate target dimensions
	targetW, targetH := origW, origH
	if targetW > opts.MaxWidth {
		targetH = int(math.Round(float64(targetH) * float64(opts.MaxWidth) / float64(targetW)))
		targetW = opts.MaxWidth
	}
	if targetH > opts.MaxHeight {
		targetW = int(math.Round(float64(targetW) * float64(opts.MaxHeight) / float64(targetH)))
		targetH = opts.MaxHeight
	}

	// Try both PNG and JPEG, pick smaller
	qualitySteps := []int{opts.JPEGQuality, 70, 55, 40}
	scaleSteps := []float64{1.0, 0.75, 0.5, 0.35, 0.25}

	for _, scale := range scaleSteps {
		w := int(math.Round(float64(targetW) * scale))
		h := int(math.Round(float64(targetH) * scale))
		if w < 100 || h < 100 {
			break
		}

		for _, quality := range qualitySteps {
			resized := resizeToFit(img, w, h)

			bestData, bestMime, err := encodeBest(resized, quality)
			if err != nil {
				continue
			}
			if len(bestData) <= opts.MaxBytes {
				return ResizedImage{
					Data:           base64.StdEncoding.EncodeToString(bestData),
					MimeType:       bestMime,
					OriginalWidth:  origW,
					OriginalHeight: origH,
					Width:          w,
					Height:         h,
					WasResized:     true,
				}
			}
		}
	}

	// Last resort: return whatever we got at smallest size
	resized := resizeToFit(img, int(math.Round(float64(targetW)*0.25)), int(math.Round(float64(targetH)*0.25)))
	bestData, bestMime, _ := encodeBest(resized, 40)
	finalW := resized.Bounds().Dx()
	finalH := resized.Bounds().Dy()

	return ResizedImage{
		Data:           base64.StdEncoding.EncodeToString(bestData),
		MimeType:       bestMime,
		OriginalWidth:  origW,
		OriginalHeight: origH,
		Width:          finalW,
		Height:         finalH,
		WasResized:     true,
	}
}

// FormatDimensionNote returns a note about the resize for the model.
func FormatDimensionNote(r ResizedImage) string {
	if !r.WasResized || r.Width == 0 {
		return ""
	}
	scale := float64(r.OriginalWidth) / float64(r.Width)
	return fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]",
		r.OriginalWidth, r.OriginalHeight, r.Width, r.Height, scale)
}

// resizeToFit resizes an image using Lanczos resampling.
func resizeToFit(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// encodeBest encodes in both PNG and JPEG and returns the smaller result.
func encodeBest(img image.Image, jpegQuality int) ([]byte, string, error) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil, "", fmt.Errorf("png encode: %w", err)
	}

	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, "", fmt.Errorf("jpeg encode: %w", err)
	}

	if pngBuf.Len() <= jpegBuf.Len() {
		return pngBuf.Bytes(), "image/png", nil
	}
	return jpegBuf.Bytes(), "image/jpeg", nil
}
