package core

import (
	"bytes"
	"encoding/binary"
	"lmtools/internal/imagetest"
	"testing"
)

func webPTestChunk(kind string, payload []byte) []byte {
	chunk := make([]byte, 8+len(payload)+len(payload)%2)
	copy(chunk, kind)
	binary.LittleEndian.PutUint32(chunk[4:], uint32(len(payload)))
	copy(chunk[8:], payload)
	return chunk
}

func webPTestContainer(chunks ...[]byte) []byte {
	data := []byte("RIFF\x00\x00\x00\x00WEBP")
	for _, chunk := range chunks {
		data = append(data, chunk...)
	}
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	return data
}

func webPTestPayload(t *testing.T, fixture []byte, kind string) []byte {
	t.Helper()
	pos := bytes.Index(fixture, []byte(kind))
	if pos < 12 || len(fixture)-pos < 8 {
		t.Fatalf("fixture has no %s chunk", kind)
	}
	size := int(binary.LittleEndian.Uint32(fixture[pos+4:]))
	if size > len(fixture)-pos-8 {
		t.Fatalf("fixture has a truncated %s chunk", kind)
	}
	return append([]byte(nil), fixture[pos+8:pos+8+size]...)
}

func TestWebPValidationRejectsDamagedHeadersAndFrames(t *testing.T) {
	// The tiny lossless and alpha fixtures were encoded by cwebp and decoded
	// independently by dwebp. Keep real codec data beneath mutated headers.
	lossy := webPTestPayload(t, imagetest.WebP, "VP8 ")
	lossless := webPTestPayload(t, imagetest.LosslessWebP, "VP8L")
	extended := webPTestPayload(t, imagetest.AlphaWebP, "VP8X")
	frameChunk := webPTestChunk("VP8 ", lossy)
	valid := webPTestContainer(frameChunk)
	badLossy := append([]byte(nil), lossy...)
	badLossy[3] = 0 // Key-frame sync code.
	badLossless := append([]byte(nil), lossless...)
	badLossless[0] = 0 // Lossless signature.
	badVersion := append([]byte(nil), lossless...)
	badVersion[4] |= 0x20
	interframe := append([]byte(nil), lossy...)
	interframe[0] |= 1
	zeroWidth := append([]byte(nil), lossy...)
	zeroWidth[6], zeroWidth[7] = 0, 0
	zeroHeight := append([]byte(nil), lossy...)
	zeroHeight[8], zeroHeight[9] = 0, 0
	oversizeCanvas := append([]byte(nil), extended...)
	for i := 4; i < 10; i++ {
		oversizeCanvas[i] = 0xff
	}
	shortRIFF := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(shortRIFF[4:], 3)
	longRIFF := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(longRIFF[4:], ^uint32(0))
	badRIFF := append([]byte(nil), valid...)
	badRIFF[0] = 0
	badForm := append([]byte(nil), valid...)
	badForm[8] = 0
	longChunk := webPTestChunk("VP8 ", nil)
	binary.LittleEndian.PutUint32(longChunk[4:], ^uint32(0))
	oddChunk := webPTestChunk("EXIF", []byte{1})
	animationHeader := append([]byte(nil), extended...)
	animationHeader[0] |= 2
	// Use one complete animation frame and corrupt only the later frame.
	frame := webPTestPayload(t, imagetest.AnimatedWebP, "ANMF")
	badFrame := append([]byte(nil), frame...)
	badFrame[16+8+3] = 0
	animation := func(frames ...[]byte) []byte {
		chunks := [][]byte{webPTestChunk("VP8X", animationHeader), webPTestChunk("ANIM", make([]byte, 6))}
		for _, frame := range frames {
			chunks = append(chunks, webPTestChunk("ANMF", frame))
		}
		return webPTestContainer(chunks...)
	}
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"short container", valid[:11]},
		{"short RIFF extent", shortRIFF},
		{"long RIFF extent", longRIFF},
		{"RIFF signature", badRIFF},
		{"WEBP signature", badForm},
		{"short chunk header", webPTestContainer([]byte("VP8 "))},
		{"long chunk extent", webPTestContainer(longChunk)},
		{"missing odd padding", webPTestContainer(frameChunk, oddChunk[:len(oddChunk)-1])},
		{"missing image data", webPTestContainer(webPTestChunk("VP8X", extended))},
		{"short lossy header", webPTestContainer(webPTestChunk("VP8 ", lossy[:9]))},
		{"lossy signature", webPTestContainer(webPTestChunk("VP8 ", badLossy))},
		{"interframe", webPTestContainer(webPTestChunk("VP8 ", interframe))},
		{"zero width", webPTestContainer(webPTestChunk("VP8 ", zeroWidth))},
		{"zero height", webPTestContainer(webPTestChunk("VP8 ", zeroHeight))},
		{"short lossless header", webPTestContainer(webPTestChunk("VP8L", lossless[:4]))},
		{"lossless signature", webPTestContainer(webPTestChunk("VP8L", badLossless))},
		{"lossless version", webPTestContainer(webPTestChunk("VP8L", badVersion))},
		{"short extended header", webPTestContainer(webPTestChunk("VP8X", extended[:9]), frameChunk)},
		{"long extended header", webPTestContainer(webPTestChunk("VP8X", append(extended, 0)), frameChunk)},
		{"extended canvas area", webPTestContainer(webPTestChunk("VP8X", oversizeCanvas), frameChunk)},
		{"extended lossy signature", webPTestContainer(webPTestChunk("VP8X", extended), webPTestChunk("VP8 ", badLossy))},
		{"extended lossless signature", webPTestContainer(webPTestChunk("VP8X", extended), webPTestChunk("VP8L", badLossless))},
		{"short animation header", animation(frame[:15])},
		{"missing animation data", animation(frame[:16])},
		{"later animation frame", animation(frame, badFrame)},
		{"nested animation frame", animation(append(append([]byte(nil), frame[:16]...), webPTestChunk("ANMF", frame)...))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateImageContainer(tt.data, "image/webp"); err == nil {
				t.Fatal("damaged WebP accepted")
			}
		})
	}
}

