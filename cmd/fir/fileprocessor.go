package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
)

// ProcessedFiles holds the result of processing @file arguments.
type ProcessedFiles struct {
	Text   string
	Images []ai.ImageContent
}

// ProcessFileArguments reads @file arguments, returning text content and images.
// Text files are included inline; image files are returned as base64-encoded images.
func ProcessFileArguments(files []string, cwd string) (*ProcessedFiles, error) {
	var textParts []string
	var images []ai.ImageContent

	for _, file := range files {
		path := file
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}

		ext := strings.ToLower(filepath.Ext(path))
		if mimeType, isImage := tools.SupportedImageExtensions[ext]; isImage {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read image %s: %w", file, err)
			}
			images = append(images, ai.ImageContent{
				Type:     "image",
				Data:     base64.StdEncoding.EncodeToString(data),
				MimeType: mimeType,
			})
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read file %s: %w", file, err)
			}
			textParts = append(textParts, fmt.Sprintf("<file path=%q>\n%s\n</file>\n", file, string(data)))
		}
	}

	return &ProcessedFiles{
		Text:   strings.Join(textParts, "\n"),
		Images: images,
	}, nil
}
