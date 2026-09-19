package core

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Minimal byte prefixes that net/http's content sniffer classifies as each
// image type. LoadImageFile trusts the sniffer, never the file name.
var (
	pngBytes  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0}
	gifBytes  = []byte("GIF89a\x00\x00")
	webpBytes = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	bmpBytes  = []byte("BM\x00\x00\x00\x00\x00\x00")
)

func writeTestImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestDetectImageMediaType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
		ok   bool
	}{
		{name: "png", data: pngBytes, want: "image/png", ok: true},
		{name: "jpeg", data: jpegBytes, want: "image/jpeg", ok: true},
		{name: "gif", data: gifBytes, want: "image/gif", ok: true},
		{name: "webp", data: webpBytes, want: "image/webp", ok: true},
		{name: "bmp is an image the providers do not take", data: bmpBytes},
		{name: "text", data: []byte("hello, world")},
		{name: "empty", data: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DetectImageMediaType(tt.data)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("DetectImageMediaType() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestImageDataURLRoundTrips(t *testing.T) {
	url := ImageDataURL("image/png", pngBytes)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("ImageDataURL() = %q, want data:image/png;base64, prefix", url)
	}
	mediaType, data, ok := ParseBase64DataURL(url)
	if !ok || mediaType != "image/png" {
		t.Fatalf("ParseBase64DataURL(%q) = %q, %v", url, mediaType, ok)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(decoded) != string(pngBytes) {
		t.Fatalf("payload round trip = %v, want %v", decoded, pngBytes)
	}
}

func TestLoadImageFileReadsSniffsAndNames(t *testing.T) {
	// The extension lies on purpose: the sniffed type is what is sent.
	path := writeTestImage(t, "shot.png", jpegBytes)

	block, err := LoadImageFile(path, 1024)
	if err != nil {
		t.Fatalf("LoadImageFile() error = %v", err)
	}
	if block.Name != "shot.png" {
		t.Errorf("Name = %q, want shot.png", block.Name)
	}
	if block.Detail != "" {
		t.Errorf("Detail = %q, want empty; the flag layer sets it", block.Detail)
	}
	if want := ImageDataURL("image/jpeg", jpegBytes); block.URL != want {
		t.Errorf("URL = %q, want %q", block.URL, want)
	}
	mediaType, size, err := ImageBlockSize(block)
	if err != nil || mediaType != "image/jpeg" || size != len(jpegBytes) {
		t.Errorf("ImageBlockSize() = %q, %d, %v; want image/jpeg, %d, nil", mediaType, size, err, len(jpegBytes))
	}
}

func TestLoadImageFileExactlyAtLimitIsAccepted(t *testing.T) {
	path := writeTestImage(t, "edge.png", pngBytes)
	if _, err := LoadImageFile(path, len(pngBytes)); err != nil {
		t.Fatalf("LoadImageFile() at the limit error = %v", err)
	}
}

func TestLoadImageFileWithoutLimitReadsWholeFile(t *testing.T) {
	path := writeTestImage(t, "unbounded.png", pngBytes)
	block, err := LoadImageFile(path, 0)
	if err != nil {
		t.Fatalf("LoadImageFile() error = %v", err)
	}
	if _, size, _ := ImageBlockSize(block); size != len(pngBytes) {
		t.Fatalf("size = %d, want %d", size, len(pngBytes))
	}
}

func TestLoadImageFileRejections(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name     string
		path     string
		maxBytes int
		want     string
	}{
		{name: "missing", path: filepath.Join(dir, "missing.png"), maxBytes: 1024, want: "read image"},
		{name: "directory", path: dir, maxBytes: 1024, want: "not a regular file"},
		{name: "empty", path: writeTestImage(t, "empty.png", nil), maxBytes: 1024, want: "file is empty"},
		{name: "text", path: writeTestImage(t, "notes.png", []byte("just text")), maxBytes: 1024, want: "content is not " + SupportedImageMediaTypesText},
		{name: "bmp", path: writeTestImage(t, "bitmap.bmp", bmpBytes), maxBytes: 1024, want: "content is not"},
		{name: "over limit", path: writeTestImage(t, "big.png", pngBytes), maxBytes: len(pngBytes) - 1, want: "larger than the " + FormatByteCount(len(pngBytes)-1) + " limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadImageFile(tt.path, tt.maxBytes)
			if err == nil {
				t.Fatalf("LoadImageFile(%q) error = nil, want %q", tt.path, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadImageFile(%q) error = %q, want it to contain %q", tt.path, err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.path) {
				t.Errorf("error %q does not name the path %q", err, tt.path)
			}
		})
	}
}

func TestLoadImageFileOverLimitDoesNotReadPastIt(t *testing.T) {
	// A file one byte over the cap is refused on the count alone, before the
	// sniffer sees it: the error names the limit, not the content.
	big := append(append([]byte(nil), pngBytes...), make([]byte, 100)...)
	path := writeTestImage(t, "big.png", big)
	_, err := LoadImageFile(path, len(big)-1)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("LoadImageFile() error = %v, want limit error", err)
	}
}

