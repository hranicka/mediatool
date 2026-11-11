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

func TestIndexBudget(t *testing.T) {
	if got := IndexBudget(0); got != 64<<10 {
		t.Errorf("IndexBudget(0) = %d, want the %d allowance", got, 64<<10)
	}
	// one index entry per frame plus the allowance
	if got := IndexBudget(750); got != 64<<10+750*32 {
		t.Errorf("IndexBudget(750) = %d, want %d", got, 64<<10+750*32)
	}
	if IndexBudget(-5) != 64<<10 {
		t.Error("negative frames must fall back to the allowance")
	}
}

func TestWithinBudget(t *testing.T) {
	if !WithinBudget(100, 100, 50) {
		t.Error("same size must stay within budget")
	}
	if !WithinBudget(150, 100, 50) {
		t.Error("growth exactly at the budget must pass")
	}
	if WithinBudget(151, 100, 50) {
		t.Error("growth beyond the budget must fail")
	}
	if !WithinBudget(50, 100, 50) {
		t.Error("smaller size must pass")
	}
}
