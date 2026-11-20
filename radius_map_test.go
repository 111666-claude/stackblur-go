package stackblur

import (
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

const testW, testH = 64, 48

// uniformGrayMap builds a radius map filled with one luminance value.
func uniformGrayMap(w, h int, y uint8) *image.Gray {
	m := image.NewGray(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = y
	}
	return m
}

func newTestImage(w, h int) *image.NRGBA {
	return image.NewNRGBA(image.Rect(0, 0, w, h))
}

// TestProcessRadiusMapErrors covers every documented validation branch and
// checks that invalid inputs fail before any pixel processing.
func TestProcessRadiusMapErrors(t *testing.T) {
	src := newTestImage(testW, testH)
	dst := newTestImage(testW, testH)
	rm := uniformGrayMap(testW, testH, 128)
	otherSize := uniformGrayMap(testW+1, testH, 128)
	emptySrc := image.NewNRGBA(image.Rect(0, 0, 0, testH))

	tests := []struct {
		name    string
		dst     *image.NRGBA
		src     image.Image
		rm      image.Image
		maxR    float64
		wantErr error
	}{
		{"zero max radius", dst, src, rm, 0, ErrInvalidMaxRadius},
		{"negative max radius", dst, src, rm, -5, ErrInvalidMaxRadius},
		{"NaN max radius", dst, src, rm, math.NaN(), ErrInvalidMaxRadius},
		{"+Inf max radius", dst, src, rm, math.Inf(1), ErrInvalidMaxRadius},
		{"-Inf max radius", dst, src, rm, math.Inf(-1), ErrInvalidMaxRadius},
		{"max radius too large", dst, src, rm, 255, ErrMaxRadiusTooLarge},
		{"huge max radius", dst, src, rm, 1e9, ErrMaxRadiusTooLarge},
		{"nil source", dst, nil, rm, 10, ErrNilSourceImage},
		{"nil radius map", dst, src, nil, 10, ErrNilRadiusMap},
		{"nil destination", nil, src, rm, 10, ErrNilDestinationImage},
		{"empty source", dst, emptySrc, uniformGrayMap(0, testH, 128), 10, ErrEmptySourceImage},
		{"size mismatch width", dst, src, otherSize, 10, ErrSizeMismatch},
		{"size mismatch height", dst, src, uniformGrayMap(testW, testH+1, 128), 10, ErrSizeMismatch},
		{"destination size mismatch", newTestImage(testW, testH-1), src, rm, 10, ErrDestinationSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ProcessRadiusMap(tt.dst, tt.src, tt.rm, tt.maxR)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestScratchLimit verifies the resource cap is enforced without allocating
// the full level pyramid: one oversized image and one reasonable image at a
// radius whose pyramid would exceed the budget.
func TestScratchLimit(t *testing.T) {
	// Dimensions whose radius field alone exceeds 256 MiB.
	hugeW, hugeH := 9000, 9000
	src := image.NewNRGBA(image.Rect(0, 0, hugeW, hugeH))
	rm := image.NewGray(image.Rect(0, 0, hugeW, hugeH))
	dst := image.NewNRGBA(image.Rect(0, 0, hugeW, hugeH))
	if err := ProcessRadiusMap(dst, src, rm, 10); !errors.Is(err, ErrScratchLimitExceeded) {
		t.Fatalf("expected ErrScratchLimitExceeded for huge image, got %v", err)
	}

	// Moderate image but radius 254 with a non-uniform map requires the full
	// level pyramid: (255+1) * pixels * 4 must exceed the cap for any image
	// larger than ~262144 pixels.
	w, h := 1024, 1024
	src2 := newTestImage(w, h)
	rm2 := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := uint8((x * 255) / (w - 1))
		for y := 0; y < h; y++ {
			rm2.SetGray(x, y, color.Gray{Y: v})
		}
	}
	dst2 := newTestImage(w, h)
	if err := ProcessRadiusMap(dst2, src2, rm2, 254); !errors.Is(err, ErrScratchLimitExceeded) {
		t.Fatalf("expected ErrScratchLimitExceeded for over-budget pyramid, got %v", err)
	}

	// A uniform map at an exact integer radius takes the single-buffer fast
	// path, so the same dimensions and radius must succeed there.
	uniform := uniformGrayMap(w, h, 255)
	dst3 := newTestImage(w, h)
	if err := ProcessRadiusMap(dst3, src2, uniform, 254); err != nil {
		t.Fatalf("uniform integer-radius fast path failed unexpectedly: %v", err)
	}

	// The single-buffer path still has a resource cap.
	bigW, bigH := 8192, 8192
	bigSrc := image.NewNRGBA(image.Rect(0, 0, bigW, bigH))
	bigDst := image.NewNRGBA(image.Rect(0, 0, bigW, bigH))
	if err := ProcessRadiusMap(bigDst, bigSrc, uniformGrayMap(bigW, bigH, 255), 10); !errors.Is(err, ErrScratchLimitExceeded) {
		t.Fatalf("expected ErrScratchLimitExceeded for oversized single-buffer path, got %v", err)
	}
}

// TestAllZeroMapPreservesSource: every pixel of an all-zero radius map must
// equal the converted source pixel, including semi-transparent ones.
func TestAllZeroMapPreservesSource(t *testing.T) {
	w, h := testW, testH
	src := newTestImage(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := src.PixOffset(x, y)
			src.Pix[i] = uint8((x * 7) % 256)
			src.Pix[i+1] = uint8((y * 11) % 256)
			src.Pix[i+2] = uint8((x + y) % 256)
			src.Pix[i+3] = uint8((x * y) % 256)
		}
	}
	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, uniformGrayMap(w, h, 0), 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pixEqual(src.Pix, dst.Pix) {
		t.Fatal("all-zero radius map did not preserve the source pixels")
	}
}

// TestFullMapMatchesFixedRadius: a white map at an integer max radius must be
// bit-identical to the fixed-radius Process API output.
func TestFullMapMatchesFixedRadius(t *testing.T) {
	w, h := testW, testH
	src := newTestImage(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := src.PixOffset(x, y)
			src.Pix[i] = uint8((x*13 + y*5) % 256)
			src.Pix[i+1] = uint8((x*3 + y*17) % 256)
			src.Pix[i+2] = uint8((x*y + 19) % 256)
			src.Pix[i+3] = uint8(64 + (x%4)*64)
		}
	}

	for _, radius := range []uint32{1, 2, 5, 17, 40} {
		expected := newTestImage(w, h)
		if err := Process(expected, src, radius); err != nil {
			t.Fatalf("Process failed: %v", err)
		}
		got := newTestImage(w, h)
		if err := ProcessRadiusMap(got, src, uniformGrayMap(w, h, 255), float64(radius)); err != nil {
			t.Fatalf("ProcessRadiusMap failed: %v", err)
		}
		if !pixEqual(expected.Pix, got.Pix) {
			t.Fatalf("radius %d: radius-map output differs from fixed-radius output", radius)
		}
	}
}

// TestSinglePixelRadiusIslands: pixels mapped to radius 0 inside a fully
// blurred image must keep their exact source value.
func TestSinglePixelRadiusIslands(t *testing.T) {
	w, h := testW, testH
	src := newTestImage(w, h)
	for i := range src.Pix {
		src.Pix[i] = uint8((i*37 + 11) % 256)
		if i%4 == 3 {
			src.Pix[i] = 255
		}
	}
	rm := uniformGrayMap(w, h, 255)
	rm.Pix[rm.PixOffset(10, 7)] = 0
	rm.Pix[rm.PixOffset(3, 30)] = 0

	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 8); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, p := range []image.Point{{10, 7}, {3, 30}} {
		si := src.PixOffset(p.X, p.Y)
		di := dst.PixOffset(p.X, p.Y)
		for c := 0; c < 4; c++ {
			if src.Pix[si+c] != dst.Pix[di+c] {
				t.Fatalf("zero-radius island at %v not preserved (channel %d)", p, c)
			}
		}
	}
}

