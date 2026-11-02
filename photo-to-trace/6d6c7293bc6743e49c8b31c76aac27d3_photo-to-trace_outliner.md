# Model 6d6c7293bc6743e49c8b31c76aac27d3:photo-to-trace:outliner

Captures photos from a camera and turns them into a CSV of points in the order
they should be drawn, by either of two routes: `trace` outlines colored shapes
on a white background, and `outline` runs the
[portrait-outliner](../portrait-outliner/README.md) Python program, which
prioritizes facial features. Both write the same CSV.

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
  "capture_theta_deg": -0.05,
  "python_bin": "/opt/portrait-outliner/venv/bin/python3",
  "outliner_script": "/opt/portrait-outliner/outline.py",
  "outline_max_strokes": 50,
  "outline_max_points": 3500,
  "plotter": "plotter-1"
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

`outline` and `portrait` shell out to portrait-outliner, so they need to be
told where it lives. Both attributes must be set together, or neither.

| Name                  | Type   | Inclusion | Description                                                                                     |
|-----------------------|--------|-----------|-------------------------------------------------------------------------------------------------|
| `python_bin`          | string | Optional  | Interpreter with portrait-outliner's dependencies installed. Absolute path — see the note below. |
| `outliner_script`     | string | Optional  | Absolute path to portrait-outliner's `outline.py`.                                              |
| `outline_max_strokes` | int    | Optional  | Default cap on pen-down strokes. Unset leaves outline.py's own default (50).                     |
| `outline_max_points`  | int    | Optional  | Default cap on total points. Unset leaves outline.py's default (3500).                           |
| `outline_min_length`  | float  | Optional  | Default: discard off-face strokes shorter than this many pixels. Default 14.                     |
| `outline_face_share`  | float  | Optional  | Default fraction of the stroke budget reserved for the face. Default 0.8.                        |
| `outline_simplify`    | float  | Optional  | Default Douglas-Peucker tolerance in pixels. Default 1.2.                                        |
| `outline_timeout_s`   | float  | Optional  | Give up on one outline run after this long. Defaults to 300.                                     |

Give `python_bin` an absolute path to the virtualenv's interpreter. The module
process does not inherit your shell's `PATH`, so a bare `python3` finds the
system interpreter, which will not have `rembg` or OpenCV.

The `outline_*` attributes are only defaults; every one can be overridden per
call, and any left unset is not passed to outline.py at all.

| Name       | Type   | Inclusion | Description                                                          |
|------------|--------|-----------|------------------------------------------------------------------------|
| `plotter`  | string | Optional  | Name of the pen-plotter generic service `draw` forwards a CSV to.    |

## DoCommand

### `capture`

Moves `move_component` to the configured capture pose, waits for the move to
finish and for the arm to settle (`capture_settle_ms`), then writes one frame
from the configured camera to `out` as a PNG.
With no capture pose configured, it captures without moving the arm.

| Name      | Type   | Inclusion | Description                                                                        |
|-----------|--------|-----------|----------------------------------------------------------------------------------------|
| `out`     | string | Required  | Path of the PNG to write. Created or truncated.                                       |
| `source`  | string | Optional  | Which of the camera's streams to capture, e.g. `color`. Defaults to the first.         |
| `copy_to` | string | Optional  | Also write the identical PNG here, e.g. a folder a data manager syncs to the cloud.    |

Request:

```json
{
  "command": "capture",
  "out": "/data/photo.png",
  "source": "color",
  "copy_to": "/sync/photo.png"
}
```

Response:

