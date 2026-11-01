//go:build unit

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const tinyAvatarWebP = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"

func TestInlineAvatarRejectsOversizedDimensionsBeforeDecode(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif", "webp-vp8", "webp-vp8l", "webp-vp8x"} {
		for _, dimensions := range []struct {
			name          string
			width, height int
		}{
			{"wide", maxInlineAvatarDimension + 1, 1},
			{"tall", 1, maxInlineAvatarDimension + 1},
			{"pixel_budget", 2048, 2049},
		} {
			t.Run(format+"/"+dimensions.name, func(t *testing.T) {
				data := inlineAvatarHeader(t, format, dimensions.width, dimensions.height)
				config, _, err := image.DecodeConfig(bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, dimensions.width, config.Width)
				require.Equal(t, dimensions.height, config.Height)

				for _, size := range []int{len(data), targetAvatarBytes, targetAvatarBytes + 1} {
					t.Run(fmt.Sprint(size), func(t *testing.T) {
						payload := make([]byte, size)
						copy(payload, data)
						// These fixtures contain only headers, not large decoded images.
						// A dimension error instead of a decode error proves early rejection.
						raw := "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)
						_, err := normalizeUserAvatarInput(raw)
						require.ErrorIs(t, err, ErrAvatarDimensionsTooLarge)
						require.ErrorIs(t, ValidateUserAvatar(raw), ErrAvatarDimensionsTooLarge)

						_, _, err = compressInlineAvatar(payload)
						require.ErrorIs(t, err, ErrAvatarDimensionsTooLarge)
					})
				}
			})
		}
	}
}

func TestInlineAvatarDimensionLimits(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		wantErr       error
	}{
		{"one_pixel", 1, 1, nil},
		{"max_width", maxInlineAvatarDimension, 1, nil},
		{"max_height", 1, maxInlineAvatarDimension, nil},
		{"max_pixels", 2048, 2048, nil},
		{"zero_width", 0, 1, ErrAvatarInvalid},
		{"zero_height", 1, 0, ErrAvatarInvalid},
		{"negative_width", -1, 1, ErrAvatarInvalid},
		{"negative_height", 1, -1, ErrAvatarInvalid},
		{"overflow", int(^uint(0) >> 1), int(^uint(0) >> 1), ErrAvatarDimensionsTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, validateInlineAvatarDimensions(tt.width, tt.height), tt.wantErr)
		})
	}
}

