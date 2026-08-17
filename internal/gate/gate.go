// Package gate runs the tqwebp release gates and reports the numbers
// behind them. cmd/tqbench drives it, so one command answers the whole
// question "does this build still meet its bars".
//
// The gates come from section 10.2 of the tqwebp specification:
//
//	G1  Every corpus image round-trips through an independent decoder,
//	    keeps its size, and, at loop filter level 0, decodes to exactly
//	    the encoder's own reconstruction.
//	G2  Photos: median bytes at most 0.90 times stdlib JPEG quality 82 at
//	    equal luma PSNR, and no image above 1.10 times.
//	G2b Photos: the quality curve does not plateau. The median luma PSNR
//	    must rise by 2.5 dB from quality 75 to quality 90, for at most
//	    2.2 times the bytes, and it must rise at every step.
//	G3  Photos against libwebp, and encode speed. Informative, not
//	    blocking.
//	G4b Photos against deepteams/webp, from the committed fixture.
//
// # Two PSNR domains, on purpose
//
// Coded luma PSNR compares the encoder's own planes with the decoder's
// planes. It answers questions about the codec, such as the shape of the
// quality curve, and no colour convention can distort it.
//
// Display luma PSNR converts both pictures to red, green, and blue first,
// each through its own correct inverse, and measures the luma of that.
// It is the only fair way to compare two codecs that carry different
// sample ranges, so every cross-codec gate uses it.
package gate

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/internal/encoder"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// Point is one measured (image, quality) result.
type Point struct {
	Image  string `json:"image"`
	Class  string `json:"class"`
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`

	Quality int `json:"quality"`
	Bytes   int `json:"bytes"`

	// DisplayYPSNR is luma PSNR after each codec's own correct inverse
	// colour conversion. Cross-codec comparisons use it.
	DisplayYPSNR float64 `json:"display_y_psnr_db"`
	// CodedYPSNR is luma PSNR in the codec's own plane domain. Only the
	// WebP encoder reports it.
	CodedYPSNR float64 `json:"coded_y_psnr_db,omitempty"`
	// SSIM is luma structural similarity after the inverse conversion.
	SSIM float64 `json:"ssim"`
	// EncodeMillis is the wall time of one encode.
	EncodeMillis float64 `json:"encode_ms"`
	// MillisPerMegapixel normalizes EncodeMillis by picture size.
	MillisPerMegapixel float64 `json:"ms_per_megapixel"`
	// ExactReconstruction reports whether the decoder reproduced the
	// encoder's own picture byte for byte.
	ExactReconstruction bool `json:"exact_reconstruction,omitempty"`
}

// Report holds every measurement and every gate verdict of one run.
type Report struct {
	Qualities []int   `json:"qualities"`
	Points    []Point `json:"points"`

	G1  G1Result  `json:"g1"`
	G2  G2Result  `json:"g2"`
	G2b G2bResult `json:"g2b"`
	G3  G3Result  `json:"g3"`
	G4b G4bResult `json:"g4b"`
}

// G1Result reports the correctness gate.
type G1Result struct {
	Pass          bool     `json:"pass"`
	Images        int      `json:"images"`
	Encodes       int      `json:"encodes"`
	ExactMatches  int      `json:"exact_matches"`
	DecodeErrors  []string `json:"decode_errors,omitempty"`
	MismatchNotes []string `json:"mismatch_notes,omitempty"`
}

// G2Match is one image's byte comparison against stdlib JPEG at equal
// luma PSNR.
type G2Match struct {
	Image        string  `json:"image"`
	JPEGQuality  int     `json:"jpeg_quality"`
	JPEGBytes    int     `json:"jpeg_bytes"`
	JPEGYPSNR    float64 `json:"jpeg_y_psnr_db"`
	WebPQualityL int     `json:"webp_quality_low"`
	WebPQualityH int     `json:"webp_quality_high"`
	WebPBytes    float64 `json:"webp_bytes_at_matched_psnr"`
	Ratio        float64 `json:"ratio_webp_over_jpeg"`
	Note         string  `json:"note,omitempty"`
}

// G2Result reports the rate gate against stdlib JPEG.
type G2Result struct {
	Pass        bool      `json:"pass"`
	MedianRatio float64   `json:"median_ratio"`
	WorstRatio  float64   `json:"worst_ratio"`
	Matches     []G2Match `json:"matches"`
}

// G2bResult reports the anti-plateau gate.
type G2bResult struct {
	Pass              bool               `json:"pass"`
	MedianGain75To90  float64            `json:"median_gain_q75_to_q90_db"`
	MedianByteRatio   float64            `json:"median_byte_ratio_q90_over_q75"`
	StrictlyIncreases bool               `json:"strictly_increases"`
	PerImage          []G2bImage         `json:"per_image"`
	Curve             map[string]float64 `json:"median_curve_db"`

	// The control fields carry the same two numbers for libwebp, measured
	// on the same images from the committed fixture. They answer the
	// question a bare verdict cannot: is a failure the encoder's, or is it
	// the corpus asking for something no encoder can give?
	ControlCodec     string  `json:"control_codec,omitempty"`
	ControlGain      float64 `json:"control_gain_q75_to_q90_db,omitempty"`
	ControlByteRatio float64 `json:"control_byte_ratio_q90_over_q75,omitempty"`
}

// G2bImage is one image's curve summary.
type G2bImage struct {
	Image      string  `json:"image"`
	GainDB     float64 `json:"gain_q75_to_q90_db"`
	ByteRatio  float64 `json:"byte_ratio_q90_over_q75"`
	Increasing bool    `json:"increasing"`
}

// G3Result reports speed, and the libwebp comparison when its fixture is
// present. The gate is informative: it never fails a run.
type G3Result struct {
	MedianMillisPerMegapixel float64      `json:"median_ms_per_megapixel"`
	WorstMillisPerMegapixel  float64      `json:"worst_ms_per_megapixel"`
	LibwebpFixture           string       `json:"libwebp_fixture,omitempty"`
	LibwebpMatches           []G2Match    `json:"libwebp_matches,omitempty"`
	LibwebpMedianRatio       float64      `json:"libwebp_median_ratio,omitempty"`
	Note                     string       `json:"note,omitempty"`
	Speed                    []SpeedPoint `json:"speed"`
}

// SpeedPoint is one image's encode speed at the default quality.
type SpeedPoint struct {
	Image              string  `json:"image"`
	Megapixels         float64 `json:"megapixels"`
	MillisPerMegapixel float64 `json:"ms_per_megapixel"`
}

// G4bResult reports the comparison against the deepteams/webp fixture.
// Specification section 10.2 gates it at WP-2 and reads it at matched
// bytes; WP-1 reports both readings and gates neither.
type G4bResult struct {
	Available            bool          `json:"available"`
	Note                 string        `json:"note,omitempty"`
	MedianGain           float64       `json:"median_gain_at_same_quality_db"`
	MedianGainAtBytes    float64       `json:"median_gain_at_same_bytes_db"`
	MedianGainAtBytesAll float64       `json:"median_gain_at_same_bytes_db_all_classes"`
	Comparisons          []G4bCompared `json:"comparisons,omitempty"`
}

// G4bCompared is one (image, quality) comparison against deepteams/webp.
// It carries both readings the question needs: the same quality number,
// and the same file size.
type G4bCompared struct {
	Image          string  `json:"image"`
	Quality        int     `json:"quality"`
	TQWebPBytes    int     `json:"tqwebp_bytes"`
	TQWebPYPSNR    float64 `json:"tqwebp_y_psnr_db"`
	DeepteamsBytes int     `json:"deepteams_bytes"`
	DeepteamsYPSNR float64 `json:"deepteams_y_psnr_db"`
	GainDB         float64 `json:"gain_at_same_quality_db"`
	ByteRatio      float64 `json:"byte_ratio"`
	// GainAtSameBytes is the gate's own reading: tqwebp's luma PSNR
	// interpolated to deepteams' file size, minus deepteams' luma PSNR.
	GainAtSameBytes float64 `json:"gain_at_same_bytes_db"`
	// AtSameBytesNote explains a reading the sweep could not bracket.
	AtSameBytesNote string `json:"at_same_bytes_note,omitempty"`
}

// Options configures a run.
type Options struct {
	// Qualities are the tqwebp quality settings to measure.
	Qualities []int
	// JPEGQuality is the stdlib JPEG setting gate G2 compares against.
	JPEGQuality int
	// DeepteamsTable is the committed deepteams/webp fixture, in the text
	// form oracle.Table renders. An empty string disables gate G4b.
	DeepteamsTable string
	// LibwebpPoints holds a libwebp measurement fixture for gate G3. A nil
	// map disables the comparison.
	LibwebpPoints map[string][]LibwebpPoint
	// LibwebpFixture names the fixture file, for the report.
	LibwebpFixture string
	// WriteEncoded receives every encoded file when it is not nil, so an
	// external tool can check the same bytes.
	WriteEncoded func(name string, quality int, data []byte) error
}

// LibwebpPoint is one measured libwebp result, read from a fixture.
type LibwebpPoint struct {
	Quality int     `json:"quality"`
	Bytes   int     `json:"bytes"`
	YPSNR   float64 `json:"display_y_psnr_db"`
}

// DefaultQualities is the quality sweep the gates use.
var DefaultQualities = []int{10, 25, 50, 75, 85, 90, 95}

// Run measures every image at every quality and evaluates the gates.
func Run(images []corpus.Image, opts Options) (*Report, error) {
	if len(opts.Qualities) == 0 {
		opts.Qualities = DefaultQualities
	}
	if opts.JPEGQuality == 0 {
		opts.JPEGQuality = 82
	}

	rep := &Report{Qualities: opts.Qualities}
	rep.G1.Images = len(images)

	for _, img := range images {
		for _, q := range opts.Qualities {
			pt, data, err := measure(img, q)
			if err != nil {
				rep.G1.DecodeErrors = append(rep.G1.DecodeErrors, fmt.Sprintf("%s q%d: %v", img.Spec.Name, q, err))
				continue
			}
			rep.G1.Encodes++
			if pt.ExactReconstruction {
				rep.G1.ExactMatches++
			} else {
				rep.G1.MismatchNotes = append(rep.G1.MismatchNotes,
					fmt.Sprintf("%s q%d: decoded picture differs from the encoder's reconstruction", img.Spec.Name, q))
			}
			rep.Points = append(rep.Points, pt)
			if opts.WriteEncoded != nil {
				if err := opts.WriteEncoded(img.Spec.Name, q, data); err != nil {
					return nil, err
				}
			}
		}
	}
	rep.G1.Pass = len(rep.G1.DecodeErrors) == 0 &&
		len(rep.G1.MismatchNotes) == 0 &&
		rep.G1.Encodes == len(images)*len(opts.Qualities)

	if err := rep.evaluateG2(images, opts); err != nil {
		return nil, err
	}
	rep.evaluateG2b(images, opts)
	rep.evaluateG3(images, opts)
	if err := rep.evaluateG4b(opts); err != nil {
		return nil, err
	}
	return rep, nil
}

// measure encodes one image at one quality and scores the result.
func measure(img corpus.Image, quality int) (Point, []byte, error) {
	start := time.Now()
	data, recon, err := encoder.EncodeWithReconstruction(img.Img, encoder.Config{Quality: quality, Method: 4})
	if err != nil {
		return Point{}, nil, fmt.Errorf("encode: %w", err)
	}
	elapsed := time.Since(start)
	planes, err := oracle.DecodeWebPPlanes(data)
	if err != nil {
		return Point{}, nil, fmt.Errorf("decode: %w", err)
	}
	rgb := oracle.WebPPlanesToRGBA(planes)

	b := img.Img.Bounds()
	if planes.Rect.Dx() != b.Dx() || planes.Rect.Dy() != b.Dy() {
		return Point{}, nil, fmt.Errorf("decoded size %v, want %v", planes.Rect.Size(), b.Size())
	}

	display, err := oracle.MeasurePSNR(img.Img, rgb)
	if err != nil {
		return Point{}, nil, err
	}
	ssim, err := oracle.MeasureSSIM(img.Img, rgb)
	if err != nil {
		return Point{}, nil, err
	}
	src := yuv.Convert(img.Img)
	coded, err := oracle.MeasurePlanePSNR(src.YCbCr(), planes)
	if err != nil {
		return Point{}, nil, err
	}

	megapixels := float64(b.Dx()*b.Dy()) / 1e6
	millis := float64(elapsed.Nanoseconds()) / 1e6
	return Point{
		Image:               img.Spec.Name,
		Class:               string(img.Spec.Class),
		Codec:               "tqwebp",
		Width:               b.Dx(),
		Height:              b.Dy(),
		Quality:             quality,
		Bytes:               len(data),
		DisplayYPSNR:        display.Y,
		CodedYPSNR:          coded.Y,
		SSIM:                ssim,
		EncodeMillis:        millis,
		MillisPerMegapixel:  millis / megapixels,
		ExactReconstruction: oracle.CompareExact(planeSource{recon}, planes) == nil,
	}, data, nil
}

type planeSource struct{ planes *image.YCbCr }

func (p planeSource) ReconstructionPlanes() (*image.YCbCr, error) { return p.planes, nil }

// evaluateG2 compares tqwebp with stdlib JPEG on the photo class, at
// equal display luma PSNR.
func (rep *Report) evaluateG2(images []corpus.Image, opts Options) error {
	var ratios []float64
	for _, img := range images {
		if img.Spec.Class != corpus.Photo {
			continue
		}
		ref, err := measureJPEG(img, opts.JPEGQuality)
		if err != nil {
			return err
		}
		match := matchBytesAtPSNR(rep.Points, img.Spec.Name, ref.DisplayYPSNR)
		match.Image = img.Spec.Name
		match.JPEGQuality = opts.JPEGQuality
		match.JPEGBytes = ref.Bytes
		match.JPEGYPSNR = ref.DisplayYPSNR
		if match.WebPBytes > 0 {
			match.Ratio = match.WebPBytes / float64(ref.Bytes)
			ratios = append(ratios, match.Ratio)
		}
		rep.G2.Matches = append(rep.G2.Matches, match)
	}

	rep.G2.MedianRatio = median(ratios)
	rep.G2.WorstRatio = maxOf(ratios)
	rep.G2.Pass = len(ratios) > 0 && rep.G2.MedianRatio <= 0.90 && rep.G2.WorstRatio <= 1.10
	return nil
}

// matchBytesAtPSNR interpolates how many bytes tqwebp spends to reach a
// target luma PSNR. Bytes interpolate geometrically between the two
// measured qualities that bracket the target, which is the shape a
// rate-distortion curve has.
func matchBytesAtPSNR(points []Point, image string, target float64) G2Match {
	var curve []Point
	for _, p := range points {
		if p.Image == image {
			curve = append(curve, p)
		}
	}
	sort.Slice(curve, func(i, j int) bool { return curve[i].Quality < curve[j].Quality })
	if len(curve) == 0 {
		return G2Match{Note: "no measurements for this image"}
	}

	for i := 1; i < len(curve); i++ {
		lo, hi := curve[i-1], curve[i]
		if lo.DisplayYPSNR <= target && target <= hi.DisplayYPSNR {
			span := hi.DisplayYPSNR - lo.DisplayYPSNR
			var t float64
			if span > 0 {
				t = (target - lo.DisplayYPSNR) / span
			}
			bytes := geometricInterpolate(float64(lo.Bytes), float64(hi.Bytes), t)
			return G2Match{
				WebPQualityL: lo.Quality,
				WebPQualityH: hi.Quality,
				WebPBytes:    bytes,
			}
		}
	}

	first, last := curve[0], curve[len(curve)-1]
	if target < first.DisplayYPSNR {
		return G2Match{
			WebPQualityL: first.Quality,
			WebPQualityH: first.Quality,
			WebPBytes:    float64(first.Bytes),
			Note: fmt.Sprintf("the sweep never drops to %.2f dB; quality %d already reaches %.2f dB, so the reported bytes are an upper bound",
				target, first.Quality, first.DisplayYPSNR),
		}
	}
	return G2Match{
		WebPQualityL: last.Quality,
		WebPQualityH: last.Quality,
		WebPBytes:    float64(last.Bytes),
		Note: fmt.Sprintf("the sweep never reaches %.2f dB; quality %d tops out at %.2f dB, so the reported bytes are a lower bound",
			target, last.Quality, last.DisplayYPSNR),
	}
}

// psnrAtBytes interpolates the luma PSNR tqwebp reaches at a given file
// size. The interpolation runs against the logarithm of the size, which
// is the shape a rate-distortion curve has.
func psnrAtBytes(points []Point, image string, size int) (float64, string) {
	var curve []Point
	for _, p := range points {
		if p.Image == image {
			curve = append(curve, p)
		}
	}
	sort.Slice(curve, func(i, j int) bool { return curve[i].Bytes < curve[j].Bytes })
	if len(curve) == 0 {
		return 0, "no measurements for this image"
	}
	for i := 1; i < len(curve); i++ {
		lo, hi := curve[i-1], curve[i]
		if lo.Bytes <= size && size <= hi.Bytes {
			var t float64
			if hi.Bytes > lo.Bytes {
				t = math.Log(float64(size)/float64(lo.Bytes)) / math.Log(float64(hi.Bytes)/float64(lo.Bytes))
			}
			return lo.DisplayYPSNR + t*(hi.DisplayYPSNR-lo.DisplayYPSNR), ""
		}
	}
	if size < curve[0].Bytes {
		return curve[0].DisplayYPSNR, fmt.Sprintf("the sweep never falls to %d bytes; its smallest file is %d bytes", size, curve[0].Bytes)
	}
	last := curve[len(curve)-1]
	return last.DisplayYPSNR, fmt.Sprintf("the sweep never reaches %d bytes; its largest file is %d bytes", size, last.Bytes)
}

func geometricInterpolate(lo, hi, t float64) float64 {
	if lo <= 0 || hi <= 0 {
		return lo + (hi-lo)*t
	}
	return lo * math.Pow(hi/lo, t)
}

// evaluateG2b checks the shape of the quality curve on the photo class.
func (rep *Report) evaluateG2b(images []corpus.Image, opts Options) {
	var gains, ratios []float64
	increasing := true
	curveSum := map[int][]float64{}

	for _, img := range images {
		if img.Spec.Class != corpus.Photo {
			continue
		}
		byQuality := map[int]Point{}
		var qualities []int
		for _, p := range rep.Points {
			if p.Image == img.Spec.Name {
				byQuality[p.Quality] = p
				qualities = append(qualities, p.Quality)
			}
		}
		sort.Ints(qualities)

		imageIncreasing := true
		for i := 1; i < len(qualities); i++ {
			prev, cur := byQuality[qualities[i-1]], byQuality[qualities[i]]
			if cur.CodedYPSNR <= prev.CodedYPSNR || cur.Bytes <= prev.Bytes {
				imageIncreasing = false
				increasing = false
			}
		}
		for _, q := range qualities {
			curveSum[q] = append(curveSum[q], byQuality[q].CodedYPSNR)
		}

		q75, ok75 := byQuality[75]
		q90, ok90 := byQuality[90]
		if !ok75 || !ok90 {
			continue
		}
		gain := q90.CodedYPSNR - q75.CodedYPSNR
		ratio := float64(q90.Bytes) / float64(q75.Bytes)
		gains = append(gains, gain)
		ratios = append(ratios, ratio)
		rep.G2b.PerImage = append(rep.G2b.PerImage, G2bImage{
			Image:      img.Spec.Name,
			GainDB:     gain,
			ByteRatio:  ratio,
			Increasing: imageIncreasing,
		})
	}

	rep.G2b.MedianGain75To90 = median(gains)
	rep.G2b.MedianByteRatio = median(ratios)
	rep.G2b.StrictlyIncreases = increasing
	rep.G2b.Curve = map[string]float64{}
	for q, values := range curveSum {
		rep.G2b.Curve[fmt.Sprintf("q%d", q)] = median(values)
	}
	rep.G2b.Pass = len(gains) > 0 &&
		rep.G2b.MedianGain75To90 >= 2.5 &&
		rep.G2b.MedianByteRatio <= 2.2 &&
		increasing

	rep.measureControlCurve(images, opts)
}

// measureControlCurve reads the same two curve numbers for libwebp from
// its fixture, over the same photo images.
func (rep *Report) measureControlCurve(images []corpus.Image, opts Options) {
	if opts.LibwebpPoints == nil {
		return
	}
	var gains, ratios []float64
	for _, img := range images {
		if img.Spec.Class != corpus.Photo {
			continue
		}
		rows, ok := opts.LibwebpPoints[img.Spec.Name]
		if !ok {
			continue
		}
		var lo, hi *LibwebpPoint
		for i := range rows {
			switch rows[i].Quality {
			case 75:
				lo = &rows[i]
			case 90:
				hi = &rows[i]
			}
		}
		if lo == nil || hi == nil {
			continue
		}
		gains = append(gains, hi.YPSNR-lo.YPSNR)
		ratios = append(ratios, float64(hi.Bytes)/float64(lo.Bytes))
	}
	if len(gains) == 0 {
		return
	}
	rep.G2b.ControlCodec = "libwebp"
	rep.G2b.ControlGain = median(gains)
	rep.G2b.ControlByteRatio = median(ratios)
}

// evaluateG3 reports speed, and compares against libwebp when a fixture
// is present.
func (rep *Report) evaluateG3(images []corpus.Image, opts Options) {
	var perMegapixel []float64
	for _, p := range rep.Points {
		if p.Quality != 75 {
			continue
		}
		perMegapixel = append(perMegapixel, p.MillisPerMegapixel)
		rep.G3.Speed = append(rep.G3.Speed, SpeedPoint{
			Image:              p.Image,
			Megapixels:         float64(p.Width*p.Height) / 1e6,
			MillisPerMegapixel: p.MillisPerMegapixel,
		})
	}
	rep.G3.MedianMillisPerMegapixel = median(perMegapixel)
	rep.G3.WorstMillisPerMegapixel = maxOf(perMegapixel)

	if opts.LibwebpPoints == nil {
		rep.G3.Note = "no libwebp fixture: the rate comparison did not run"
		return
	}
	rep.G3.LibwebpFixture = opts.LibwebpFixture

	var ratios []float64
	for _, img := range images {
		if img.Spec.Class != corpus.Photo {
			continue
		}
		ref, ok := opts.LibwebpPoints[img.Spec.Name]
		if !ok {
			continue
		}
		var at75 *LibwebpPoint
		for i := range ref {
			if ref[i].Quality == 75 {
				at75 = &ref[i]
			}
		}
		if at75 == nil {
			continue
		}
		match := matchBytesAtPSNR(rep.Points, img.Spec.Name, at75.YPSNR)
		match.Image = img.Spec.Name
		match.JPEGQuality = at75.Quality
		match.JPEGBytes = at75.Bytes
		match.JPEGYPSNR = at75.YPSNR
		if match.WebPBytes > 0 {
			match.Ratio = match.WebPBytes / float64(at75.Bytes)
			ratios = append(ratios, match.Ratio)
		}
		rep.G3.LibwebpMatches = append(rep.G3.LibwebpMatches, match)
	}
	rep.G3.LibwebpMedianRatio = median(ratios)
}

// evaluateG4b compares tqwebp with the committed deepteams/webp fixture,
// at the qualities both measured.
func (rep *Report) evaluateG4b(opts Options) error {
	if opts.DeepteamsTable == "" {
		rep.G4b.Note = "no deepteams/webp fixture: the comparison did not run"
		return nil
	}
	rows, err := parseTable(opts.DeepteamsTable)
	if err != nil {
		return err
	}
	rep.G4b.Available = true

	var gains, gainsAtBytes, gainsAtBytesAll []float64
	for _, p := range rep.Points {
		key := tableKey{image: p.Image, quality: p.Quality}
		row, ok := rows[key]
		if !ok {
			continue
		}
		gain := p.DisplayYPSNR - row.psnr
		gains = append(gains, gain)

		atBytes, note := psnrAtBytes(rep.Points, p.Image, row.bytes)
		cmp := G4bCompared{
			Image:           p.Image,
			Quality:         p.Quality,
			TQWebPBytes:     p.Bytes,
			TQWebPYPSNR:     p.DisplayYPSNR,
			DeepteamsBytes:  row.bytes,
			DeepteamsYPSNR:  row.psnr,
			GainDB:          gain,
			ByteRatio:       float64(p.Bytes) / float64(row.bytes),
			GainAtSameBytes: atBytes - row.psnr,
			AtSameBytesNote: note,
		}
		if note == "" {
			gainsAtBytesAll = append(gainsAtBytesAll, cmp.GainAtSameBytes)
			if p.Class == string(corpus.Photo) {
				gainsAtBytes = append(gainsAtBytes, cmp.GainAtSameBytes)
			}
		}
		rep.G4b.Comparisons = append(rep.G4b.Comparisons, cmp)
	}
	rep.G4b.MedianGain = median(gains)
	rep.G4b.MedianGainAtBytes = median(gainsAtBytes)
	rep.G4b.MedianGainAtBytesAll = median(gainsAtBytesAll)
	if len(gains) == 0 {
		rep.G4b.Note = "the fixture and this run share no (image, quality) pair"
	}
	return nil
}

// measureJPEG scores stdlib JPEG at one quality, in the display domain.
func measureJPEG(img corpus.Image, quality int) (Point, error) {
	enc := func(w io.Writer, m image.Image) error {
		return jpeg.Encode(w, m, &jpeg.Options{Quality: quality})
	}
	res, err := oracle.RoundTripWith(enc, img.Img, jpeg.Decode)
	if err != nil {
		return Point{}, fmt.Errorf("jpeg baseline for %s: %w", img.Spec.Name, err)
	}
	return Point{
		Image:        img.Spec.Name,
		Class:        string(img.Spec.Class),
		Codec:        "jpeg",
		Quality:      quality,
		Bytes:        res.EncodedBytes,
		DisplayYPSNR: res.PSNR.Y,
		SSIM:         res.SSIM,
	}, nil
}

type tableKey struct {
	image   string
	quality int
}

type tableRow struct {
	bytes int
	psnr  float64
}

// parseTable reads the fixed-width table oracle.Table renders.
func parseTable(text string) (map[tableKey]tableRow, error) {
	rows := map[tableKey]tableRow{}
	for i, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || i == 0 {
			continue
		}
		var key tableKey
		var row tableRow
		key.image = fields[0]
		if _, err := fmt.Sscanf(fields[3], "%d", &key.quality); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(fields[4], "%d", &row.bytes); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(fields[5], "%g", &row.psnr); err != nil {
			continue
		}
		rows[key] = row
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("gate: the fixture holds no rows")
	}
	return rows, nil
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func maxOf(values []float64) float64 {
	out := 0.0
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	return out
}