func TestWebPValidationPreservesUnknownChunksAndReservedFields(t *testing.T) {
	header := webPTestPayload(t, imagetest.AlphaWebP, "VP8X")
	header[0] |= 0xc1 // Reserved bits must be ignored by readers.
	header[1], header[2], header[3] = 1, 2, 3
	data := webPTestContainer(
		webPTestChunk("VP8X", header),
		webPTestChunk("META", []byte("odd")),
		webPTestChunk("VP8L", webPTestPayload(t, imagetest.LosslessWebP, "VP8L")),
	)
	data = append(data, []byte("trailing metadata")...)
	if err := validateImageContainer(data, "image/webp"); err != nil {
		t.Fatal(err)
	}
}

func TestWebPValidationAllocatesNoPixelsForLargeDimensions(t *testing.T) {
	lossy := webPTestPayload(t, imagetest.WebP, "VP8 ")
	binary.LittleEndian.PutUint16(lossy[6:8], 0x3fff)
	binary.LittleEndian.PutUint16(lossy[8:10], 0x3fff)
	lossless := webPTestPayload(t, imagetest.LosslessWebP, "VP8L")
	binary.LittleEndian.PutUint32(lossless[1:5], 0x0fffffff) // Both 14-bit dimensions minus one.
	header := webPTestPayload(t, imagetest.AnimatedWebP, "VP8X")
	header[0] |= 0x10                                  // The small lossless animation frame contains alpha.
	header[4], header[5], header[6] = 0xff, 0xff, 0xff // Width 2^24.
	header[7], header[8], header[9] = 254, 0, 0        // Height 255, within the area limit.
	frame := make([]byte, 16)
	frame[6], frame[9] = 1, 1 // A 2-by-2 frame fits inside this canvas.
	frame = append(frame, webPTestChunk("VP8L", webPTestPayload(t, imagetest.LosslessWebP, "VP8L"))...)
	animation := webPTestContainer(webPTestChunk("VP8X", header), webPTestChunk("ANIM", make([]byte, 6)), webPTestChunk("ANMF", frame))
	for _, data := range [][]byte{
		webPTestContainer(webPTestChunk("VP8 ", lossy)),
		webPTestContainer(webPTestChunk("VP8L", lossless)),
		animation,
	} {
		if err := validateImageContainer(data, "image/webp"); err != nil {
			t.Fatal(err)
		}
		if allocs := testing.AllocsPerRun(5, func() {
			if err := validateImageContainer(data, "image/webp"); err != nil {
				panic(err)
			}
		}); allocs != 0 {
			t.Fatalf("header validation allocated %g objects", allocs)
		}
	}
}
