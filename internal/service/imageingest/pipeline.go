// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package imageingest provides content-validation, decompression-bomb defense,
// resizing, and re-encoding for uploaded newsletter images. All functions are
// pure (no network/DB) and fully unit-testable.
package imageingest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Sentinel errors for image ingest validation and processing.
var (
	ErrUnsupportedType  = errors.New("unsupported image type")
	ErrTooLarge         = errors.New("image exceeds maximum byte size")
	ErrPixelCapExceeded = errors.New("image dimensions exceed maximum pixel count")
	ErrDecodeFailed     = errors.New("failed to decode image")
	ErrEncodeFailed     = errors.New("failed to encode image")
)

// Limits controls the constraints applied to image ingest.
type Limits struct {
	MaxBytes  int64 // maximum file size in bytes
	MaxPixels int64 // maximum total pixels (width * height)
	MaxWidth  int   // maximum width in pixels
}

// Result is the outcome of a successful ingest.
type Result struct {
	Hash        string // hex sha256 of the final re-encoded bytes
	Data        []byte // final re-encoded bytes
	ContentType string // canonical output content type
	Width       int    // width of the decoded image
	Height      int    // height of the decoded image
	ByteSize    int    // len(Data)
}

// Ingest validates, resizes, and re-encodes the given image data.
// The declared content type is validated against an allowlist before any
// processing. Decompression-bomb defense via DecodeConfig checks pixel count
// before full decode. Output is re-encoded (stripping EXIF/ICC metadata) and
// content-addressed by sha256 of the final bytes.
//
// Returns ErrUnsupportedType, ErrTooLarge, ErrPixelCapExceeded, or ErrDecodeFailed
// on failure; never ErrEncodeFailed (encoding the output is the final step, so
// any failure there is wrapped as an internal error).
func Ingest(data []byte, declaredContentType string, cfg Limits) (Result, error) {
	// Content-type allowlist: validate the declared type before any processing.
	if err := validateContentType(declaredContentType); err != nil {
		return Result{}, err
	}

	// Reject oversized files before decoding.
	if int64(len(data)) > cfg.MaxBytes {
		return Result{}, ErrTooLarge
	}

	// Decompression-bomb defense: read width/height from header without full decode.
	reader := bytes.NewReader(data)
	width, height, err := decodeConfigByType(reader, declaredContentType)
	if err != nil {
		return Result{}, ErrDecodeFailed
	}

	// Reject if pixel count exceeds limit.
	if int64(width)*int64(height) > cfg.MaxPixels {
		return Result{}, ErrPixelCapExceeded
	}

	// Decode the full image.
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	img, _, err := image.Decode(reader)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}

	// Resize preserving aspect ratio.
	resized := resize(img, cfg.MaxWidth)

	// Re-encode based on the input type.
	// PNG input re-encodes as PNG; JPEG and WebP re-encode as JPEG.
	var outContentType string
	outData, err := reEncode(resized, declaredContentType, &outContentType)
	if err != nil {
		return Result{}, err
	}

	// Compute sha256 of the final re-encoded bytes.
	hash := sha256.Sum256(outData)

	return Result{
		Hash:        fmt.Sprintf("%x", hash),
		Data:        outData,
		ContentType: outContentType,
		Width:       resized.Bounds().Dx(),
		Height:      resized.Bounds().Dy(),
		ByteSize:    len(outData),
	}, nil
}

// validateContentType checks if the declared type is in the allowlist.
func validateContentType(ct string) error {
	switch ct {
	case "image/png", "image/jpeg", "image/webp":
		return nil
	default:
		return ErrUnsupportedType
	}
}

// decodeConfigByType reads width/height from the image header without full
// decode, using the appropriate decoder for the declared content type.
func decodeConfigByType(r io.Reader, contentType string) (width, height int, err error) {
	switch contentType {
	case "image/png":
		cfg, err := png.DecodeConfig(r)
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	case "image/jpeg":
		cfg, err := jpeg.DecodeConfig(r)
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	case "image/webp":
		cfg, err := webp.DecodeConfig(r)
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	default:
		return 0, 0, fmt.Errorf("unsupported type for decode: %s", contentType)
	}
}

// resize scales the image so the longer dimension is capped at maxWidth,
// preserving aspect ratio. Uses CatmullRom scaling for quality.
func resize(img image.Image, maxWidth int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// If already within bounds, return as-is.
	if w <= maxWidth && h <= maxWidth {
		return img
	}

	// Calculate the scale factor based on the longer dimension.
	var scale float64
	if w > h {
		scale = float64(maxWidth) / float64(w)
	} else {
		scale = float64(maxWidth) / float64(h)
	}

	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	out := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Over, nil)
	return out
}

// reEncode re-encodes the image based on the input type.
// PNG input re-encodes as PNG; JPEG and WebP input re-encode as JPEG.
// Sets outContentType to the final content type.
func reEncode(img image.Image, inContentType string, outContentType *string) ([]byte, error) {
	buf := new(bytes.Buffer)

	switch inContentType {
	case "image/png":
		*outContentType = "image/png"
		if err := png.Encode(buf, img); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
		}
	case "image/jpeg", "image/webp":
		*outContentType = "image/jpeg"
		if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 85}); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
		}
	default:
		return nil, fmt.Errorf("unsupported type for encode: %s", inContentType)
	}

	return buf.Bytes(), nil
}
