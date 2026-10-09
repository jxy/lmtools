package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
)

// validateImageContainer reads configuration and checks the complete container.
// It never allocates decoded pixels, even for large declared dimensions or
// animations. Providers remain responsible for decoding compressed pixel data.
func validateImageContainer(data []byte, mediaType string) error {
	if mediaType == "image/webp" {
		return validateWebPContainer(data)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
		// JPEG permits coding methods the Go decoder does not implement.
		// Check their frame and scan structure below without decoding pixels.
		var unsupported jpeg.UnsupportedError
		if mediaType != "image/jpeg" || !errors.As(err, &unsupported) {
			return err
		}
	}
	switch mediaType {
	case "image/png":
		return validatePNGContainer(data)
	case "image/jpeg":
		return validateJPEGContainer(data)
	case "image/gif":
		return validateGIFContainer(data)
	default:
		return fmt.Errorf("unsupported image type %q", mediaType)
	}
}

func validatePNGContainer(data []byte) error {
	var hasData bool
	for pos := 8; pos < len(data); {
		if len(data)-pos < 12 {
			return io.ErrUnexpectedEOF
		}
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		if size+12 > uint64(len(data)-pos) {
			return io.ErrUnexpectedEOF
		}
		end := pos + 8 + int(size)
		if crc32.ChecksumIEEE(data[pos+4:end]) != binary.BigEndian.Uint32(data[end:]) {
			return fmt.Errorf("png chunk %q has an invalid checksum", data[pos+4:pos+8])
		}
		switch string(data[pos+4 : pos+8]) {
		case "IDAT":
			hasData = true
		case "IEND":
			if size != 0 || !hasData {
				return fmt.Errorf("invalid PNG end chunk")
			}
			return nil
		}
		pos = end + 4
	}
	return io.ErrUnexpectedEOF
}

func validateJPEGContainer(data []byte) error {
	var hasFrame, hasScan bool
	var needsHeight bool
	for pos := 2; pos < len(data); {
		if data[pos] != 0xff {
			return fmt.Errorf("invalid JPEG marker")
		}
		for pos < len(data) && data[pos] == 0xff {
			pos++
		}
		if pos == len(data) {
			return io.ErrUnexpectedEOF
		}
		marker := data[pos]
		pos++
		if marker == 0xd9 { // End of image; trailing bytes are not raster data.
			if !hasFrame || !hasScan || needsHeight {
				return fmt.Errorf("jpeg requires an image frame and scan")
			}
			return nil
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if len(data)-pos < 2 {
			return io.ErrUnexpectedEOF
		}
		size := int(binary.BigEndian.Uint16(data[pos:]))
		if size < 2 || size > len(data)-pos {
			return io.ErrUnexpectedEOF
		}
		if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
			// Start-of-frame markers share precision, dimensions, and a
			// component table. A zero height may be supplied later by DNL.
			if needsHeight || size < 8 || data[pos+2] == 0 || data[pos+2] > 16 ||
				binary.BigEndian.Uint16(data[pos+5:pos+7]) == 0 ||
				data[pos+7] == 0 || size != 8+3*int(data[pos+7]) {
				return fmt.Errorf("invalid JPEG frame header")
			}
			hasFrame = true
			needsHeight = binary.BigEndian.Uint16(data[pos+3:pos+5]) == 0
		}
		if marker == 0xda && (!hasFrame || size < 6 || data[pos+2] == 0 || size != 6+2*int(data[pos+2])) {
			return fmt.Errorf("invalid JPEG scan header")
		}
		pos += size
		if marker == 0xda { // Scan data escapes 0xff and has restart markers.
			hasScan = true
			for pos < len(data) {
				if data[pos] != 0xff {
					pos++
					continue
				}
				if pos+1 == len(data) {
					return io.ErrUnexpectedEOF
				}
				next := data[pos+1]
				if next == 0xdc { // DNL supplies a deferred height within a scan.
					if len(data)-pos < 6 {
						return io.ErrUnexpectedEOF
					}
					if !needsHeight || binary.BigEndian.Uint16(data[pos+2:pos+4]) != 4 ||
						binary.BigEndian.Uint16(data[pos+4:pos+6]) == 0 {
						return fmt.Errorf("invalid JPEG line count")
					}
					needsHeight = false
					pos += 6
					continue
				}
				if next != 0 && (next < 0xd0 || next > 0xd7) {
					break
				}
				pos += 2
			}
		}
	}
	return io.ErrUnexpectedEOF
}