// TestFractionalRadiusInterpolation: a map luminance resolving to an exact
// fractional radius must match a direct interpolation of the two bracketing
// integer-radius blurs, channel by channel including alpha.
func TestFractionalRadiusInterpolation(t *testing.T) {
	w, h := testW, testH
	src := newTestImage(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := src.PixOffset(x, y)
			src.Pix[i] = uint8((x*29 + y*7) % 256)
			src.Pix[i+1] = uint8((y*23 + 3) % 256)
			src.Pix[i+2] = uint8((x*y*13 + 5) % 256)
			src.Pix[i+3] = uint8(32 + (x%8)*28)
		}
	}

	const maxRadius = 4.0
	// Luminance 64 => effective radius 4 * 64/255 = 1.0039...
	rm := uniformGrayMap(w, h, 64)
	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, maxRadius); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lo := newTestImage(w, h)
	hi := newTestImage(w, h)
	if err := Process(lo, src, 1); err != nil {
		t.Fatal(err)
	}
	if err := Process(hi, src, 2); err != nil {
		t.Fatal(err)
	}

	const levels = 65536
	quant := uint32(math.Round(maxRadius * levels / 255.0 * 64))
	frac := quant & 0xffff
	for i := 0; i < len(dst.Pix); i++ {
		low := uint32(lo.Pix[i])
		high := uint32(hi.Pix[i])
		want := uint8((low*(levels-frac) + high*frac + (levels >> 1)) >> 16)
		if dst.Pix[i] != want {
			t.Fatalf("pixel %d: interpolated value %d, expected %d (lo=%d hi=%d frac=%d)", i, dst.Pix[i], want, lo.Pix[i], hi.Pix[i], frac)
		}
	}
}

