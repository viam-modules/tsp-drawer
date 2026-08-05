package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// loadInput reads the ordered (x,y) tour for a "draw" command.
//
// The command gives the path to a TSPLIB .tsp file: {"command":"draw","path":...}.
// Its NODE_COORD_SECTION rows are taken IN FILE ORDER as the draw order. The
// producer's contract: write the .tsp with rows already ordered by the solved tour.
func (d *drawer) loadInput(cmd map[string]interface{}) ([][2]float64, error) {
	path, _ := cmd["path"].(string)
	if path == "" {
		return nil, errors.New("draw requires a 'path' to a .tsp file")
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