```json
{
  "width": 1280,
  "height": 720,
  "out": "/data/photo.png",
  "copy_to": "/sync/photo.png"
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

### `outline`

Runs portrait-outliner over the photo at `path`, writing pen strokes to `out`.
Unlike `trace`, which follows the boundary of flat shapes, this segments the
subject, finds the face, and spends most of the stroke budget on the features —
so it works on an ordinary photo of a person.

| Name              | Type   | Inclusion | Description                                                                    |
|-------------------|--------|-----------|----------------------------------------------------------------------------------|
| `path`            | string | Required  | Portrait photo to outline (PNG or JPEG), on the machine running the module.      |
| `out`             | string | Required  | Path of the CSV to write. Created or truncated.                                  |
| `max_strokes`     | int    | Optional  | Cap on pen-down strokes, overriding `outline_max_strokes`.                       |
| `max_points`      | int    | Optional  | Cap on total points, overriding `outline_max_points`.                            |
| `min_length`      | float  | Optional  | Drop off-face strokes shorter than this, overriding `outline_min_length`.        |
| `face_min_length` | float  | Optional  | The same limit for strokes on the face, where eyebrows are only a few px long.   |
| `face_share`      | float  | Optional  | Fraction of the budget reserved for the face, overriding `outline_face_share`.   |
| `simplify`        | float  | Optional  | Douglas-Peucker tolerance in px, overriding `outline_simplify`.                  |
| `preview`         | string | Optional  | Also render the strokes to this path, drawn exactly as the arm will draw them.   |
| `face_map`        | string | Optional  | Write a heat map of the face detection here. Check it first if a portrait is wrong. |
| `no_mask`         | bool   | Optional  | Skip segmentation and trace the whole frame. Only for an already-flat background. |
| `no_face`         | bool   | Optional  | Weight the whole subject equally. Hair and clothing will outcompete the features. |

Request:

```json
{
  "command": "outline",
  "path": "/opt/viam/photo.png",
  "out": "/opt/viam/strokes.csv",
  "max_strokes": 60,
  "preview": "/opt/viam/preview.png"
}
```

Response:

```json
{
  "strokes": 47,
  "points": 3120,
  "out": "/opt/viam/strokes.csv",
  "stats": "Saved 47 strokes / 3120 points to /opt/viam/strokes.csv\n  image        1280x720 px\n  ..."
}
```

`stats` is portrait-outliner's own report — image size, whether a face was
found and with what confidence, how many strokes were dropped and why. Read it
when a portrait comes out wrong.

The first run is the slow one: `rembg` downloads its segmentation model before
it can start. Warm it up from a shell before the first `outline` call, or raise
`outline_timeout_s`.

### `portrait`

The whole pipeline in one call: move to the capture pose, settle, photograph
the subject to `photo`, then outline it to `out`. It takes every argument
`outline` does, plus:

| Name      | Type   | Inclusion | Description                                                            |
|-----------|--------|-----------|-------------------------------------------------------------------------|
| `photo`   | string | Required  | Path of the PNG to capture to. Kept, so it can be re-outlined.         |
| `out`     | string | Required  | Path of the CSV to write.                                              |
| `source`  | string | Optional  | Which of the camera's streams to capture, e.g. `color`.                |
| `copy_to` | string | Optional  | Also write the captured PNG here, e.g. a cloud-sync folder.            |

Request:

```json
{
  "command": "portrait",
  "photo": "/opt/viam/photo.png",
  "out": "/opt/viam/strokes.csv",
  "source": "color",
  "copy_to": "/sync/photo.png"
}
```

Response:

```json
{
  "width": 1280,
  "height": 720,
  "photo": "/opt/viam/photo.png",
  "strokes": 47,
  "points": 3120,
  "out": "/opt/viam/strokes.csv",
  "stats": "...",
  "copy_to": "/sync/photo.png"
}
```

The photo is left on disk deliberately. Outlining is the part that needs
tuning, and re-running `outline` over a kept photo costs nothing, where
re-running `portrait` asks the subject to sit again.

### `draw`

Forwards a CSV already on disk to the configured `plotter`'s own `draw`
DoCommand — a thin passthrough, so this module needs no knowledge of how the
plotter draws, only that it accepts the same CSV shape `trace`/`outline`/
`portrait` write.

| Name   | Type   | Inclusion | Description                    |
|--------|--------|-----------|----------------------------------|
| `path` | string | Required  | CSV to draw, on the plotter's own machine. |

Request:

```json
{
  "command": "draw",
  "path": "/opt/viam/strokes.csv"
}
```

Response: whatever the plotter's `draw` DoCommand returns.

### `draw_portrait`

The whole pipeline in one call: `portrait` (move, settle, capture, outline)
then `draw` on the CSV it just wrote. Takes every argument `portrait` does.

Request:

```json
{
  "command": "draw_portrait",
  "photo": "/opt/viam/photo.png",
  "out": "/opt/viam/strokes.csv",
  "source": "color"
}
```

Response: `portrait`'s response with one field added — `draw`, holding whatever
the plotter's own `draw` DoCommand returned.

```json
{
  "width": 1280,
  "height": 720,
  "photo": "/opt/viam/photo.png",
  "strokes": 47,
  "points": 3120,
  "out": "/opt/viam/strokes.csv",
  "stats": "...",
  "draw": { }
}
```

`capture`, `outline` and `draw` stay available on their own — `portrait` and
`draw_portrait` are just fixed compositions of them, for when you don't need
to inspect or tune a step in between.

### Output CSV

`trace`, `outline` and `portrait` all write the same file. One point per line,
in draw order. `contour` marks where one shape's outline or one pen stroke ends
and the next begins — points within a contour are consecutive, so a pen should
lift between contours.

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