// TestSmoothRadiusNoSteps: with a linearly increasing radius map, the blurred
// output must not contain abrupt steps that cannot be explained by the source
// content. We blur a perfectly uniform source so any non-monotone jump in an
// output column is an artifact of the radius handling.
func TestSmoothRadiusNoSteps(t *testing.T) {
	w, h := 128, 16
	src := newTestImage(w, h)
	// Uniform semi-transparent source: same color and alpha everywhere.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := src.PixOffset(x, y)
			src.Pix[i] = 200
			src.Pix[i+1] = 100
			src.Pix[i+2] = 50
			src.Pix[i+3] = 128
		}
	}
	rm := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := uint8((x * 255) / (w - 1))
		for y := 0; y < h; y++ {
			rm.Pix[rm.PixOffset(x, y)] = v
		}
	}

	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 16); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A uniform field blurred with any radius stays exactly uniform; every
	// row must be constant across x and identical to the source, and the
	// transitions between integer radii must not perturb any channel.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := dst.PixOffset(x, y)
			if dst.Pix[i] != 200 || dst.Pix[i+1] != 100 || dst.Pix[i+2] != 50 || dst.Pix[i+3] != 128 {
				t.Fatalf("uniform field changed at (%d,%d): %v", x, y, dst.Pix[i:i+4])
			}
		}
	}
}

// TestLinearMapSmoothTransitions uses a point-source on a black background so
// the blur genuinely spreads. Adjacent columns with adjacent radii must differ
// smoothly: the second difference of the center-row value stays bounded, i.e.
// there is no single-pixel step at integer-radius boundaries.
func TestLinearMapSmoothTransitions(t *testing.T) {
	w, h := 96, 33
	src := newTestImage(w, h)
	ci := src.PixOffset(w/2, h/2)
	src.Pix[ci] = 255
	src.Pix[ci+1] = 255
	src.Pix[ci+2] = 255
	src.Pix[ci+3] = 255

	rm := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := uint8((x * 255) / (w - 1))
		for y := 0; y < h; y++ {
			rm.Pix[rm.PixOffset(x, y)] = v
		}
	}

	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	centerY := h / 2
	values := make([]int, w)
	for x := 0; x < w; x++ {
		values[x] = int(dst.Pix[dst.PixOffset(x, centerY)])
	}

	// Locate integer-radius boundaries in the first half (away from the point
	// source at center) and ensure no single transition is larger than 3 LSB,
	// which would indicate a visible step between blended levels.
	for x := 2; x < w/2-1; x++ {
		step := absInt(values[x+1] - values[x])
		if step > 3 {
			t.Fatalf("abrupt step of %d at x=%d (values %d -> %d)", step, x, values[x], values[x+1])
		}
	}
}

