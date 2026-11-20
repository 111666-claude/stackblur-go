// Variable (per-pixel) radius StackBlur driven by a radius map.
//
// A radius map is a same-sized image whose per-pixel luminance selects the
// blur radius: black means "keep the original pixel", white means "blur with
// the configured maximum radius", and intermediate values interpolate
// smoothly between the two. Fractional radii are resolved by blending the
// StackBlur results of the two bracketing integer radii, so a continuously
// varying radius map does not produce visible steps, while integer radii
// remain bit-identical to the fixed-radius Process API.

package stackblur

import (
	"errors"
	"image"
	"image/color"
	"math"
)

const (
	// MaxRadiusMapRadius is the largest blur radius accepted by ProcessRadiusMap.
	// It matches the highest index usable by the StackBlur multiplication/shift
	// lookup tables (which cover radii 0..254).
	MaxRadiusMapRadius float64 = 254

	// radiusMapLevels is the number of discrete sub-integer steps per radius
	// unit. Each per-pixel effective radius is quantized to 1/radiusMapLevels
	// of a pixel and the results of the two bracketing integer-radius blurs are
	// linearly interpolated, eliminating visible steps at radius transitions.
	radiusMapLevels = 1 << 16

	// maxRadiusMapScratchBytes caps the temporary pixel storage ProcessRadiusMap
	// is allowed to allocate: the quantized per-pixel radius field plus the
	// retained integer-radius blur levels.
	maxRadiusMapScratchBytes = 256 << 20 // 256 MiB
)

// Sentinel errors returned by ProcessRadiusMap.
var (
	// ErrNilSourceImage is returned when the source image is nil.
	ErrNilSourceImage = errors.New("the source image cannot be nil")
	// ErrNilRadiusMap is returned when the radius map is nil.
	ErrNilRadiusMap = errors.New("the radius map cannot be nil")
	// ErrNilDestinationImage is returned when the destination image is nil.
	ErrNilDestinationImage = errors.New("the destination image cannot be nil")
	// ErrEmptySourceImage is returned when the source image bounds are empty.
	ErrEmptySourceImage = errors.New("the source image must have a non-empty bounds rectangle")
	// ErrSizeMismatch is returned when the radius map dimensions differ from the source image dimensions.
	ErrSizeMismatch = errors.New("the radius map must have the same dimensions as the source image")
	// ErrDestinationSize is returned when the destination image dimensions differ from the source image dimensions.
	ErrDestinationSize = errors.New("the destination image must have the same dimensions as the source image")
	// ErrInvalidMaxRadius is returned when the maximum radius is zero, negative or non-finite.
	ErrInvalidMaxRadius = errors.New("the maximum blur radius must be a finite number greater than 0")
	// ErrMaxRadiusTooLarge is returned when the maximum radius exceeds MaxRadiusMapRadius.
	ErrMaxRadiusTooLarge = errors.New("the maximum blur radius exceeds the supported upper limit (254)")
	// ErrScratchLimitExceeded is returned when the required temporary storage exceeds the resource limit.
	ErrScratchLimitExceeded = errors.New("the blur would allocate more scratch memory than the allowed 256MiB limit")
)

