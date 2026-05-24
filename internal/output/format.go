package output

import "fmt"

func FormatCores(cores float64) string {
	m := cores * 1000
	if m < 1 {
		return "<1m"
	}
	if m >= 1000 {
		return fmt.Sprintf("%.1f", cores)
	}
	return fmt.Sprintf("%.0fm", m)
}

func FormatMillicores(m int64) string {
	if m >= 1000 {
		return fmt.Sprintf("%.1f", float64(m)/1000)
	}
	return fmt.Sprintf("%dm", m)
}

func FormatBytes(b int64) string {
	const (
		gi = 1024 * 1024 * 1024
		mi = 1024 * 1024
	)
	if b >= gi {
		return fmt.Sprintf("%.1fGi", float64(b)/float64(gi))
	}
	return fmt.Sprintf("%dMi", b/mi)
}

func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