// TestSemiTransparentEdges: alpha must be blurred with exactly the same
// per-pixel radius and interpolation weight as the color channels.
func TestSemiTransparentEdges(t *testing.T) {
	w, h := testW, testH
	src := newTestImage(w, h)
	// Opaque red block in the middle, fully transparent elsewhere.
	for y := h/2 - 8; y < h/2+8; y++ {
		for x := testW/2 - 8; x < testW/2+8; x++ {
			i := src.PixOffset(x, y)
			src.Pix[i] = 255
			src.Pix[i+3] = 255
		}
	}

	rm := uniformGrayMap(w, h, 200)
	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Recompute the expected output from the two bracketing fixed-radius blurs
	// and compare every channel, alpha included.
	const levels = 65536
	quant := uint32(math.Round(10.0 * levels / 255.0 * 200))
	level := quant >> 16
	frac := quant & 0xffff

	lo := newTestImage(w, h)
	hi := newTestImage(w, h)
	if err := Process(lo, src, level); err != nil {
		t.Fatal(err)
	}
	if err := Process(hi, src, level+1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(dst.Pix); i++ {
		low := uint32(lo.Pix[i])
		high := uint32(hi.Pix[i])
		want := uint8((low*(levels-frac) + high*frac + (levels >> 1)) >> 16)
		if dst.Pix[i] != want {
			t.Fatalf("channel byte %d: got %d want %d", i, dst.Pix[i], want)
		}
	}

	// The edge of the block must show partial alpha values (blur reached it),
	// and fully transparent input corners must remain transparent.
	corner := dst.PixOffset(0, 0)
	if dst.Pix[corner+3] != 0 {
		t.Fatalf("transparent corner gained alpha %d", dst.Pix[corner+3])
	}
	edgeAlpha := dst.Pix[dst.PixOffset(testW/2-9, h/2)+3]
	if edgeAlpha == 0 || edgeAlpha == 255 {
		t.Fatalf("expected a partial alpha at the soft edge, got %d", edgeAlpha)
	}
}

// TestTransparentMapPixelsAreZeroRadius: an NRGBA radius map whose pixels are
// fully transparent must resolve to radius 0 regardless of their RGB channels.
func TestTransparentMapPixelsAreZeroRadius(t *testing.T) {
	w, h := testW, testH
	src := newTestImage(w, h)
	for i := range src.Pix {
		src.Pix[i] = uint8((i*41 + 7) % 256)
		if i%4 == 3 {
			src.Pix[i] = 255
		}
	}
	rm := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(rm.Pix); i += 4 {
		rm.Pix[i] = 255 // bright RGB...
		rm.Pix[i+3] = 0 // ...but fully transparent
	}
	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pixEqual(src.Pix, dst.Pix) {
		t.Fatal("transparent radius-map pixels did not resolve to radius 0")
	}
}

// TestBlackPointDecay: a white point on black background blurred harder and
// harder must lose peak intensity monotonically, while total energy stays
// bounded and is never created.
func TestBlackPointDecay(t *testing.T) {
	w, h := 41, 41
	center := image.Point{20, 20}

	peaks := make([]int, 0, 6)
	for _, y := range []uint8{0, 51, 102, 153, 204, 255} {
		src := newTestImage(w, h)
		for i := 0; i < len(src.Pix); i += 4 {
			src.Pix[i+3] = 255 // opaque black background
		}
		ci := src.PixOffset(center.X, center.Y)
		src.Pix[ci] = 255
		src.Pix[ci+1] = 255
		src.Pix[ci+2] = 255
		src.Pix[ci+3] = 255

		dst := newTestImage(w, h)
		if err := ProcessRadiusMap(dst, src, uniformGrayMap(w, h, y), 10); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		peaks = append(peaks, int(dst.Pix[dst.PixOffset(center.X, center.Y)]))

		// The canvas stays opaque end-to-end because alpha is handled with the
		// same blur as the color channels on an all-opaque source.
		for i := 3; i < len(dst.Pix); i += 4 {
			if dst.Pix[i] != 255 {
				t.Fatalf("luminance %d: opaque canvas lost alpha at byte %d: %d", y, i, dst.Pix[i])
			}
		}

		var energy uint64
		for i := 0; i < len(dst.Pix); i += 4 {
			energy += uint64(dst.Pix[i])
		}
		if energy == 0 && y != 0 {
			t.Fatalf("luminance %d: blurred point lost all energy", y)
		}
		if energy > uint64(255)*uint64(w)*uint64(h) {
			t.Fatalf("luminance %d: energy exceeded physical bound", y)
		}
	}

	for i := 1; i < len(peaks); i++ {
		if peaks[i] > peaks[i-1] {
			t.Fatalf("peak intensity did not decay monotonically: %v", peaks)
		}
	}
	if peaks[0] != 255 {
		t.Fatalf("zero-radius peak must be untouched 255, got %d", peaks[0])
	}
}

// TestGray16MapAndNonStandardBounds exercises the Gray16 fast path and images
// whose bounds do not start at the origin.
func TestGray16MapAndNonStandardBounds(t *testing.T) {
	rect := image.Rect(5, 7, 5+testW, 7+testH)
	src := image.NewNRGBA(rect)
	for i := range src.Pix {
		src.Pix[i] = uint8((i*19 + 3) % 256)
		if i%4 == 3 {
			src.Pix[i] = 255
		}
	}
	rm16 := image.NewGray16(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			rm16.SetGray16(x, y, color.Gray16{Y: 0x8080}) // luminance 128 in 16-bit space
		}
	}

	dst := image.NewNRGBA(rect)
	if err := ProcessRadiusMap(dst, src, rm16, 8); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A uniform map must yield the same result as an equivalent Gray map.
	rm8 := uniformGrayMap(testW, testH, 128)
	dst8 := image.NewNRGBA(rect)
	if err := ProcessRadiusMap(dst8, src, rm8, 8); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pixEqual(dst.Pix, dst8.Pix) {
		t.Fatal("Gray16 and Gray radius maps produced different results")
	}
}

