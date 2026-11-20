# stackblur-go

[![Go Reference](https://pkg.go.dev/badge/github.com/esimov/stackblur-go.svg)](https://pkg.go.dev/github.com/esimov/stackblur-go)
[![build](https://github.com/esimov/stackblur-go/actions/workflows/build.yml/badge.svg)](https://github.com/esimov/stackblur-go/actions/workflows/build.yml)

Go port of Mario Klingemann's [Stackblur](http://incubator.quasimondo.com/processing/fast_blur_deluxe.php) algorithm.

Stackblur is a compromise between Gaussian blur and Box blur, but it creates much better looking blurs than Box blur and it is ~7x faster than Gaussian blur.

Comparing to the Javascript implementation the Go version is at least 50% faster (depending on the image size and blur radius), applied on the same image with the same bluring radius.

### Benchmark
Radius       | Javascript  | Go
-------------|-------------|-------------
20           | ~15ms       | ~7.4ms

## Installation

```bash
$ go install github.com/esimov/stackblur-go/cmd/stackblur@latest
```

#### CLI example

The provided CLI example supports the following flags:
```bash
$ stackblur --help

Usage of stackblur:
  -gif
    	Output Gif
  -in string
    	Source
  -out string
    	Destination
  -radius int
    	Radius (default 20)
```
The command below will generate the blurred version of the source image.

```bash
$ stackblur -in image/sample.png -out image/output.png -radius 10
```
The cli command supports a `-gif` flag, which if set as true it visualize the bluring process by outputting the result into a gif file.

## API

The usage of the API is very simple: it exposes a single public `Process` function which requires a destination and a source image together with a blur radius. The blured image will be encoded into the destination image.

```Go
stackblur.Process(dst, src, blurRadius)
```

### Radius-map (variable radius) blur

For depth-of-field style effects the package also exposes `ProcessRadiusMap`, which blurs each pixel with its own radius obtained from a same-sized radius map. The map luminance scales the configured maximum radius:

```Go
func ProcessRadiusMap(dst *image.NRGBA, src image.Image, radiusMap image.Image, maxRadius float64) error
```

- Black map pixels (`luminance = 0`) keep the source pixels untouched; white pixels (`luminance = 255`) apply `maxRadius`, and intermediate values produce `effectiveRadius = maxRadius * luminance / 255`.
- Fractional radii are rendered by interpolating the StackBlur results of the two bracketing integer radii, with the same weight applied to R, G, B and A. Radius transitions are therefore smooth (no visible steps) and semi-transparent edges keep a consistent alpha, while exact integer radii stay bit-identical to `Process`.
- `maxRadius` must be finite, positive and at most `stackblur.MaxRadiusMapRadius` (254). Size mismatches, nil/empty inputs, non-finite or out-of-range parameters return the documented sentinel errors. Temporary storage is capped at 256 MiB and `ErrScratchLimitExceeded` is returned before the cap is breached.
- The fixed-radius `Process` API is unchanged.

```Go
dst := image.NewNRGBA(src.Bounds())
rm := image.NewGray(src.Bounds())
// ...fill rm with per-pixel luminance values...
if err := stackblur.ProcessRadiusMap(dst, src, rm, 20); err != nil {
    log.Fatal(err)
}
```

## Results

| Original image | Blurred image |
|:--:|:--:|
| <img src="https://github.com/esimov/stackblur-go/blob/master/image/sample.png" height="300"> | <img src="https://github.com/esimov/stackblur-go/blob/master/image/output.png" height="300"> |


## License

This project is under the MIT License. See the [LICENSE](https://github.com/esimov/stackblur-go/blob/master/LICENSE) file for the full license text.
