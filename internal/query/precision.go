package query

// PrecisionRank orders edge precisions: precise > heuristic > candidate >
// unknown (0). Use it to pick the strongest precision of merged call sites.
func PrecisionRank(p string) int {
	switch p {
	case "precise":
		return 3
	case "heuristic":
		return 2
	case "candidate":
		return 1
	}
	return 0
}
