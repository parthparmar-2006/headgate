package rolling

import "slices"

func MaxInWindow(timestampsMs []int64, windowMs int64) int {
	sorted := slices.Clone(timestampsMs)
	slices.Sort(sorted)

	best := 0
	start := 0
	for end := range sorted {
		for sorted[end]-sorted[start] >= windowMs {
			start++
		}
		count := end - start + 1
		if count > best {
			best = count
		}
	}
	return best
}
