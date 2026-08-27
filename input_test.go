package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "points.csv")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadContourCSV_GroupsByContour(t *testing.T) {
	// Two contours: pen lifts between the last point of contour 0 and the first of 1.
	csv := "contour,x,y\n" +
		"0,0,0\n" +
		"0,1,1\n" +
		"0,2,0\n" +
		"1,10,10\n" +
		"1,11,11\n"
	strokes, err := loadContourCSV(writeTemp(t, csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 2 {
		t.Fatalf("want 2 strokes, got %d", len(strokes))
	}
	if len(strokes[0]) != 3 || len(strokes[1]) != 2 {
		t.Fatalf("want stroke lengths [3 2], got [%d %d]", len(strokes[0]), len(strokes[1]))
	}
	if strokes[0][0] != [2]float64{0, 0} || strokes[1][0] != [2]float64{10, 10} {
		t.Fatalf("points out of file order: %v", strokes)
	}
}

func TestLoadContourCSV_SingleContour(t *testing.T) {
	csv := "contour,x,y\n0,5,5\n0,6,6\n0,7,5\n"
	strokes, err := loadContourCSV(writeTemp(t, csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 1 {
		t.Fatalf("want 1 stroke, got %d", len(strokes))
	}
	if countPoints(strokes) != 3 {
		t.Fatalf("want 3 points, got %d", countPoints(strokes))
	}
}

func TestLoadContourCSV_NoHeader(t *testing.T) {
	// A missing header must not eat a real data row.
	csv := "0,5,5\n0,6,6\n1,7,5\n"
	strokes, err := loadContourCSV(writeTemp(t, csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 2 || countPoints(strokes) != 3 {
		t.Fatalf("want 2 strokes / 3 points, got %d / %d", len(strokes), countPoints(strokes))
	}
}
