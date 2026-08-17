package corpus

// splitmix64 is a small, fully self-contained pseudo-random generator.
// The corpus generator does not use math/rand so the emitted PNG bytes
// stay identical across Go toolchains and versions forever: the sequence
// depends only on this file, not on any standard-library promise.
type splitmix64 struct {
	state uint64
}

func newRNG(seed uint64) *splitmix64 {
	return &splitmix64{state: seed}
}

func (r *splitmix64) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// float64 returns a pseudo-random value in [0, 1).
func (r *splitmix64) float64() float64 {
	return float64(r.next()>>11) / (1 << 53)
}

// intn returns a pseudo-random value in [0, n).
func (r *splitmix64) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// signedNoise returns a pseudo-random value in [-amplitude, amplitude].
func (r *splitmix64) signedNoise(amplitude float64) float64 {
	return (r.float64()*2 - 1) * amplitude
}
