package main

import "math"

// rdp is the Ramer–Douglas–Peucker line simplification. It drops points that lie
// within epsilon of the straight segment between the surviving endpoints, so long
// near-collinear runs collapse to a single segment. Endpoints are always kept.
func rdp(pts [][2]float64, epsilon float64) [][2]float64 {
	if len(pts) < 3 || epsilon <= 0 {
		return pts
	}
	first, last := pts[0], pts[len(pts)-1]
	maxDist, idx := 0.0, 0
	for i := 1; i < len(pts)-1; i++ {
		if dd := perpDistance(pts[i], first, last); dd > maxDist {
			maxDist, idx = dd, i
		}
	}
	if maxDist <= epsilon {
		return [][2]float64{first, last}
	}
	left := rdp(pts[:idx+1], epsilon)
	right := rdp(pts[idx:], epsilon)
	return append(left[:len(left)-1], right...) // drop the shared pivot
}

// perpDistance is the perpendicular distance from p to the line through a and b.
func perpDistance(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	seg := math.Hypot(dx, dy)
	if seg == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	return math.Abs(dy*p[0]-dx*p[1]+b[0]*a[1]-b[1]*a[0]) / seg
}

// strOpt reads a non-empty string option from a command map, else returns def.
func strOpt(cmd map[string]interface{}, key, def string) string {
	if v, ok := cmd[key].(string); ok && v != "" {
		return v
	}
	return def
}
