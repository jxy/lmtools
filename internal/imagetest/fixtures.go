// Package imagetest provides complete image fixtures for offline tests.
package imagetest

import _ "embed"

var (
	//go:embed testdata/valid.png
	PNG []byte
	//go:embed testdata/valid.jpg
	JPEG []byte
	//go:embed testdata/arithmetic.jpg
	ArithmeticJPEG []byte
	//go:embed testdata/valid.gif
	GIF []byte
	//go:embed testdata/valid.webp
	WebP []byte
	//go:embed testdata/lossless.webp
	LosslessWebP []byte
	//go:embed testdata/alpha.webp
	AlphaWebP []byte
	//go:embed testdata/animated.gif
	AnimatedGIF []byte
	//go:embed testdata/animated.webp
	AnimatedWebP []byte
	//go:embed testdata/truncated.png
	TruncatedPNG []byte
)
