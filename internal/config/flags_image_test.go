package config

import (
	"bytes"
	"flag"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	testPNGBytes  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	testJPEGBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0}
)

func writeImageFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestParseFlagsImageAttachesFilesInOrder(t *testing.T) {
	png := writeImageFixture(t, "first.png", testPNGBytes)
	jpg := writeImageFixture(t, "second.jpg", testJPEGBytes)

	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-image", png, "-image", jpg, "-image-detail", "High"})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if got := cfg.ImagePaths; len(got) != 2 || got[0] != png || got[1] != jpg {
		t.Fatalf("ImagePaths = %v, want [%s %s]", got, png, jpg)
	}
	if len(cfg.Images) != 2 {
		t.Fatalf("Images = %d entries, want 2", len(cfg.Images))
	}

	wantNames := []string{"first.png", "second.jpg"}
	wantTypes := []string{"image/png", "image/jpeg"}
	for i, image := range cfg.Images {
		if image.Name != wantNames[i] {
			t.Errorf("Images[%d].Name = %q, want %q", i, image.Name, wantNames[i])
		}
		if image.Detail != "high" {
			t.Errorf("Images[%d].Detail = %q, want high (lowercased)", i, image.Detail)
		}
		mediaType, _, err := core.ImageBlockSize(image)
		if err != nil || mediaType != wantTypes[i] {
			t.Errorf("Images[%d] media type = %q, %v; want %q", i, mediaType, err, wantTypes[i])
		}
	}

	opts := cfg.RequestOptions()
	if len(opts.Images) != 2 || opts.Images[0] != cfg.Images[0] || opts.Images[1] != cfg.Images[1] {
		t.Fatalf("RequestOptions().Images = %#v, want the loaded images", opts.Images)
	}
	opts.Images[0].Name = "changed"
	if cfg.Images[0].Name == "changed" {
		t.Fatal("RequestOptions() shares its Images slice with the config")
	}
}

func TestParseFlagsWithoutImageLeavesImagesEmpty(t *testing.T) {
	cfg, err := ParseFlags([]string{"-argo-user", "testuser"})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if len(cfg.Images) != 0 || len(cfg.RequestOptions().Images) != 0 {
		t.Fatalf("Images = %#v, want none", cfg.Images)
	}
}

func TestParseFlagsImageRejections(t *testing.T) {
	png := writeImageFixture(t, "ok.png", testPNGBytes)
	text := writeImageFixture(t, "notes.png", []byte("not an image"))

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing file",
			args: []string{"-image", filepath.Join(t.TempDir(), "missing.png")},
			want: "-image: read image",
		},
		{
			name: "not an image",
			args: []string{"-image", text},
			want: "content is not " + core.SupportedImageMediaTypesText,
		},
		{
			name: "empty path",
			args: []string{"-image", ""},
			want: "image path cannot be empty",
		},
		{
			name: "detail without image",
			args: []string{"-image-detail", "high"},
			want: "-image-detail requires -image",
		},
		{
			name: "invalid detail",
			args: []string{"-image", png, "-image-detail", "huge"},
			want: "-image-detail must be one of: auto, low, high",
		},
		{
			name: "embed mode",
			args: []string{"-e", "-image", png},
			want: "-image is only supported in chat mode",
		},
		{
			name: "legacy argo",
			args: []string{"-argo-legacy", "-image", png},
			want: "-image is not supported with -argo-legacy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-argo-user", "testuser"}, tt.args...)
			_, err := ParseFlags(args)
			if err == nil {
				t.Fatalf("ParseFlags(%v) error = nil, want %q", args, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseFlags(%v) error = %q, want it to contain %q", args, err, tt.want)
			}
		})
	}
}

func TestParseFlagsImageDetailAcceptsEachHint(t *testing.T) {
	png := writeImageFixture(t, "ok.png", testPNGBytes)
	for _, detail := range []string{"auto", "low", "high", " Low "} {
		cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-image", png, "-image-detail", detail})
		if err != nil {
			t.Fatalf("ParseFlags(-image-detail %q) error = %v", detail, err)
		}
		if want := strings.ToLower(strings.TrimSpace(detail)); cfg.Images[0].Detail != want {
			t.Errorf("Detail = %q, want %q", cfg.Images[0].Detail, want)
		}
	}
}

func TestImageFlagHelpAndDefaults(t *testing.T) {
	var cfg Config
	fs := flag.NewFlagSet("lmc", flag.ContinueOnError)
	registerFlags(fs, &cfg)

	image := fs.Lookup("image")
	if image == nil {
		t.Fatal("flag -image is not registered")
	}
	for _, text := range []string{"repeatable", "PNG", "WebP"} {
		if !strings.Contains(image.Usage, text) {
			t.Errorf("-image help = %q, want it to mention %q", image.Usage, text)
		}
	}
	if detail := fs.Lookup("image-detail"); detail == nil || !strings.Contains(detail.Usage, "auto, low, high") {
		t.Errorf("-image-detail help missing or incomplete: %+v", detail)
	}

	// PrintDefaults calls String on a zero flag value to decide whether to
	// print a default; the repeatable flag must survive that with no paths.
	var out bytes.Buffer
	fs.SetOutput(&out)
	fs.PrintDefaults()
	if !strings.Contains(out.String(), "-image value") {
		t.Errorf("PrintDefaults() = %q, want it to list -image", out.String())
	}
	if (imagePathsFlag{}).String() != "" {
		t.Error("zero imagePathsFlag.String() should be empty")
	}
}