func validateGIFContainer(data []byte) error {
	if len(data) < 13 {
		return io.ErrUnexpectedEOF
	}
	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 << ((data[10] & 7) + 1) // Global color table.
	}
	var hasFrame bool
	for pos < len(data) {
		kind := data[pos]
		pos++
		switch kind {
		case 0x3b: // Trailer; tolerate trailing metadata as decoders do.
			if !hasFrame {
				return fmt.Errorf("gif contains no image frames")
			}
			return nil
		case 0x21: // Extension label, followed by data sub-blocks.
			pos++
		case 0x2c: // Image descriptor and optional local color table.
			if len(data)-pos < 9 {
				return io.ErrUnexpectedEOF
			}
			flags := data[pos+8]
			pos += 9
			if flags&0x80 != 0 {
				pos += 3 << ((flags & 7) + 1)
			}
			pos++ // LZW minimum code size.
			hasFrame = true
		default:
			return fmt.Errorf("invalid GIF block 0x%02x", kind)
		}
		for {
			if pos >= len(data) {
				return io.ErrUnexpectedEOF
			}
			size := int(data[pos])
			pos++
			if size > len(data)-pos {
				return io.ErrUnexpectedEOF
			}
			pos += size
			if size == 0 {
				break
			}
		}
	}
	return io.ErrUnexpectedEOF
}

// WebP's RIFF length excludes the first eight bytes. Keep trailing metadata
// outside that extent, as the other image container checks do.
func validateWebPContainer(data []byte) error {
	if len(data) < 12 {
		return io.ErrUnexpectedEOF
	}
	if string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return fmt.Errorf("invalid WebP container header")
	}
	size := uint64(binary.LittleEndian.Uint32(data[4:8]))
	if size < 4 || size+8 > uint64(len(data)) {
		return io.ErrUnexpectedEOF
	}
	return validateWebPChunks(data[12:8+int(size)], true)
}

// WebP frames are RIFF chunks too. Check every codec header and the lengths of
// nested animation chunks without decoding or allocating pixel buffers.
// Header layouts: https://developers.google.com/speed/webp/docs/riff_container
func validateWebPChunks(data []byte, allowAnimation bool) error {
	var hasFrame, hasExtendedHeader bool
	for len(data) > 0 {
		if len(data) < 8 {
			return io.ErrUnexpectedEOF
		}
		size := uint64(binary.LittleEndian.Uint32(data[4:]))
		paddedSize := size + size%2
		if paddedSize+8 > uint64(len(data)) {
			return io.ErrUnexpectedEOF
		}
		payload := data[8 : 8+int(size)]
		switch string(data[:4]) {
		case "VP8 ", "VP8L":
			if err := validateWebPFrameHeader(string(data[:4]), payload); err != nil {
				return err
			}
			hasFrame = true
		case "VP8X":
			if !allowAnimation || hasExtendedHeader || len(payload) != 10 {
				return fmt.Errorf("invalid WebP extended header")
			}
			// Reserved fields are ignored by readers. Dimensions use 24-bit
			// values offset by one, with a maximum canvas area of 2^32-1.
			width, height := uint64(webPUint24(payload[4:7]))+1, uint64(webPUint24(payload[7:10]))+1
			if width*height > uint64(^uint32(0)) {
				return fmt.Errorf("invalid WebP canvas dimensions")
			}
			hasExtendedHeader = true
		case "ALPH":
			if allowAnimation && !hasExtendedHeader {
				return fmt.Errorf("WebP alpha requires an extended header")
			}
		case "ANMF":
			if !allowAnimation || !hasExtendedHeader || len(payload) < 16 {
				return fmt.Errorf("invalid WebP animation frame")
			}
			if err := validateWebPChunks(payload[16:], false); err != nil {
				return err
			}
			hasFrame = true
		}
		data = data[8+int(paddedSize):]
	}
	if !hasFrame {
		return fmt.Errorf("webp contains no image frames")
	}
	return nil
}

// VP8 has a ten-byte key-frame header (RFC 6386 section 9.1). VP8L has a
// five-byte signature, dimensions, alpha hint, and version header. Compressed
// pixel data is left to the provider, as it is for the other image formats.
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
func validateWebPFrameHeader(kind string, data []byte) error {
	if kind == "VP8L" {
		if len(data) < 5 {
			return io.ErrUnexpectedEOF
		}
		// Both dimensions are 14-bit values offset by one. Only version
		// zero is defined; the alpha bit is an informational hint.
		if data[0] != 0x2f || data[4]&0xe0 != 0 {
			return fmt.Errorf("invalid WebP lossless frame header")
		}
		return nil
	}
	if len(data) < 10 {
		return io.ErrUnexpectedEOF
	}
	if data[0]&1 != 0 || data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a ||
		binary.LittleEndian.Uint16(data[6:8])&0x3fff == 0 || binary.LittleEndian.Uint16(data[8:10])&0x3fff == 0 {
		return fmt.Errorf("invalid WebP lossy frame header")
	}
	return nil
}

func webPUint24(data []byte) uint32 {
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
}
