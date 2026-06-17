package command

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
)

const generatedJPEGQuality = 92

func NormalizeGeneratedImageToJPEG(input GeneratedImage) (GeneratedImage, error) {
	if len(input.Bytes) == 0 {
		return GeneratedImage{}, fmt.Errorf("generated image is empty")
	}
	decoded, _, err := image.Decode(bytes.NewReader(input.Bytes))
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("decode generated image: %w", err)
	}
	bounds := decoded.Bounds()
	canvas := image.NewRGBA(bounds)
	draw.Draw(canvas, bounds, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, bounds, decoded, bounds.Min, draw.Over)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: generatedJPEGQuality}); err != nil {
		return GeneratedImage{}, fmt.Errorf("encode generated jpeg: %w", err)
	}
	return GeneratedImage{
		Bytes:    out.Bytes(),
		MIMEType: "image/jpeg",
	}, nil
}
