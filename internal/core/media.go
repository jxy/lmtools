package core

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func ParseBase64DataURL(raw string) (mediaType string, data string, ok bool) {
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", "", false
	}

	comma := strings.Index(raw, ",")
	if comma <= len("data:") || comma == len(raw)-1 {
		return "", "", false
	}

	meta := raw[len("data:"):comma]
	parts := strings.Split(meta, ";")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", false
	}

	hasBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(part, "base64") {
			hasBase64 = true
			break
		}
	}
	if !hasBase64 {
		return "", "", false
	}

	return parts[0], raw[comma+1:], true
}

// supportedImageMediaTypes is the set of image media types -image attaches.
// It is the intersection of what the providers document: Anthropic, OpenAI,
// and Google all accept PNG, JPEG, GIF, and WebP.
var supportedImageMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// SupportedImageMediaTypesText names the accepted image types for flag help
// and error text, so the two cannot list different sets.
const SupportedImageMediaTypesText = "PNG, JPEG, GIF, or WebP"

// DetectImageMediaType sniffs the content and reports its media type when it
// is one lmc accepts. The file name is never consulted: the provider validates
// the bytes, and a .png holding a JPEG is sent as the JPEG it is.
func DetectImageMediaType(data []byte) (string, bool) {
	mediaType := http.DetectContentType(data)
	if idx := strings.Index(mediaType, ";"); idx >= 0 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	if !supportedImageMediaTypes[mediaType] {
		return "", false
	}
	return mediaType, true
}

// ImageDataURL encodes image bytes as the base64 data URL an ImageBlock
// carries; every renderer already understands that form.
func ImageDataURL(mediaType string, data []byte) string {
	return Base64DataURL(mediaType, base64.StdEncoding.EncodeToString(data))
}

// Base64DataURL assembles a data URL from a media type and an already
// base64-encoded payload, the inverse of ParseBase64DataURL.
func Base64DataURL(mediaType, encoded string) string {
	return "data:" + mediaType + ";base64," + encoded
}

// AnthropicImageSourceURL is the URL an ImageBlock carries for an Anthropic
// image source: the url of a url source, or a data URL assembled from a
// base64 source's media type and data. Reading only the url field turned
// every base64 image a client sent into an empty block.
func AnthropicImageSourceURL(sourceType, url, mediaType, data string) string {
	if strings.EqualFold(sourceType, "base64") || (url == "" && data != "") {
		return Base64DataURL(mediaType, data)
	}
	return url
}

// ImageTooLargeError reports a file over the caller's byte cap. It is a type
// rather than text so a tool result can add advice a flag error has no use
// for: the model can shrink the file, the operator would just pick another.
type ImageTooLargeError struct {
	Limit int
}

func (e ImageTooLargeError) Error() string {
	return fmt.Sprintf("larger than the %s limit", FormatByteCount(e.Limit))
}

// LoadImageFile reads one -image argument into an ImageBlock carrying a data
// URL. It accepts a regular file of at most maxBytes whose content sniffs as a
// supported image type, and records the base name so a session listing can
// say which file was attached. The read is bounded by maxBytes rather than by
// a size the file reported beforehand, so a file that grows after the check
// cannot be read whole.
func LoadImageFile(path string, maxBytes int) (ImageBlock, error) {
	file, err := os.Open(path)
	if err != nil {
		return ImageBlock{}, fmt.Errorf("read image %q: %w", path, err)
	}
	defer file.Close()

	return ReadImageFile(file, path, maxBytes)
}

// ReadImageFile is the half of LoadImageFile after the open: the regular-file
// check on the descriptor, the bounded read, and the sniff. It is separate so
// the tool path can open the file under its own rules and still read it under
// the flag's.
func ReadImageFile(file *os.File, path string, maxBytes int) (ImageBlock, error) {
	info, err := file.Stat()
	if err != nil {
		return ImageBlock{}, fmt.Errorf("read image %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return ImageBlock{}, fmt.Errorf("read image %q: not a regular file", path)
	}

	var reader io.Reader = file
	if maxBytes > 0 {
		reader = io.LimitReader(file, int64(maxBytes)+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return ImageBlock{}, fmt.Errorf("read image %q: %w", path, err)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return ImageBlock{}, fmt.Errorf("read image %q: %w", path, ImageTooLargeError{Limit: maxBytes})
	}
	if len(data) == 0 {
		return ImageBlock{}, fmt.Errorf("read image %q: file is empty", path)
	}

	mediaType, ok := DetectImageMediaType(data)
	if !ok {
		return ImageBlock{}, fmt.Errorf("read image %q: content is not %s", path, SupportedImageMediaTypesText)
	}

	return ImageBlock{
		URL:  ImageDataURL(mediaType, data),
		Name: filepath.Base(path),
	}, nil
}

// ErrNotDataURL reports an ImageBlock whose URL is not a base64 data URL.
var ErrNotDataURL = errors.New("image URL is not a base64 data URL")

// ImageBlockSize reports the media type and decoded byte size of a data URL
// image without decoding it.
func ImageBlockSize(block ImageBlock) (mediaType string, size int, err error) {
	mediaType, data, ok := ParseBase64DataURL(block.URL)
	if !ok {
		return "", 0, ErrNotDataURL
	}
	return mediaType, base64DecodedSize(data), nil
}

// DescribeImageBlock renders an image for an operator's eyes: the attached
// file name when one was recorded, then the media type and decoded size. A
// URL image is shown as its URL. The bytes themselves are never printed.
func DescribeImageBlock(block ImageBlock) string {
	detail := block.URL
	if mediaType, size, err := ImageBlockSize(block); err == nil {
		detail = mediaType + ", " + FormatByteCount(size)
	}
	if block.Name == "" {
		return detail
	}
	return block.Name + " (" + detail + ")"
}

// base64DecodedSize computes the byte count a base64 string decodes to. Every
// full group of four characters carries three bytes; a trailing group of two
// or three characters carries one or two, whether or not it was padded.
func base64DecodedSize(data string) int {
	trimmed := strings.TrimRight(data, "=")
	size := len(trimmed) / 4 * 3
	switch len(trimmed) % 4 {
	case 2:
		size++
	case 3:
		size += 2
	}
	return size
}
