package service

import (
	"bytes"
	"image"

	"golang.org/x/image/riff"
	"golang.org/x/image/vp8"
	"golang.org/x/image/vp8l"
	_ "golang.org/x/image/webp"
)

const (
	maxInlineAvatarDimension     = 4096
	maxInlineAvatarPixels        = 4 * 1024 * 1024
	maxCompressedAvatarDimension = 512
	maxInlineAvatarJPEGScans     = 32
)

func validateInlineAvatarImage(data []byte) (string, error) {
	if len(data) > maxInlineAvatarBytes {
		return "", ErrAvatarTooLarge
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", ErrAvatarInvalid
	}
	if err := validateInlineAvatarDimensions(config.Width, config.Height); err != nil {
		return "", err
	}

	switch format {
	case "jpeg":
		return format, validateInlineJPEGAvatar(data)
	case "png", "gif":
		return format, nil
	case "webp":
		return format, validateInlineWebPAvatar(data, config)
	default:
		return "", ErrAvatarNotImage
	}
}

func validateInlineAvatarDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return ErrAvatarInvalid
	}
	if width > maxInlineAvatarDimension || height > maxInlineAvatarDimension || width > maxInlineAvatarPixels/height {
		return ErrAvatarDimensionsTooLarge
	}
	return nil
}

func validateInlineJPEGAvatar(data []byte) error {
	// Progressive scans can revisit every coefficient without increasing the
	// decoded dimensions, so the pixel budget alone does not bound CPU work.
	scans := 0
	for offset := 2; offset < len(data); {
		if data[offset] != 0xff {
			offset++
			continue
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			break
		}
		marker := data[offset]
		offset++
		if marker == 0x00 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if marker == 0xd9 {
			if scans == 0 {
				return ErrAvatarInvalid
			}
			return nil
		}
		if marker == 0xd8 || len(data)-offset < 2 {
			return ErrAvatarInvalid
		}
		length := int(data[offset])<<8 | int(data[offset+1])
		if length < 2 || length > len(data)-offset {
			return ErrAvatarInvalid
		}
		if marker == 0xda {
			scans++
			if scans > maxInlineAvatarJPEGScans {
				return ErrAvatarTooComplex
			}
		}
		offset += length
	}
	return ErrAvatarInvalid
}

func validateInlineWebPAvatar(data []byte, canvas image.Config) error {
	// WebP DecodeConfig can return the VP8X canvas size before inspecting the
	// independently sized VP8/VP8L bitstream that Decode actually allocates.
	_, chunks, err := riff.NewReader(bytes.NewReader(data))
	if err != nil {
		return ErrAvatarInvalid
	}
	for {
		id, size, chunk, err := chunks.Next()
		if err != nil || size > uint32(len(data)) {
			return ErrAvatarInvalid
		}

		var width, height int
		switch id {
		case riff.FourCC{'V', 'P', '8', ' '}:
			decoder := vp8.NewDecoder()
			decoder.Init(chunk, int(size))
			header, err := decoder.DecodeFrameHeader()
			if err != nil {
				return ErrAvatarInvalid
			}
			width, height = header.Width, header.Height
		case riff.FourCC{'V', 'P', '8', 'L'}:
			config, err := vp8l.DecodeConfig(chunk)
			if err != nil {
				return ErrAvatarInvalid
			}
			width, height = config.Width, config.Height
		default:
			continue
		}

		if err := validateInlineAvatarDimensions(width, height); err != nil {
			return err
		}
		if width != canvas.Width || height != canvas.Height {
			return ErrAvatarInvalid
		}
		return nil
	}
}