func TestInlineAvatarRejectsInvalidAndOversizedData(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"empty", "data:image/png;base64,", ErrAvatarInvalid},
		{"invalid_base64", "data:image/png;base64,%%%", ErrAvatarInvalid},
		{"not_an_image", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("small-avatar")), ErrAvatarInvalid},
		{"truncated_header", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n")), ErrAvatarInvalid},
		{"non_image_type", "data:text/plain;base64,YQ==", ErrAvatarNotImage},
		{"svg", "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), ErrAvatarInvalid},
		{"encoded_limit_before_decode", "data:image/png;base64," + strings.Repeat("!", base64.StdEncoding.EncodedLen(maxInlineAvatarBytes)+1), ErrAvatarTooLarge},
		{"decoded_limit_with_same_base64_length", "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, maxInlineAvatarBytes+1)), ErrAvatarTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeUserAvatarInput(tt.raw)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestInlineAvatarPreservesSmallSupportedImages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var pngData, jpegData, gifData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, img))
	require.NoError(t, jpeg.Encode(&jpegData, img, nil))
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	require.NoError(t, gif.EncodeAll(&gifData, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{1, 1}}))
	webpData, err := base64.StdEncoding.DecodeString(tinyAvatarWebP)
	require.NoError(t, err)

	for _, tt := range []struct {
		format string
		data   []byte
	}{
		{"png", pngData.Bytes()},
		{"jpeg", jpegData.Bytes()},
		{"gif", gifData.Bytes()},
		{"webp", webpData},
	} {
		t.Run(tt.format, func(t *testing.T) {
			raw := "data:image/" + tt.format + ";base64," + base64.StdEncoding.EncodeToString(tt.data)
			input, err := normalizeUserAvatarInput(raw)
			require.NoError(t, err)
			require.Equal(t, raw, input.URL)
			require.Equal(t, "image/"+tt.format, input.ContentType)
			require.Equal(t, len(tt.data), input.ByteSize)
			sum := sha256.Sum256(tt.data)
			require.Equal(t, hex.EncodeToString(sum[:]), input.SHA256)
		})
	}
}

func TestInlineAvatarAcceptsExactByteLimit(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	data := make([]byte, maxInlineAvatarBytes)
	copy(data, encoded.Bytes())
	raw := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	input, err := normalizeUserAvatarInput(raw)
	require.NoError(t, err)
	require.LessOrEqual(t, input.ByteSize, targetAvatarBytes)
}

func TestInlineAvatarRejectsWebPBitstreamCanvasMismatch(t *testing.T) {
	canvas := inlineAvatarHeader(t, "webp-vp8x", 1, 1)
	for _, format := range []string{"webp-vp8", "webp-vp8l"} {
		for _, tt := range []struct {
			name    string
			width   int
			wantErr error
		}{
			{"oversized_bitstream", maxInlineAvatarDimension + 1, ErrAvatarDimensionsTooLarge},
			{"mismatched_dimensions", 2, ErrAvatarInvalid},
		} {
			t.Run(format+"/"+tt.name, func(t *testing.T) {
				bitstream := inlineAvatarHeader(t, format, tt.width, 1)
				data := append(append([]byte(nil), canvas...), bitstream[12:]...)
				binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
				config, _, err := image.DecodeConfig(bytes.NewReader(data))
				require.NoError(t, err)
				require.Equal(t, 1, config.Width)
				_, err = validateInlineAvatarImage(data)
				require.ErrorIs(t, err, tt.wantErr)
				_, _, err = compressInlineAvatar(data)
				require.ErrorIs(t, err, tt.wantErr)
			})
		}
	}
}

func TestCompressInlineAvatarBoundsOutputDimensions(t *testing.T) {
	for _, size := range []image.Point{{X: 1024, Y: 512}, {X: 512, Y: 1024}, {X: 4096, Y: 1}} {
		t.Run(fmt.Sprintf("%dx%d", size.X, size.Y), func(t *testing.T) {
			var encoded bytes.Buffer
			require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rectangle{Max: size})))
			data, mimeType, err := compressInlineAvatar(encoded.Bytes())
			require.NoError(t, err)
			require.Equal(t, "image/jpeg", mimeType)
			require.LessOrEqual(t, len(data), targetAvatarBytes)
			config, err := jpeg.DecodeConfig(bytes.NewReader(data))
			require.NoError(t, err)
			longestSide := max(size.X, size.Y)
			require.Equal(t, max(1, size.X*maxCompressedAvatarDimension/longestSide), config.Width)
			require.Equal(t, max(1, size.Y*maxCompressedAvatarDimension/longestSide), config.Height)
		})
	}
}

func TestCompressInlineAvatarRejectsWebPBeforeDecode(t *testing.T) {
	for _, format := range []string{"webp-vp8", "webp-vp8l", "webp-vp8x"} {
		t.Run(format, func(t *testing.T) {
			data := inlineAvatarHeader(t, format, 1, 1)
			if format == "webp-vp8x" {
				data[20] = 0x10 // Alpha flag.
				data = append(data, []byte{'A', 'L', 'P', 'H', 1, 0, 0, 0, 1, 0}...)
				data = append(data, inlineAvatarHeader(t, "webp-vp8", 1, 1)[12:]...)
				binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
			}
			// No pixel stream is needed: even malformed entropy data must not
			// reach the WebP decoder's unbounded Huffman workspace allocation.
			_, _, err := compressInlineAvatar(data)
			require.ErrorIs(t, err, ErrAvatarWebPTooLarge)
			padded := make([]byte, targetAvatarBytes+1)
			copy(padded, data)
			raw := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(padded)
			_, err = normalizeUserAvatarInput(raw)
			require.ErrorIs(t, err, ErrAvatarWebPTooLarge)
		})
	}
}

func TestInlineAvatarRejectsExcessiveJPEGScans(t *testing.T) {
	data := inlineAvatarHeader(t, "jpeg", 2048, 2048)
	sof := bytes.Index(data, []byte{0xff, 0xc0})
	data[sof+1] = 0xc2 // Progressive frame.
	sos := bytes.Index(data, []byte{0xff, 0xda})
	require.NotEqual(t, -1, sos)
	header := append([]byte(nil), data[:sos]...)
	scanLength := int(binary.BigEndian.Uint16(data[sos+2:sos+4])) + 2
	scan := data[sos : sos+scanLength]
	for _, count := range []int{maxInlineAvatarJPEGScans, maxInlineAvatarJPEGScans + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			payload := append(append([]byte(nil), header...), bytes.Repeat(scan, count)...)
			payload = append(payload, 0xff, 0xd9)
			_, err := validateInlineAvatarImage(payload)
			if count == maxInlineAvatarJPEGScans {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrAvatarTooComplex)
			_, _, err = compressInlineAvatar(payload)
			require.ErrorIs(t, err, ErrAvatarTooComplex)
			raw := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(payload)
			require.ErrorIs(t, ValidateUserAvatar(raw), ErrAvatarTooComplex)
		})
	}
}

