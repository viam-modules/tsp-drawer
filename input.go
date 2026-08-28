package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// stroke is one continuous pen-down polyline. A drawing is an ordered list of
// strokes; the pen lifts, travels, and comes back down between consecutive strokes.
type stroke = [][2]float64

// loadInput reads the ordered strokes for a "draw" command from a contour CSV:
//
//	{"command":"draw","path":"<points.csv>"}
//
// The CSV is a "contour,x,y" header then one row per point in draw order. The file
// itself specifies both the points and the order to visit them, so no separate tour
// is needed. Consecutive rows sharing a contour value form one stroke; each change in
// the contour column is a pen-up / travel / pen-down.
func (d *drawer) loadInput(cmd map[string]interface{}) ([]stroke, error) {
	path, _ := cmd["path"].(string)

	if raw, ok := cmd["point_array"]; ok && raw != nil {
		strokes, err := parsePointArray(raw)
		if err != nil {
			return nil, err
		}
		if countPoints(strokes) == 0 {
			return nil, errors.New("'point_array' is empty")
		}
		d.logger.Infof("drawing %d strokes (%d points) from point_array in the order given",
			len(strokes), countPoints(strokes))
		return strokes, nil
	}

	if path == "" {
		return nil, errors.New("draw requires a 'path' to a contour CSV file")
	}

	strokes, err := loadContourCSV(path)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", path)
	}
	if len(strokes) == 0 {
		return nil, errors.Errorf("no points in %s", path)
	}
	d.logger.Infof("drawing %d strokes (%d points) from %s in file order",
		len(strokes), countPoints(strokes), path)
	return strokes, nil
}

// parsePointArray decodes the "point_array" value of a DoCommand payload into
// strokes. Two shapes are accepted, matching what a caller naturally has to hand:
//
//	[[x,y], ...]          -> one continuous stroke
//	[[[x,y], ...], ...]   -> one entry per stroke, i.e. the CSV's contour grouping
//
// The command arrives as a protobuf Struct, so nested arrays land as []interface{}
// of []interface{} and every number is a float64 — never a Go [][2]float64.
func parsePointArray(raw interface{}) ([]stroke, error) {
	rows, ok := raw.([]interface{})
	if !ok {
		return nil, errors.Errorf("'point_array' must be a list of [x,y] pairs or a list of strokes, got %T", raw)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// Disambiguate on the first element: a pair of numbers means the flat shape,
	// anything nested deeper means the grouped shape.
	if isPointPair(rows[0]) {
		s, err := parseStroke(rows, "point_array")
		if err != nil {
			return nil, err
		}
		return []stroke{s}, nil
	}

	strokes := make([]stroke, 0, len(rows))
	for i, row := range rows {
		pts, ok := row.([]interface{})
		if !ok {
			return nil, errors.Errorf("point_array[%d] must be a list of [x,y] pairs, got %T", i, row)
		}
		s, err := parseStroke(pts, fmt.Sprintf("point_array[%d]", i))
		if err != nil {
			return nil, err
		}
		if len(s) > 0 { // an empty stroke would be a spurious pen lift
			strokes = append(strokes, s)
		}
	}
	return strokes, nil
}

// parseStroke decodes one flat list of [x,y] pairs. label names the list in errors.
func parseStroke(rows []interface{}, label string) (stroke, error) {
	s := make(stroke, 0, len(rows))
	for i, row := range rows {
		pair, ok := row.([]interface{})
		if !ok {
			return nil, errors.Errorf("%s[%d] must be an [x,y] pair, got %T", label, i, row)
		}
		if len(pair) != 2 {
			return nil, errors.Errorf("%s[%d] must have 2 values, got %d", label, i, len(pair))
		}
		x, okX := pair[0].(float64)
		y, okY := pair[1].(float64)
		if !okX || !okY {
			return nil, errors.Errorf("%s[%d] must hold numbers, got [%T %T]", label, i, pair[0], pair[1])
		}
		s = append(s, [2]float64{x, y})
	}
	return s, nil
}

// isPointPair reports whether v is a 2-element list of numbers, i.e. an [x,y].
func isPointPair(v interface{}) bool {
	pair, ok := v.([]interface{})
	if !ok || len(pair) != 2 {
		return false
	}
	_, okX := pair[0].(float64)
	_, okY := pair[1].(float64)
	return okX && okY
}

// loadContourCSV reads the "contour,x,y" format: a header line then one row per point
// in draw order. Consecutive rows sharing a contour value form one stroke; each change
// in the contour column is a pen-up / travel / pen-down. Points keep their file order,
// so the file itself specifies the drawing order.
func loadContourCSV(path string) ([]stroke, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate ragged rows; we validate the fields we use
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	var (
		strokes  []stroke
		cur      stroke
		prevC    string
		havePrev bool
	)
	for _, rec := range records {
		if len(rec) < 3 {
			continue // blank or short line
		}
		c := strings.TrimSpace(rec[0])
		x, errX := strconv.ParseFloat(strings.TrimSpace(rec[1]), 64)
		y, errY := strconv.ParseFloat(strings.TrimSpace(rec[2]), 64)
		if errX != nil || errY != nil {
			continue // header row ("contour,x,y") or any non-numeric line
		}

		// A change in the contour column starts a new stroke (pen lift between them).
		if havePrev && c != prevC && len(cur) > 0 {
			strokes = append(strokes, cur)
			cur = nil
		}
		cur = append(cur, [2]float64{x, y})
		prevC, havePrev = c, true
	}
	if len(cur) > 0 {
		strokes = append(strokes, cur)
	}
	return strokes, nil
}

// countPoints totals the points across all strokes.
func countPoints(strokes []stroke) int {
	n := 0
	for _, s := range strokes {
		n += len(s)
	}
	return n
}