// TestProcessUnchanged guards the original fixed-radius API: its documented
// error behavior and basic output remain intact after the refactor.
func TestProcessUnchanged(t *testing.T) {
	src := newTestImage(16, 16)
	if err := Process(newTestImage(16, 16), src, 0); err == nil {
		t.Fatal("radius 0 must still be rejected")
	}
	var wrongType image.RGBA
	if err := Process(&wrongType, src, 5); err == nil {
		t.Fatal("non-NRGBA destination must still be rejected")
	}

	f, err := os.Open(filepath.Join("image", "sample.png"))
	if err != nil {
		t.Skipf("sample image unavailable: %v", err)
	}
	defer f.Close()
	sample, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewNRGBA(sample.Bounds())
	if err := Process(dst, sample, 10); err != nil {
		t.Fatalf("Process on sample image failed: %v", err)
	}
	// A blur of a natural image must actually change pixels.
	original := image.NewNRGBA(sample.Bounds())
	convertToNRGBA(original, sample)
	if pixEqual(original.Pix, dst.Pix) {
		t.Fatal("blur did not change the sample image")
	}
}

func pixEqual(a, b []uint8) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGenericImageSources covers the default model-conversion branches of the
// NRGBA conversion helper: a YCbCr source and an NRGBA radius map.
func TestGenericImageSources(t *testing.T) {
	w, h := 24, 18
	src := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio444)
	for i := range src.Y {
		src.Y[i] = uint8((i * 13) % 256)
	}
	rm := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := uint8((x * 255) / (w - 1))
		for y := 0; y < h; y++ {
			i := rm.PixOffset(x, y)
			rm.Pix[i] = v
			rm.Pix[i+1] = v
			rm.Pix[i+2] = v
			rm.Pix[i+3] = 255
		}
	}
	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The YCbCr fast path always emits opaque alpha.
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] != 255 {
			t.Fatal("YCbCr source must produce opaque alpha")
		}
	}

	// The same gradient through a Gray map must agree with the NRGBA map.
	gray := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := uint8((x * 255) / (w - 1))
		for y := 0; y < h; y++ {
			gray.SetGray(x, y, color.Gray{Y: v})
		}
	}
	dstGray := newTestImage(w, h)
	if err := ProcessRadiusMap(dstGray, src, gray, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pixEqual(dst.Pix, dstGray.Pix) {
		t.Fatal("NRGBA and Gray radius maps produced different results")
	}
}

// TestYCbCrSourceAndMap exercises the YCbCr conversion branch on the source
// and the generic model-based path when the radius map itself is YCbCr.
func TestYCbCrSourceAndMap(t *testing.T) {
	w, h := 20, 16
	src := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio444)
	for i := range src.Y {
		src.Y[i] = uint8((i * 17) % 256)
	}
	rm := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio444)
	// Luminance ramp via the Y plane only.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rm.Y[y*rm.YStride+x] = uint8((x * 255) / (w - 1))
		}
	}
	dst := newTestImage(w, h)
	if err := ProcessRadiusMap(dst, src, rm, 9); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] != 255 {
			t.Fatal("YCbCr source output must be opaque")
		}
	}
}

// TestProcessClampsOversizedRadius covers the fixed-radius API's defensive
// clamp when the radius reaches the lookup-table limit.
func TestProcessClampsOversizedRadius(t *testing.T) {
	src := newTestImage(24, 24)
	for i := range src.Pix {
		src.Pix[i] = uint8((i * 5) % 256)
	}
	clamped := newTestImage(24, 24)
	if err := Process(clamped, src, uint32(len(mulTable))+10); err != nil {
		t.Fatalf("clamped radius returned error: %v", err)
	}
	limit := newTestImage(24, 24)
	if err := Process(limit, src, uint32(len(mulTable)-1)); err != nil {
		t.Fatalf("limit radius returned error: %v", err)
	}
	if !pixEqual(clamped.Pix, limit.Pix) {
		t.Fatal("radius above the table limit must clamp to the last entry")
	}
}