func TestInlineJPEGScanCountIgnoresMetadataAndEscapedBytes(t *testing.T) {
	data := inlineAvatarHeader(t, "jpeg", 1, 1)
	metadata := bytes.Repeat([]byte{0xff, 0xda}, maxInlineAvatarJPEGScans+1)
	app := []byte{0xff, 0xe1, 0, byte(len(metadata) + 2)}
	app = append(app, metadata...)
	payload := append(append([]byte(nil), data[:2]...), app...)
	payload = append(payload, data[2:len(data)-2]...)
	payload = append(payload, bytes.Repeat([]byte{0xff, 0x00, 0xda, 0xff, 0xd0}, maxInlineAvatarJPEGScans+1)...)
	payload = append(payload, 0xff, 0xd9)
	require.NoError(t, validateInlineJPEGAvatar(payload))
	require.ErrorIs(t, validateInlineJPEGAvatar([]byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff}), ErrAvatarInvalid)
}

func TestUpdateProfileRejectsAvatarBombWithoutWrites(t *testing.T) {
	for _, size := range []int{33, targetAvatarBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := make([]byte, size)
			copy(data, inlineAvatarHeader(t, "png", maxInlineAvatarDimension+1, 1))
			raw := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
			repo := &mockUserRepo{getByIDUser: &User{ID: 7, AvatarURL: "https://example.com/old.png"}}
			svc := NewUserService(repo, nil, nil, nil)
			_, err := svc.UpdateProfile(context.Background(), 7, UpdateProfileRequest{AvatarURL: &raw})
			require.ErrorIs(t, err, ErrAvatarDimensionsTooLarge)
			require.Empty(t, repo.upsertAvatarArgs)
			require.Empty(t, repo.deleteAvatarIDs)
			require.Zero(t, repo.updateCalls)
			require.Equal(t, "https://example.com/old.png", repo.getByIDUser.AvatarURL)
		})
	}
}

func inlineAvatarHeader(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	switch format {
	case "png":
		require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
		data := encoded.Bytes()[:33]
		binary.BigEndian.PutUint32(data[16:20], uint32(width))
		binary.BigEndian.PutUint32(data[20:24], uint32(height))
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		return data
	case "jpeg":
		require.NoError(t, jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, 1, 1)), nil))
		data := encoded.Bytes()
		sof := bytes.Index(data, []byte{0xff, 0xc0})
		require.NotEqual(t, -1, sof)
		binary.BigEndian.PutUint16(data[sof+5:sof+7], uint16(height))
		binary.BigEndian.PutUint16(data[sof+7:sof+9], uint16(width))
		return data
	case "gif":
		img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
		require.NoError(t, gif.Encode(&encoded, img, nil))
		data := encoded.Bytes()
		binary.LittleEndian.PutUint16(data[6:8], uint16(width))
		binary.LittleEndian.PutUint16(data[8:10], uint16(height))
		return data
	}

	var chunk []byte
	var id string
	switch format {
	case "webp-vp8":
		id = "VP8 "
		chunk = []byte{0x10, 0, 0, 0x9d, 0x01, 0x2a, 0, 0, 0, 0}
		binary.LittleEndian.PutUint16(chunk[6:8], uint16(width))
		binary.LittleEndian.PutUint16(chunk[8:10], uint16(height))
	case "webp-vp8l":
		id = "VP8L"
		chunk = make([]byte, 5)
		chunk[0] = 0x2f
		binary.LittleEndian.PutUint32(chunk[1:], uint32(width-1)|uint32(height-1)<<14)
	case "webp-vp8x":
		id = "VP8X"
		chunk = make([]byte, 10)
		for i := range 3 {
			chunk[4+i] = byte((width - 1) >> (8 * i))
			chunk[7+i] = byte((height - 1) >> (8 * i))
		}
	default:
		t.Fatalf("unsupported fixture format %s", format)
	}
	data := make([]byte, 20+len(chunk)+len(chunk)%2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:], "WEBP")
	copy(data[12:], id)
	binary.LittleEndian.PutUint32(data[16:20], uint32(len(chunk)))
	copy(data[20:], chunk)
	return data
}
