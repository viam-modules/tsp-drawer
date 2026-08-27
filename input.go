package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// loadInput reads the ordered (x,y) tour for a "draw" command, from either
// {"command":"draw","path":...}       — a TSPLIB .tsp file on the robot, or
// {"command":"draw","point_array":...} — [[x,y],...] sent straight in the command.
//
// Either way the points are taken IN THE ORDER GIVEN as the draw order. The
// producer's contract: order the rows by the solved tour before sending.
func (d *drawer) loadInput(cmd map[string]interface{}) ([][2]float64, error) {
	path, _ := cmd["path"].(string)

	if raw, ok := cmd["point_array"]; ok && raw != nil {
		tour, err := parsePointArray(raw)
		if err != nil {
			return nil, err
		}
		if len(tour) == 0 {
			return nil, errors.New("'point_array' is empty")
		}
		d.logger.Infof("drawing %d points from point_array", len(tour))
		return tour, nil
	}

	if path == "" {
		return nil, errors.New("draw requires a 'path' to a .tsp file or a 'point_array'")
	}

	tour, err := loadCoords(path)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", path)
	}
	if len(tour) == 0 {
		return nil, errors.Errorf("no NODE_COORD_SECTION points in %s", path)
	}
	d.logger.Infof("drawing %d points from %s", len(tour), path)
	return tour, nil
}

// parsePointArray decodes a [[x,y],...] value out of a DoCommand payload. The
// command arrives as a protobuf Struct, so nested arrays land as []interface{}
// of []interface{} and every number is a float64 — never a Go [][2]float64.
func parsePointArray(raw interface{}) ([][2]float64, error) {
	rows, ok := raw.([]interface{})
	if !ok {
		return nil, errors.Errorf("'point_array' must be a list of [x,y] pairs, got %T", raw)
	}

	tour := make([][2]float64, 0, len(rows))
	for i, row := range rows {
		pair, ok := row.([]interface{})
		if !ok {
			return nil, errors.Errorf("point_array[%d] must be an [x,y] pair, got %T", i, row)
		}
		if len(pair) != 2 {
			return nil, errors.Errorf("point_array[%d] must have 2 values, got %d", i, len(pair))
		}
		x, okX := pair[0].(float64)
		y, okY := pair[1].(float64)
		if !okX || !okY {
			return nil, errors.Errorf("point_array[%d] must hold numbers, got [%T %T]", i, pair[0], pair[1])
		}
		tour = append(tour, [2]float64{x, y})
	}
	return tour, nil
}

// loadCoords reads a TSPLIB NODE_COORD_SECTION ("id x y" rows) and returns the
// coordinates IN FILE ORDER — that order is the draw order.
func loadCoords(path string) ([][2]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	i := indexOfKeyword(fields, "NODE_COORD_SECTION")

	var tour [][2]float64
	for i >= 0 && i+2 < len(fields) {
		if _, err := strconv.Atoi(fields[i]); err != nil {
			break // "EOF" marker or next section keyword ends the coordinates
		}
		x, errX := strconv.ParseFloat(fields[i+1], 64)
		y, errY := strconv.ParseFloat(fields[i+2], 64)
		if errX != nil || errY != nil {
			break
		}
		tour = append(tour, [2]float64{x, y})
		i += 3
	}
	return tour, nil
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
