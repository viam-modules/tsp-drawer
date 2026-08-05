# tsp-drawer

A Viam **generic service** (`viam:tsp-drawer:pen-plotter`) that reads a
TSP-art tour from disk (a single TSPLIB `.tsp` file whose points are in draw order)
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

    "area_x_mm": 300.0,
    "area_y_mm": -150.0,
    "area_width_mm": 200.0,
    "area_height_mm": 200.0,

    "z_draw_mm": 12.0,
    "z_lift_mm": 30.0
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
| `area_x_mm`, `area_y_mm` | Min-x/min-y corner of the drawing area in the reference frame (mm; `world` by default); the area extends in +x/+y from here. | `0` |
| `area_width_mm`, `area_height_mm` | Size of the drawing area (mm). The tour is uniformly scaled to fit inside (aspect preserved) and centered. **Both required, positive.** | — |
| `z_draw_mm` | Pen tip touching the paper (Z in the reference frame). | `0` |
| `z_lift_mm` | Travel/idle height, pen up. | `0` |
| `pen_ox`, `pen_oy`, `pen_oz`, `pen_theta_deg` | Fixed pen orientation as an orientation vector (degrees). Default points straight down. | `(0,0,-1,0)` |
| `line_tolerance_mm` | Max deviation from the straight line for pen-down segments. | `1.0` |
| `orientation_tolerance_degs` | Max pen-orientation deviation during moves. | `5.0` |
| `rdp_epsilon` | Ramer–Douglas–Peucker simplification tolerance in **mm on the paper**; `0` disables. Drops points whose removal moves the drawn line less than this, cutting motion-plan requests. Recommended for dense tours. | `0` |

### Setting the drawing area and Z

All measured on the paper with a ruler — no need to know the tour's coordinate units.

1. Decide the rectangle on the paper where the drawing should go. Jog the pen tip to
   one corner → that XY is `area_x_mm` / `area_y_mm`; its width/height in mm are
   `area_width_mm` / `area_height_mm`. The tour is scaled to fit inside and centered.
2. Jog the pen tip down until it touches the paper → that Z is `z_draw_mm`.
3. `z_lift_mm = z_draw_mm + ~15mm`.

> Orientation (mirrored/upside-down) depends on how the paper sits under the arm and
> how the `.tsp` is written; there's no flip option — orient the paper (or emit the
> `.tsp` accordingly) so it comes out right.

## Draw a tour

Give the path to a `.tsp` file. The draw runs in the background and the command
returns immediately; poll `status` for progress. No coordinate data is passed in
the command.

```json
{ "command": "draw", "path": "/data/mona-lisa.tsp" }
```

Returns `{ "started": true, "points": N }` (N = points after simplification). A
second `draw` while one is running is rejected — send `stop` first.

### Tour contract

The `.tsp` is a TSPLIB file. Its `NODE_COORD_SECTION` (`id x y` rows) is read **in
file order**, and that order is the draw order — so the producer writes the `.tsp`
with its rows already ordered by the solved tour. The `id` column is ignored. No
separate order/tour file is used.

```
NODE_COORD_SECTION
1 961 10768      <- drawn first
2 14991 8390     <- drawn second
3 13616 12541    <- drawn third
...
EOF
```

## Status

```json
{ "command": "status" }
```

Returns `{ "running": bool, "drawn": N, "total": M }`, plus `"error"` if the last
draw ended early.

## Stop

```json
{ "command": "stop" }
```

Cancels the in-progress draw, **lifts the pen** to `z_lift_mm`, and calls `arm.Stop`.

## Notes

- **Scale.** The motion planner runs a full plan per point, so a very dense tour is
  slow. Set `rdp_epsilon` (mm) to simplify; poll `status` for progress.
- **Table obstacle.** Add the table as a static geometry in the robot's **frame
  system** — the motion service uses it automatically (no `WorldState` needed). Set
  its top surface slightly *below* `z_draw_mm`, or the pen touching the paper reads
  as a collision, and give the pen-tip frame a small geometry so the tip is planned.
- **Interrupted draws lift the pen.** Any early exit (stop, `Close`, or a planning
  error) retracts to `z_lift_mm` best-effort before the goroutine ends.