// TestGenericSourceFallback covers the default model-conversion branch with an
// image type that is neither *image.NRGBA nor *image.YCbCr.
func TestGenericSourceFallback(t *testing.T) {
	rect := image.Rect(0, 0, 18, 14)
	src := image.NewRGBA(rect)
	for i := range src.Pix {
		src.Pix[i] = uint8((i*9 + 1) % 256)
	}
	dst := image.NewNRGBA(rect)
	if err := ProcessRadiusMap(dst, src, uniformGrayMap(18, 14, 128), 6); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The same generic source through the fixed-radius API must agree at the
	// bracketing integer radius for an all-full integer map.
	ref := image.NewNRGBA(rect)
	if err := Process(ref, src, 3); err != nil {
		t.Fatal(err)
	}
	full := image.NewNRGBA(rect)
	if err := ProcessRadiusMap(full, src, uniformGrayMap(18, 14, 255), 3); err != nil {
		t.Fatal(err)
	}
	if !pixEqual(ref.Pix, full.Pix) {
		t.Fatal("generic source: integer-radius map did not match fixed-radius output")
	}
}

// FuzzProcessRadiusMap verifies that arbitrary map content and source pixels
// never crash the function and that zero-radius pixels are always preserved.
func FuzzProcessRadiusMap(f *testing.F) {
	f.Add(16, 12, 10.0, []byte{0, 1, 2, 255, 128, 64, 0, 0})
	f.Fuzz(func(t *testing.T, w, h int, maxRadius float64, seed []byte) {
		if w < 1 || h < 1 || w > 48 || h > 48 {
			return
		}
		if math.IsNaN(maxRadius) || math.IsInf(maxRadius, 0) || maxRadius <= 0 || maxRadius > 32 {
			return
		}
		if len(seed) == 0 {
			seed = []byte{0}
		}
		src := image.NewNRGBA(image.Rect(0, 0, w, h))
		rm := image.NewGray(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			rm.Pix[i] = seed[i%len(seed)]
			for c := 0; c < 4; c++ {
				src.Pix[i*4+c] = seed[(i+c)%len(seed)]
			}
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		if err := ProcessRadiusMap(dst, src, rm, maxRadius); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for i := 0; i < w*h; i++ {
			if rm.Pix[i] == 0 {
				for c := 0; c < 4; c++ {
					if dst.Pix[i*4+c] != src.Pix[i*4+c] {
						t.Fatal("zero-radius pixel was modified")
					}
				}
			}
		}
	})
}

// TestSkinnyDimensions guards against out-of-bounds access in the vertical
// blur pass for images shorter than the blur radius (a 1-pixel-tall image
// used to panic inside the original StackBlur code).
func TestSkinnyDimensions(t *testing.T) {
	for _, dims := range [][2]int{{1, 1}, {1, 8}, {8, 1}, {2, 1}, {1, 2}, {3, 5}} {
		w, h := dims[0], dims[1]
		for r := uint32(1); r <= 12; r++ {
			src := newTestImage(w, h)
			for i := range src.Pix {
				src.Pix[i] = uint8((i*7 + 3) % 256)
			}
			dst := newTestImage(w, h)
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("Process panicked for %dx%d radius %d: %v", w, h, r, rec)
					}
				}()
				if err := Process(dst, src, r); err != nil {
					t.Fatalf("Process error for %dx%d radius %d: %v", w, h, r, err)
				}
			}()

			rmDst := newTestImage(w, h)
			rm := uniformGrayMap(w, h, 200)
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("ProcessRadiusMap panicked for %dx%d: %v", w, h, rec)
					}
				}()
				if err := ProcessRadiusMap(rmDst, src, rm, float64(r)); err != nil {
					t.Fatalf("ProcessRadiusMap error for %dx%d radius %d: %v", w, h, r, err)
				}
			}()
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func BenchmarkProcessRadiusMap(b *testing.B) {
	w, h := 512, 512
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range src.Pix {
		src.Pix[i] = uint8((i*31 + 13) % 256)
	}
	rm := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := uint8((x * 255) / (w - 1))
		for y := 0; y < h; y++ {
			rm.SetGray(x, y, color.Gray{Y: v})
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if err := ProcessRadiusMap(dst, src, rm, 24); err != nil {
			b.Fatal(err)
		}
	}
}
