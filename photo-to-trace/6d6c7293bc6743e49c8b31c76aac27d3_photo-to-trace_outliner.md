# Model 6d6c7293bc6743e49c8b31c76aac27d3:photo-to-trace:outliner

Traces an image of colored shapes on a white background and writes the outline
points to a CSV, in the order they should be drawn.

## Configuration

This model takes no configuration attributes.

```json
{}
```

## DoCommand

### `trace`

| Name       | Type   | Inclusion | Description                                                            |
|------------|--------|-----------|------------------------------------------------------------------------|
| `path`     | string | Required  | Image to trace (PNG or JPEG), on the machine running the module.       |
| `out`      | string | Required  | Path of the CSV to write. Created or truncated.                        |
| `thresh`   | int    | Optional  | How far from white a pixel must be to count as ink, 0-255. Default 40. |
| `min`      | int    | Optional  | Discard contours shorter than this many pixels. Default 8.             |
| `simplify` | float  | Optional  | Douglas-Peucker tolerance in pixels. 0 (default) keeps every pixel.     |

Request:

```json
{
  "command": "trace",
  "path": "/data/shape.png",
  "out": "/data/shape.csv",
  "simplify": 1.5
}
```

Response:

```json
{
  "width": 2000,
  "height": 2000,
  "contours": 1,
  "points": 260,
  "out": "/data/shape.csv"
}
```

### Output CSV

One point per line, in draw order. `contour` marks where one shape's outline
ends and the next begins — points within a contour are consecutive, so a pen
should lift between contours.

```csv
contour,x,y
0,820,240
0,833,241
0,836,245
```

Coordinates are pixels in image space: `x` right, `y` down from the top-left.

## CLI

`cmd/cli` runs the same command locally:

```bash
go run ./cmd/cli -out points.csv -simplify 1.5 image.png
```