// ProcessRadiusMap applies a spatially varying StackBlur to src and writes the
// result into dst, which must be a non-nil *image.NRGBA of the same size as
// src.
//
// radiusMap must have exactly the same dimensions as src. Its luminance at each
// pixel (fully transparent pixels read as black) scales maxRadius:
//
//	effectiveRadius(x,y) = maxRadius * luminance(radiusMap(x,y)) / 255
//
// Pixels mapped to radius 0 are copied from the source untouched. Fractional
// radii are rendered by linearly interpolating between the StackBlur outputs
// of the two surrounding integer radii, applied identically to R, G, B and A,
// which keeps semi-transparent edges consistent and avoids steps at radius
// transitions. An effective radius that is an exact integer produces exactly
// the same output as Process with that radius.
//
// maxRadius must be finite, positive and at most MaxRadiusMapRadius. The
// function allocates at most maxRadiusMapScratchBytes of temporary storage and
// returns ErrScratchLimitExceeded before allocating when the image and radius
// would exceed that budget.
func ProcessRadiusMap(dst *image.NRGBA, src image.Image, radiusMap image.Image, maxRadius float64) error {
	if math.IsNaN(maxRadius) || math.IsInf(maxRadius, 0) || maxRadius <= 0 {
		return ErrInvalidMaxRadius
	}
	if maxRadius > MaxRadiusMapRadius {
		return ErrMaxRadiusTooLarge
	}
	if src == nil {
		return ErrNilSourceImage
	}
	if radiusMap == nil {
		return ErrNilRadiusMap
	}
	if dst == nil {
		return ErrNilDestinationImage
	}

	srcBounds := src.Bounds()
	w, h := srcBounds.Dx(), srcBounds.Dy()
	if w <= 0 || h <= 0 {
		return ErrEmptySourceImage
	}

	mapBounds := radiusMap.Bounds()
	if mapBounds.Dx() != w || mapBounds.Dy() != h {
		return ErrSizeMismatch
	}

	dstBounds := dst.Bounds()
	if dstBounds.Dx() != w || dstBounds.Dy() != h {
		return ErrDestinationSize
	}

	pixelCount := uint64(w) * uint64(h)

	// Even the quantized per-pixel radius field alone must fit the budget; this
	// rejects pathological dimensions before any allocation takes place.
	if pixelCount*4 > maxRadiusMapScratchBytes {
		return ErrScratchLimitExceeded
	}

	// Quantize the per-pixel effective radius once, while scanning the map for
	// its extrema so all-zero / all-full maps can take cheap fast paths.
	scale := maxRadius * float64(radiusMapLevels) / 255.0
	fullQuant := uint32(math.Round(maxRadius * float64(radiusMapLevels)))

	radii := make([]uint32, pixelCount)
	allZero, allFull := true, true
	var maxQuant uint32

	switch rm := radiusMap.(type) {
	case *image.Gray:
		minX, minY := mapBounds.Min.X, mapBounds.Min.Y
		i := 0
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				q := quantizedRadius(scale, rm.GrayAt(minX+x, minY+y).Y)
				radii[i] = q
				i++
				if q != 0 {
					allZero = false
				}
				if q != fullQuant {
					allFull = false
				}
				if q > maxQuant {
					maxQuant = q
				}
			}
		}
	case *image.Gray16:
		minX, minY := mapBounds.Min.X, mapBounds.Min.Y
		i := 0
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				q := quantizedRadius(scale, uint8(rm.Gray16At(minX+x, minY+y).Y>>8))
				radii[i] = q
				i++
				if q != 0 {
					allZero = false
				}
				if q != fullQuant {
					allFull = false
				}
				if q > maxQuant {
					maxQuant = q
				}
			}
		}
	default:
		minX, minY := mapBounds.Min.X, mapBounds.Min.Y
		i := 0
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				lum := color.GrayModel.Convert(rm.At(minX+x, minY+y)).(color.Gray).Y
				q := quantizedRadius(scale, lum)
				radii[i] = q
				i++
				if q != 0 {
					allZero = false
				}
				if q != fullQuant {
					allFull = false
				}
				if q > maxQuant {
					maxQuant = q
				}
			}
		}
	}

	// Fast path: the whole map requests no blur, so the source is preserved.
	if allZero {
		convertToNRGBA(dst, src)
		return nil
	}

	// Highest integer blur level that must be retained: level k holds the
	// StackBlur result for integer radius k. A pixel with fraction f needs both
	// level k and level k+1.
	topLevel := int(maxQuant / radiusMapLevels)
	if maxQuant%radiusMapLevels != 0 {
		topLevel++
	}

	// Fast path: a uniform map at an exact integer radius is the fixed-radius
	// API, which only needs a single image-sized scratch buffer.
	uniformInteger := allFull && fullQuant%radiusMapLevels == 0
	if uniformInteger {
		if pixelCount*8 > maxRadiusMapScratchBytes {
			return ErrScratchLimitExceeded
		}
		process(dst, src, fullQuant/radiusMapLevels)
		return nil
	}

	// The level pyramid retains every integer-radius blur at once. Reject
	// over-budget requests before allocating any of it: radii (4 bytes/px) plus
	// levels 0..topLevel (4 bytes/px each).
	scratch := pixelCount*4 + uint64(topLevel+1)*pixelCount*4
	if scratch > maxRadiusMapScratchBytes {
		return ErrScratchLimitExceeded
	}

	base := image.NewNRGBA(image.Rect(0, 0, w, h))
	convertToNRGBA(base, src)

	levels := make([]*image.NRGBA, topLevel+1)
	levels[0] = base
	for r := 1; r <= topLevel; r++ {
		levels[r] = image.NewNRGBA(base.Bounds())
		copy(levels[r].Pix, base.Pix)
		blurImage(levels[r], uint32(r))
	}

	di := dst.PixOffset(dstBounds.Min.X, dstBounds.Min.Y)
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			q := radii[i]
			level := q >> 16
			frac := q & 0xffff
			li := i * 4

			if frac == 0 {
				lo := levels[level].Pix
				dst.Pix[di] = lo[li]
				dst.Pix[di+1] = lo[li+1]
				dst.Pix[di+2] = lo[li+2]
				dst.Pix[di+3] = lo[li+3]
			} else {
				lo := levels[level].Pix
				hi := levels[level+1].Pix
				for c := 0; c < 4; c++ {
					low := uint32(lo[li+c])
					high := uint32(hi[li+c])
					// Interpolate all four channels with the same weight. The
					// maximum intermediate value is well within uint32 range.
					dst.Pix[di+c] = uint8((low*(radiusMapLevels-frac) + high*frac + (radiusMapLevels >> 1)) >> 16)
				}
			}
			di += 4
			i++
		}
		di += dst.Stride - w*4
	}

	return nil
}

// quantizedRadius maps a radius-map luminance value to an effective radius
// expressed in 1/radiusMapLevels units.
func quantizedRadius(scale float64, luminance uint8) uint32 {
	if luminance == 0 {
		return 0
	}
	return uint32(math.Round(scale * float64(luminance)))
}
