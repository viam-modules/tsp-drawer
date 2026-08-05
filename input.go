package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// loadInput resolves the ordered (x,y) tour from files on disk.
//
// The command may give "points_path" (+ optional "order_path"), or a "path"
// (falling back to configured input_path) that is a single file or a directory.
// A directory is scanned for a .tsp (points) and a .tour/.cyc (order); a single
// file resolves its sibling by basename. Points come from the TSPLIB
// NODE_COORD_SECTION; order (optional, 1-based) from a TOUR_SECTION. Without an
// order file, points are drawn in ascending node-id order.
func (d *drawer) loadInput(cmd map[string]interface{}) ([][2]float64, error) {
	pointsPath := strOpt(cmd, "points_path", "")
	orderPath := strOpt(cmd, "order_path", "")
	if pointsPath == "" {
		p := strOpt(cmd, "path", d.cfg.InputPath)
		if p == "" {
			return nil, errors.New("no input: set 'path'/'points_path' in the command or 'input_path' in config")
		}
		var err error
		if pointsPath, orderPath, err = discover(p); err != nil {
			return nil, err
		}
	}

	coords, err := loadNodeCoords(pointsPath)
	if err != nil {
		return nil, errors.Wrapf(err, "reading points from %s", pointsPath)
	}
	if len(coords) == 0 {
		return nil, errors.Errorf("no NODE_COORD_SECTION points in %s", pointsPath)
	}

	if orderPath == "" {
		d.logger.Infof("drawing %d points from %s in node order", len(coords), pointsPath)
		return coords, nil
	}

	tour, err := loadTour(orderPath)
	if err != nil {
		return nil, errors.Wrapf(err, "reading order from %s", orderPath)
	}
	ordered := make([][2]float64, len(tour))
	for i, id := range tour {
		idx := id - 1 // TSPLIB tours are 1-based
		if idx < 0 || idx >= len(coords) {
			return nil, errors.Errorf("%s: tour node %d out of range for %d points", orderPath, id, len(coords))
		}
		ordered[i] = coords[idx]
	}
	d.logger.Infof("drawing %d points from %s ordered by %s", len(ordered), pointsPath, orderPath)
	return ordered, nil
}

// discover resolves a points path and (optional) order path from a file or directory.
func discover(p string) (points, order string, err error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", "", err
	}

	if info.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil {
			return "", "", err
		}
		for _, e := range entries {
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".tsp":
				if points == "" {
					points = filepath.Join(p, e.Name())
				}
			case ".tour", ".cyc":
				if order == "" {
					order = filepath.Join(p, e.Name())
				}
			}
		}
		if points == "" {
			return "", "", errors.Errorf("no .tsp file found in directory %s", p)
		}
		return points, order, nil
	}

	// Single file: resolve its sibling by basename.
	base := strings.TrimSuffix(p, filepath.Ext(p))
	switch strings.ToLower(filepath.Ext(p)) {
	case ".tour", ".cyc":
		order = p
		if points = firstExisting(base + ".tsp"); points == "" {
			return "", "", errors.Errorf("order file %s given but no sibling .tsp found", p)
		}
	default: // treat as the points file; look for a sibling tour
		points = p
		order = firstExisting(base+".tour", base+".cyc")
	}
	return points, order, nil
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// loadNodeCoords parses a TSPLIB NODE_COORD_SECTION ("id x y" rows) into a slice
// ordered by ascending node id.
func loadNodeCoords(path string) ([][2]float64, error) {
	fields, err := readFields(path)
	if err != nil {
		return nil, err
	}
	i := indexOfKeyword(fields, "NODE_COORD_SECTION")
	byID := map[int][2]float64{}
	for i >= 0 && i+2 < len(fields) {
		id, err := strconv.Atoi(fields[i])
		if err != nil {
			break // "EOF" marker or next section
		}
		x, errX := strconv.ParseFloat(fields[i+1], 64)
		y, errY := strconv.ParseFloat(fields[i+2], 64)
		if errX != nil || errY != nil {
			break
		}
		byID[id] = [2]float64{x, y}
		i += 3
	}

	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([][2]float64, len(ids))
	for k, id := range ids {
		out[k] = byID[id]
	}
	return out, nil
}

// loadTour parses a TSPLIB TOUR_SECTION (or a bare list) of node ids, ending at
// -1 or the first non-integer token.
func loadTour(path string) ([]int, error) {
	fields, err := readFields(path)
	if err != nil {
		return nil, err
	}
	start := indexOfKeyword(fields, "TOUR_SECTION")
	if start < 0 {
		start = 0 // headerless bare list
	}
	var ids []int
	for _, f := range fields[start:] {
		n, err := strconv.Atoi(f)
		if err != nil || n == -1 {
			break
		}
		ids = append(ids, n)
	}
	return ids, nil
}

func readFields(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

// indexOfKeyword returns the index just after the given keyword token, or -1.
func indexOfKeyword(fields []string, keyword string) int {
	for i, f := range fields {
		if strings.EqualFold(f, keyword) {
			return i + 1
		}
	}
	return -1
}
