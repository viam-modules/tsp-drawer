# Model 6d6c7293bc6743e49c8b31c76aac27d3:photo-to-trace:outliner

Captures photos from a camera, and traces an image of colored shapes on a white
background into a CSV of outline points, in the order they should be drawn.

## Configuration

```json
{
  "camera": "realsense",
  "move_component": "pen-tip-frame",
  "capture_x_mm": 799.94,
  "capture_y_mm": -119.62,
  "capture_z_mm": 349.7,
  "capture_ox": 1,
  "capture_oy": 0,
  "capture_oz": 0,
  "capture_theta_deg": -0.05
}
```

### Attributes

| Name                | Type   | Inclusion | Description                                                                          |
|---------------------|--------|-----------|--------------------------------------------------------------------------------------|
| `camera`            | string | Optional  | Name of the camera `capture` grabs frames from. Omit for a trace-only service.        |
| `move_component`    | string | Optional  | Frame moved to the capture pose. Required if a capture pose is set.                   |
| `motion_service`    | string | Optional  | Motion service name. Defaults to `builtin`.                                           |
| `reference_frame`   | string | Optional  | Frame the capture pose is expressed in. Defaults to `world`.                          |
| `capture_x_mm`      | float  | Optional  | Capture position, mm in the reference frame.                                          |
| `capture_y_mm`      | float  | Optional  | Capture position, mm.                                                                 |
| `capture_z_mm`      | float  | Optional  | Capture position, mm.                                                                 |
| `capture_ox`        | float  | Optional  | Capture orientation vector. Defaults to `(0, 0, -1)`, pointing straight down.          |
| `capture_oy`        | float  | Optional  | Capture orientation vector.                                                           |
| `capture_oz`        | float  | Optional  | Capture orientation vector.                                                           |
| `capture_theta_deg` | float  | Optional  | Capture orientation angle in degrees.                                                 |
| `capture_settle_ms` | float  | Optional  | Wait after the arm reaches the pose before capturing, in ms. Defaults to 500; 0 skips. |

All three of `capture_x_mm`, `capture_y_mm` and `capture_z_mm` must be set for
the arm to move. With any of them missing, `capture` grabs a frame from wherever
the arm already is and no motion service is required.

## DoCommand

### `capture`

Moves `move_component` to the configured capture pose, waits for the move to
finish and for the arm to settle (`capture_settle_ms`), then writes one frame
from the configured camera to `out` as a PNG.
With no capture pose configured, it captures without moving the arm.

| Name     | Type   | Inclusion | Description                                                                     |
|----------|--------|-----------|---------------------------------------------------------------------------------|
| `out`    | string | Required  | Path of the PNG to write. Created or truncated.                                 |
| `source` | string | Optional  | Which of the camera's streams to capture, e.g. `color`. Defaults to the first.   |

Request:

```json
{
  "command": "capture",
  "out": "/data/photo.png",
  "source": "color"
}
```

Response:

```json
{
  "width": 1280,
  "height": 720,
  "out": "/data/photo.png"
}
```

A camera with more than one stream — a RealSense reports colour and depth —
returns whichever it lists first unless `source` names the one you want.

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
