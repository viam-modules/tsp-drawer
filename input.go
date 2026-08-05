package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// loadInput reads the ordered (x,y) points for a "draw" command.
//
//	{"command":"draw","path":"<points.tsp>","tour":"<tour file>"}
//
// Points come from the .tsp NODE_COORD_SECTION in file order (the i-th row is index
// i, matching 0-based tour indices). "tour" is required and gives the visiting order.
func (d *drawer) loadInput(cmd map[string]interface{}) ([][2]float64, error) {
	path, _ := cmd["path"].(string)
	if path == "" {
		return nil, errors.New("draw requires a 'path' to a .tsp file")
	}
	tourPath, _ := cmd["tour"].(string)
	if tourPath == "" {
		return nil, errors.New("draw requires a 'tour' file giving the point execution order")
	}

	points, err := loadCoords(path)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", path)
	}
	if len(points) == 0 {
		return nil, errors.Errorf("no NODE_COORD_SECTION points in %s", path)
	}

	order, err := loadTour(tourPath)
	if err != nil {
		return nil, errors.Wrapf(err, "reading tour %s", tourPath)
	}
	if len(order) == 0 {
		return nil, errors.Errorf("no tour entries in %s", tourPath)
	}
	ordered := make([][2]float64, 0, len(order))
	for _, idx := range order {
		id := idx + 1 // .tsp node ids are 1-based; tour indices are 0-based
		coord, ok := points[id]
		if !ok {
			return nil, errors.Errorf("%s: tour index %d (node id %d) not found in %s", tourPath, idx, id, path)
		}
		ordered = append(ordered, coord)
	}
	d.logger.Infof("drawing %d points from %s ordered by %s", len(ordered), path, tourPath)
	return ordered, nil
}

// loadCoords reads a TSPLIB NODE_COORD_SECTION ("id x y" rows) into a map keyed by
// the (1-based) node id, so tour indices can be resolved by id regardless of row order.
func loadCoords(path string) (map[int][2]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	i := indexOfKeyword(fields, "NODE_COORD_SECTION")

	pts := make(map[int][2]float64)
	for i >= 0 && i+2 < len(fields) {
		id, errID := strconv.Atoi(fields[i])
		if errID != nil {
			break // "EOF" marker or next section keyword ends the coordinates
		}
		x, errX := strconv.ParseFloat(fields[i+1], 64)
		y, errY := strconv.ParseFloat(fields[i+2], 64)
		if errX != nil || errY != nil {
			break
		}
		pts[id] = [2]float64{x, y}
		i += 3
	}
	return pts, nil
}

// loadTour parses an LKH-style tour file: an optional "N M" header line, then edge
// lines "u v weight" describing a Hamiltonian cycle over 0-based node indices. It
// follows successor edges from the first node and returns the visiting order,
// re-appending the start so the closed tour's final segment is drawn.
func loadTour(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	succ := make(map[int]int)
	start, haveStart := 0, false
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 { // "N M" header or blank line
			continue
		}
		u, err1 := strconv.Atoi(f[0])
		v, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		succ[u] = v
		if !haveStart {
			start, haveStart = u, true
		}
	}

	order := make([]int, 0, len(succ)+1)
	seen := make(map[int]bool, len(succ))
	for cur := start; haveStart && !seen[cur]; {
		seen[cur] = true
		order = append(order, cur)
		next, ok := succ[cur]
		if !ok {
			break
		}
		cur = next
	}
	// Close the cycle back to the start if the last edge returns there.
	if n := len(order); n > 0 {
		if next, ok := succ[order[n-1]]; ok && next == order[0] {
			order = append(order, order[0])
		}
	}
	return order, nil
}

// indexOfKeyword returns the index just after the given keyword token, or -1.
func indexOfKeyword(fields []string, keyword string) int {
	for idx, f := range fields {
		if strings.EqualFold(f, keyword) {
			return idx + 1
		}
	}
	return -1
}
