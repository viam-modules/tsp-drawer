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

// pbPairs builds the []interface{}-of-[]interface{}-of-float64 shape a protobuf
// Struct actually delivers, which is what parsePointArray has to cope with.
func pbPairs(pts ...[2]float64) []interface{} {
	out := make([]interface{}, len(pts))
	for i, p := range pts {
		out[i] = []interface{}{p[0], p[1]}
	}
	return out
}

func TestParsePointArray_FlatIsOneStroke(t *testing.T) {
	strokes, err := parsePointArray(pbPairs([2]float64{0, 0}, [2]float64{1, 1}, [2]float64{2, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 1 || countPoints(strokes) != 3 {
		t.Fatalf("want 1 stroke / 3 points, got %d / %d", len(strokes), countPoints(strokes))
	}
	if strokes[0][0] != [2]float64{0, 0} || strokes[0][2] != [2]float64{2, 0} {
		t.Fatalf("points out of order: %v", strokes)
	}
}

func TestParsePointArray_GroupedIsManyStrokes(t *testing.T) {
	raw := []interface{}{
		pbPairs([2]float64{0, 0}, [2]float64{1, 1}),
		pbPairs([2]float64{10, 10}, [2]float64{11, 11}, [2]float64{12, 10}),
	}
	strokes, err := parsePointArray(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 2 {
		t.Fatalf("want 2 strokes, got %d", len(strokes))
	}
	if len(strokes[0]) != 2 || len(strokes[1]) != 3 {
		t.Fatalf("want stroke lengths [2 3], got [%d %d]", len(strokes[0]), len(strokes[1]))
	}
	if strokes[1][0] != [2]float64{10, 10} {
		t.Fatalf("second stroke starts at %v", strokes[1][0])
	}
}

func TestParsePointArray_GroupedSkipsEmptyStrokes(t *testing.T) {
	// An empty stroke would otherwise become a pen lift to nowhere.
	raw := []interface{}{
		pbPairs([2]float64{0, 0}, [2]float64{1, 1}),
		[]interface{}{},
		pbPairs([2]float64{5, 5}, [2]float64{6, 6}),
	}
	strokes, err := parsePointArray(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 2 || countPoints(strokes) != 4 {
		t.Fatalf("want 2 strokes / 4 points, got %d / %d", len(strokes), countPoints(strokes))
	}
}

func TestParsePointArray_Rejects(t *testing.T) {
	cases := map[string]interface{}{
		"not a list":        "0,0",
		"pair too short":    []interface{}{[]interface{}{1.0}},
		"pair too long":     []interface{}{[]interface{}{1.0, 2.0, 3.0}},
		"non-numeric pair":  []interface{}{[]interface{}{"1", "2"}},
		"scalars not pairs": []interface{}{1.0, 2.0},
	}
	for name, raw := range cases {
		if _, err := parsePointArray(raw); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}

func TestParsePointArray_Empty(t *testing.T) {
	strokes, err := parsePointArray([]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if countPoints(strokes) != 0 {
		t.Fatalf("want 0 points, got %d", countPoints(strokes))
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
