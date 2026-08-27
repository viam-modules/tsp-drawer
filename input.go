package main

import (
	"encoding/csv"
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
