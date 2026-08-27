# portrait-outliner

Converts a portrait photo into a small set of pen strokes suitable for a
drawing robot / pen plotter, prioritizing facial features (eyes, brows, nose,
mouth) so the result stays recognizable within a limited stroke budget.

## Pipeline

1. **Segment** (`segment.py`) — uses `rembg` to matte the subject from the
   background, producing a boundary mask (silhouette) and an eroded interior
   mask (bounds which interior edges are kept).
2. **Locate face** (`face.py`) — runs YuNet face detection to find eyes,
   brows, nose, and mouth, and builds a per-pixel importance/weight map from
   the landmarks (falls back to mask proportions if no face is detected).
3. **Trace edges** (`strokes.py`) — CLAHE contrast boost + bilateral blur +
   Canny edge detection + skeletonization, then traces the skeleton into
   polyline strokes. Silhouette, simplification (Douglas-Peucker), Chaikin
   smoothing, and stroke ordering also live here.
4. **Budget & write** (`outline.py`) — filters short strokes, spends most of
   the stroke/point budget on the face (`--face-share`), drops the shortest
   strokes off-face first, and writes the result as CSV.

`remove_bg.py` is a standalone utility that uses the same segmentation to
composite a subject onto a flat background (white/black/grey/transparent) —
useful for producing a clean input photo before outlining, or as its own
output.

## Alternative pipeline: sketch.py (XDoG/FDoG + hatching)

`sketch.py` is a second, swappable edge-generation pipeline meant to be A/B
tested against `outline.py` on the same photos. It produces the same
`contour,x,y` CSV, so everything downstream (the drawing robot) works with
either; it can additionally write a layered SVG for plotter tooling.

Instead of Canny it runs:

1. **Bilateral preprocess** (`lineart.preprocess`) — suppresses pores/noise
   that Canny turns into false edges.
2. **XDoG or flow-based DoG** (`lineart.xdog` / `lineart.edge_tangent_flow` +
   `lineart.fdog`) — FDoG (the default) filters along a smoothed tangent
   field, producing long coherent strokes that read as drawn rather than
   detected.
3. **Landmark-guided thresholds** (`lineart.feature_tau_map`) — the YuNet
   landmarks locally relax the binarization threshold over eyes, brows, nose
   and mouth so faint-but-important feature edges survive.
4. **Highlight suppression** (`lineart.suppress_highlights`) — drops lines in
   the subject's brightest regions (illustrators lift the pen where light
   hits), sparing the feature zones.
5. **Luminance-driven hatching** (`hatching.py`) — shading as a separate
   stroke layer: streamlines traced along the same flow field, spaced
   inversely to darkness, so value comes from line density and strokes curve
   with the form. `--cross-hatch` adds a perpendicular pass in the darkest
   regions.
6. **Vectorize + merge** — structural edges are skeletonized and traced with
   the same machinery as `outline.py`; hatching is born vector. Both layers
   are budgeted, ordered, and written as CSV (and `--svg`, with structural
   and hatching as separate SVG layers).

```bash
python3 sketch.py input.jpg output.csv --preview preview.png --svg output.svg
```

Useful flags (see `--help`): `--engine xdog|fdog`, `--no-hatch`,
`--no-landmarks`, `--highlight-percentile`, `--tau` / `--feature-tau`,
`--hatch-spacing` / `--hatch-min` / `--max-hatch-strokes`, `--bold-offset`
(duplicates important contours sub-mm offset so they plot bolder), and
`--debug-dir DIR`, which writes every intermediate stage as an image so each
stage can be judged independently.

The SVG is written with vpype/Inkscape-compatible layers, so plotter-side
optimization can be chained on afterwards, e.g.:

```bash
vpype read output.svg linesort linemerge --tolerance 0.3mm write plot.svg
```

## Setup

```bash
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
```

The YuNet face model is expected at `models/face_detection_yunet_2023mar.onnx`
(see the URL in `face.py` if it needs to be re-downloaded). The `rembg`
segmentation model (`u2net_human_seg` by default) downloads automatically on
first use.

## Usage

```bash
python3 outline.py input.jpg output.csv --preview preview.png
```

**Output format:** `output.csv` has rows `contour,x,y` — one row per point,
where `contour` increments each time a new stroke starts. Read top to bottom,
draw each contour as a connected polyline, and lift the pen whenever
`contour` changes. Coordinates are pixels in the input image's frame (x
right, y down).

Useful flags (see `python3 outline.py --help` for the full list):

- `--max-strokes` / `--max-points` — cap on total strokes / points in the
  output.
- `--face-share` — fraction of the stroke budget reserved for the face
  (default 0.8), so hair/clothing only compete for what's left.
- `--min-length` / `--face-min-length` — drop strokes shorter than this many
  pixels (a separate, lower limit applies on the face so eyebrows/eyelids
  survive).
- `--simplify` — Douglas-Peucker tolerance in pixels.
- `--no-mask` — skip segmentation and trace the whole frame (only sensible
  on an already-flat-background photo).
- `--no-face` — disable face-priority weighting, treating the whole subject
  equally.
- `--face-map PATH` — write a heat-map overlay showing face detection and
  weighting; the first thing to check if a portrait comes out wrong.

`remove_bg.py` usage:

```bash
python3 remove_bg.py input.jpg output.png --color white
```
