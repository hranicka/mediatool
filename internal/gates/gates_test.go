package gates

import "testing"

func TestDurationKept(t *testing.T) {
	cases := []struct {
		newDur, srcDur float64
		want           bool
	}{
		{600, 600, true},
		{594.1, 600, true}, // 0.9901 of source
		{594, 600, true},   // exactly at the 0.99 threshold
		{593.9, 600, false},
		{600.5, 600, true}, // longer is fine
		{100, 600, false},  // damaged source losing content
	}
	for _, c := range cases {
		if got := DurationKept(c.newDur, c.srcDur, 0.99); got != c.want {
			t.Errorf("DurationKept(%v, %v) = %v, want %v", c.newDur, c.srcDur, got, c.want)
		}
	}
}

func TestShrunk(t *testing.T) {
	if Shrunk(100, 100) {
		t.Error("equal size must not count as shrunk")
	}
	if !Shrunk(99, 100) {
		t.Error("smaller size must count as shrunk")
	}
	if Shrunk(101, 100) {
		t.Error("bigger size must not count as shrunk")
	}
}
