package main

import "math"

// fitStrokesToArea uniformly scales the bounding box over ALL strokes to fit within
// the configured drawing area (aspect ratio preserved), centers it, and returns the
// mapped strokes as pen-tip XY in the reference frame (mm). One shared transform is
// used across every stroke so their relative positions are preserved. Assumes there
// is at least one point.
func fitStrokesToArea(strokes []stroke, cfg Config) []stroke {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, s := range strokes {
		for _, p := range s {
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
			minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	bw, bh := maxX-minX, maxY-minY

	scale := math.Inf(1)
	if bw > 0 {
		scale = cfg.AreaWidthMM / bw
	}
	if bh > 0 {
		scale = math.Min(scale, cfg.AreaHeightMM/bh)
	}
	if math.IsInf(scale, 1) {
		scale = 1 // degenerate: all points coincident
	}

	// Center the scaled drawing within the area.
	offsetX := cfg.AreaXMM + (cfg.AreaWidthMM-bw*scale)/2
	offsetY := cfg.AreaYMM + (cfg.AreaHeightMM-bh*scale)/2

	out := make([]stroke, len(strokes))
	for i, s := range strokes {
		mapped := make(stroke, len(s))
		for j, p := range s {
			mapped[j] = [2]float64{
				offsetX + (p[0]-minX)*scale,
				offsetY + (p[1]-minY)*scale,
			}
		}
		out[i] = mapped
	}
	return out
}

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
