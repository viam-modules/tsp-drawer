# tsp-drawer

A Viam **generic service** (`viam:tsp-drawer:pen-plotter`) that reads a
TSP-art tour from disk (TSPLIB `.tsp` points + an optional `.tour`/`.cyc` order)
and draws it with a pen by issuing **motion-service** plan requests to a
configured **pen-tip frame**.

Because TSP art is a single continuous closed stroke, a draw is: travel to start →
pen down → trace every point → pen up. No mid-draw pen lifts.

## Build

```bash
go mod tidy
go build -o tsp-drawer .
```

Point your robot config's module `executable_path` at the `tsp-drawer` binary.

## Pen-tip frame

The motion service moves the frame named by `move_component` to each goal, so
**configure a pen-tip frame** (a frame whose origin is the pen tip, parented to
the arm) and set `move_component` to its name. Goal poses are then pen-tip poses.

## Configure the service

```json
{
  "name": "plotter",
  "type": "generic",
  "namespace": "rdk",
  "model": "viam:tsp-drawer:pen-plotter",
  "attributes": {
    "arm": "xarm6",
    "move_component": "pen_tip",
    "motion_service": "builtin",

    "origin_x_mm": 300.0,
    "origin_y_mm": -150.0,
    "mm_per_unit_x": 0.01,
    "mm_per_unit_y": -0.01,

    "z_draw_mm": 12.0,
    "z_lift_mm": 30.0,

    "input_path": "/data/tours",
    "rdp_epsilon": 5.0
  }
}
```

`arm` and `motion_service` (default `"builtin"`) are declared as dependencies.

### Attributes

| Attribute | Meaning | Default |
|---|---|---|
| `arm` | Arm resource name (used for `stop`). **Required.** | — |
| `move_component` | Pen-tip frame the motion service moves to each goal. **Required.** | — |
| `motion_service` | Motion service name. | `builtin` |
| `reference_frame` | Frame the goal poses are expressed in. | `world` |
| `input_path` | Default file or directory to read a tour from when `draw` gives no path. | — |
| `origin_x_mm`, `origin_y_mm` | Robot base-frame mm that input point `(0,0)` maps to. | `0` |
| `mm_per_unit_x` | Scale, input-x-unit → mm. **Required, non-zero.** | — |
| `mm_per_unit_y` | Scale, input-y-unit → mm. Use a **negative** value to flip the image upright (TSPLIB is y-up; images are y-down). | `mm_per_unit_x` |
| `z_draw_mm` | Pen tip touching the paper (robot base-frame Z). | `0` |
| `z_lift_mm` | Travel/idle height, pen up. | `0` |
| `pen_ox`, `pen_oy`, `pen_oz`, `pen_theta_deg` | Fixed pen orientation as an orientation vector (degrees). Default points straight down. | `(0,0,-1,0)` |
| `line_tolerance_mm` | Max deviation from the straight line for pen-down segments. | `1.0` |
| `orientation_tolerance_degs` | Max pen-orientation deviation during moves. | `5.0` |
| `rdp_epsilon` | Ramer–Douglas–Peucker simplification in **input units**; `0` disables. Collapses near-collinear runs into fewer motion calls. | `0` |

### Calibrating `origin_*` / `mm_per_unit_*` / `z_draw_mm`

1. Jog the pen tip to touch the paper where you want input `(0,0)` to land → that
   XY is `origin_x_mm` / `origin_y_mm`, and the Z there is `z_draw_mm`.
2. Jog to a second input point a known number of units away → `mm_per_unit_* = Δmm / Δunits`.
3. `z_lift_mm = z_draw_mm + ~15mm`.

## Draw a tour

Call `DoCommand` on the service. Point it at input files; no coordinate data is
passed in the command itself.

```json
{ "command": "draw", "path": "/data/tours/mona-lisa" }
```

`path` (or the configured `input_path`) may be:

- a **directory** — scanned for a `.tsp` (points) and a `.tour`/`.cyc` (order);
- a **`.tsp` file** — its sibling `.tour`/`.cyc` is used for order if present;
- a **`.tour`/`.cyc` file** — its sibling `.tsp` is used for points.

Or give the two explicitly:

```json
{ "command": "draw", "points_path": "/data/mona.tsp", "order_path": "/data/mona.tour" }
```

- Points are read from the TSPLIB `NODE_COORD_SECTION` (`id x y` rows).
- Order is read from a TSPLIB `TOUR_SECTION` (1-based node ids, `-1`-terminated),
  or a bare whitespace-separated list of ids. With no order file, points are
  drawn in ascending node-id order.

Returns `{ "drew_points": N }` on success.

## Stop

```json
{ "command": "stop" }
```

Cancels an in-progress draw and calls `arm.Stop`.

## Notes

- **Scale.** The motion planner runs a full plan per segment; fine for a modest
  tour, slow for a very dense one. Downsample upstream and/or set `rdp_epsilon`.
- **No table obstacle.** Z is commanded explicitly. If you later add a `WorldState`
  tabletop, set it slightly *below* `z_draw_mm` or the drawing pen reads as a collision.
