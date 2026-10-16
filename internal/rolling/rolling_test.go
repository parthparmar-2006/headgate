package rolling

import "testing"

func TestMaxInWindowEmpty(t *testing.T) {
	if MaxInWindow(nil, 1000) != 0 {
		t.Fatalf("empty input should give 0")
	}
}

func TestMaxInWindowCountsHalfOpenWindow(t *testing.T) {
	timestamps := []int64{0, 999, 1000}

	got := MaxInWindow(timestamps, 1000)

	if got != 2 {
		t.Fatalf("got %d, want 2: a 1000ms window holds 0 and 999 or 999 and 1000, never all three", got)
	}
}

func TestMaxInWindowFindsBurstAcrossFixedBoundary(t *testing.T) {
	timestamps := []int64{}
	for i := range 100 {
		timestamps = append(timestamps, int64(900+i))
	}
	for i := range 100 {
		timestamps = append(timestamps, int64(1100+i))
	}

	got := MaxInWindow(timestamps, 1000)

	if got != 200 {
		t.Fatalf("got %d, want 200", got)
	}
}

func TestMaxInWindowIgnoresInputOrder(t *testing.T) {
	timestamps := []int64{3000, 0, 2000, 1, 2}

	got := MaxInWindow(timestamps, 1000)

	if got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}
