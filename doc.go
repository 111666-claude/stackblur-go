/*
stackblur-go is a Go port of the Stackblur algorithm.

Stackblur is a compromise between Gaussian blur and Box blur, but it creates much better looking blurs than Box blur and it is ~7x faster than Gaussian blur.

The usage of the API is very simple: it exposes a single public `Process` function which requires a destination and a source image together with a blur radius. The blured image will be encoded into the destination image.

	func Process(dst, src image.Image, radius uint32) error

Below is a very simple example of how you can use this package.

	package main

	import (
		"image"
		"image/jpeg"
		"log"
		"os"

		"github.com/esimov/stackblur-go"
	)

	func main() {
		var radius uint32 = 5
		f, err := os.Open("sample.png")
		if err != nil {
			log.Fatalf("could not open source file: %v", err)
		}
		defer f.Close()

		src, _, err := image.Decode(f)
		if err != nil {
			log.Fatalf("could not decode source file: %v", err)
		}

		dst := image.NewNRGBA(src.Bounds())
		err = stackblur.Process(dst, src, radius)
		if err != nil {
			log.Fatal(err)
		}

		output, err := os.OpenFile("output.jpg", os.O_CREATE|os.O_RDWR, 0755)
		if err != nil {
			log.Fatalf("could not open destination file: %v", err)
		}
		defer output.Close()

		if err = jpeg.Encode(output, dst, &jpeg.Options{Quality: 100}); err != nil {
			log.Fatalf("could not encode destination image: %v", err)
		}
	}

## Radius-map (variable radius) blur

ProcessRadiusMap blurs each pixel with its own radius taken from a same-sized
radius map. The map luminance scales the configured maximum radius: black keeps
the original pixel, white applies the full radius, and intermediate values are
interpolated smoothly. This is useful for depth-of-field effects where the
foreground stays sharp and the background fades into blur.

	func ProcessRadiusMap(dst *image.NRGBA, src image.Image, radiusMap image.Image, maxRadius float64) error

  - radiusMap must match the source image dimensions; its luminance is used as
    effectiveRadius = maxRadius * luminance / 255.
  - Radius 0 regions preserve the source pixels exactly.
  - Fractional radii are resolved by blending the two bracketing integer-radius
    StackBlur results with the same weight on R, G, B and A, so semi-transparent
    edges stay consistent and radius transitions show no steps.
  - maxRadius must be finite, positive and at most MaxRadiusMapRadius (254).
    Invalid inputs return the documented sentinel errors; temporary storage is
    capped at 256 MiB and ErrScratchLimitExceeded is returned before the cap is
    breached.
  - The fixed-radius Process API is unchanged.
*/
package stackblur