func TestBase64DecodedSize(t *testing.T) {
	for size := 0; size < 12; size++ {
		raw := make([]byte, size)
		padded := base64.StdEncoding.EncodeToString(raw)
		if got := base64DecodedSize(padded); got != size {
			t.Errorf("base64DecodedSize(padded %d bytes) = %d", size, got)
		}
		unpadded := base64.RawStdEncoding.EncodeToString(raw)
		if got := base64DecodedSize(unpadded); got != size {
			t.Errorf("base64DecodedSize(unpadded %d bytes) = %d", size, got)
		}
	}
}

func TestImageBlockSizeRejectsURLImages(t *testing.T) {
	_, _, err := ImageBlockSize(ImageBlock{URL: "https://example.com/a.png"})
	if err == nil {
		t.Fatal("ImageBlockSize() error = nil for a URL image")
	}
}

func TestDescribeImageBlock(t *testing.T) {
	data := ImageDataURL("image/png", pngBytes)
	tests := []struct {
		name  string
		block ImageBlock
		want  string
	}{
		{name: "named data URL", block: ImageBlock{URL: data, Name: "shot.png"}, want: "shot.png (image/png, 12 bytes)"},
		{name: "anonymous data URL", block: ImageBlock{URL: data}, want: "image/png, 12 bytes"},
		{name: "remote URL", block: ImageBlock{URL: "https://example.com/a.png"}, want: "https://example.com/a.png"},
		{name: "named remote URL", block: ImageBlock{URL: "https://example.com/a.png", Name: "a.png"}, want: "a.png (https://example.com/a.png)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DescribeImageBlock(tt.block); got != tt.want {
				t.Fatalf("DescribeImageBlock() = %q, want %q", got, tt.want)
			}
			if strings.Contains(DescribeImageBlock(tt.block), "base64,") {
				t.Fatal("DescribeImageBlock() leaked the payload")
			}
		})
	}
}

func TestUserMessageBlocksPutsImagesBeforeText(t *testing.T) {
	first := ImageBlock{URL: ImageDataURL("image/png", pngBytes), Name: "1.png"}
	second := ImageBlock{URL: ImageDataURL("image/gif", gifBytes), Name: "2.gif", Detail: "low"}

	blocks := UserMessageBlocks("what are these?", []ImageBlock{first, second})
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	if got, ok := blocks[0].(ImageBlock); !ok || got != first {
		t.Errorf("blocks[0] = %#v, want %#v", blocks[0], first)
	}
	if got, ok := blocks[1].(ImageBlock); !ok || got != second {
		t.Errorf("blocks[1] = %#v, want %#v", blocks[1], second)
	}
	if got, ok := blocks[2].(TextBlock); !ok || got.Text != "what are these?" {
		t.Errorf("blocks[2] = %#v, want the prompt text", blocks[2])
	}
}

func TestUserMessageBlocksOmitsEmptyText(t *testing.T) {
	image := ImageBlock{URL: ImageDataURL("image/png", pngBytes)}
	blocks := UserMessageBlocks("", []ImageBlock{image})
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want the image alone", len(blocks))
	}
	if _, ok := blocks[0].(ImageBlock); !ok {
		t.Fatalf("blocks[0] = %#v, want ImageBlock", blocks[0])
	}
	if got := UserMessageBlocks("", nil); len(got) != 0 {
		t.Fatalf("UserMessageBlocks(\"\", nil) = %#v, want none", got)
	}
}

func TestNewUserMessageMatchesNewTextMessageWithoutImages(t *testing.T) {
	got := NewUserMessage("hello", nil)
	want := NewTextMessage(string(RoleUser), "hello")
	if got.Role != want.Role || len(got.Blocks) != 1 || got.Blocks[0] != want.Blocks[0] {
		t.Fatalf("NewUserMessage() = %#v, want %#v", got, want)
	}
}
