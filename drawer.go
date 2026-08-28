package main

import (
	"context"
	"sync"
	"time"

	"github.com/golang/geo/r3"
	"github.com/pkg/errors"

	"go.viam.com/rdk/components/arm"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/motionplan"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/spatialmath"
)

// Model is this module's model triple: namespace, family, model name.
var Model = resource.NewModel("viam", "tsp-drawer", "pen-plotter")

// Config holds the module's configured attributes. Everything here is drawing
// geometry and defaults; the tour itself is supplied per-call to DoCommand.
type Config struct {
	// Arm is the arm resource name to drive (e.g. "xarm6").
	Arm string `json:"arm"`
	// MotionService is the motion service name; defaults to "builtin".
	MotionService string `json:"motion_service"`
	// MoveComponent is the configured pen-tip frame the motion service moves to each
	// goal, so the TIP (not the flange) tracks the poses. Required.
	MoveComponent string `json:"move_component"`
	// ReferenceFrame is the frame the goal poses are expressed in. Defaults to "world".
	ReferenceFrame string `json:"reference_frame"`

	// Drawing area on the paper, in the reference frame (mm; reference_frame, default
	// "world"). (area_x_mm, area_y_mm) is the min-x/min-y corner; the area extends in
	// +x/+y over area_width_mm x area_height_mm. The tour's bounding box is uniformly
	// scaled to fit inside (aspect ratio preserved) and centered, so it may letterbox.
	AreaXMM      float64 `json:"area_x_mm"`
	AreaYMM      float64 `json:"area_y_mm"`
	AreaWidthMM  float64 `json:"area_width_mm"`
	AreaHeightMM float64 `json:"area_height_mm"`

	// Pen Z heights in the reference frame (mm).
	ZDrawMM float64 `json:"z_draw_mm"` // pen tip touching the paper
	ZLiftMM float64 `json:"z_lift_mm"` // travel/idle height (pen up)

	// Fixed pen orientation as an orientation vector in DEGREES.
	// Default (0,0,-1) => tool +Z points straight down (pen down).
	PenOX    float64 `json:"pen_ox"`
	PenOY    float64 `json:"pen_oy"`
	PenOZ    float64 `json:"pen_oz"`
	PenTheta float64 `json:"pen_theta_deg"`

	// Motion-planning tolerances for the linear/orientation constraints.
	LineToleranceMM          float64 `json:"line_tolerance_mm"`          // default 1.0
	OrientationToleranceDegs float64 `json:"orientation_tolerance_degs"` // default 5.0

	// RDPEpsilon downsamples the tour with Ramer–Douglas–Peucker AFTER fitting it to
	// the drawing area, so epsilon is a deviation tolerance in MILLIMETERS on the paper.
	// 0 disables. Collapses near-collinear runs into far fewer motion plan requests.
	RDPEpsilon float64 `json:"rdp_epsilon"`
}

// Validate checks required fields and declares the arm + motion service as dependencies.
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if cfg.Arm == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "arm")
	}
	if cfg.MoveComponent == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "move_component")
	}
	if cfg.AreaWidthMM <= 0 || cfg.AreaHeightMM <= 0 {
		return nil, nil, resource.NewConfigValidationError(path, errors.New("area_width_mm and area_height_mm must be positive"))
	}
	motionName := cfg.MotionService
	if motionName == "" {
		motionName = "builtin"
	}
	return []string{arm.Named(cfg.Arm).String(), motion.Named(motionName).String()}, nil, nil
}

// normalized returns a copy with defaults filled in.
func (cfg Config) normalized() Config {
	if cfg.MotionService == "" {
		cfg.MotionService = "builtin"
	}
	if cfg.ReferenceFrame == "" {
		cfg.ReferenceFrame = referenceframe.World
	}
	if cfg.PenOX == 0 && cfg.PenOY == 0 && cfg.PenOZ == 0 {
		cfg.PenOZ = -1 // straight down
	}
	if cfg.LineToleranceMM == 0 {
		cfg.LineToleranceMM = 1.0
	}
	if cfg.OrientationToleranceDegs == 0 {
		cfg.OrientationToleranceDegs = 5.0
	}
	return cfg
}

type drawer struct {
	resource.Named
	logger logging.Logger

	// Set once at construction. The module rebuilds the resource (re-runs the
	// constructor) on any config change, so these are immutable and need no lock.
	cfg    Config
	arm    arm.Arm
	motion motion.Service

	// Background-draw state, guarded by mu. A draw runs in its own goroutine so
	// DoCommand("draw") returns immediately.
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc // cancels the in-flight draw
	total   int                // points in the current/last draw
	done    int                // points reached so far
	lastErr error              // error from the last finished draw, if any
	wg      sync.WaitGroup     // tracks the draw goroutine
}

func newDrawer(
	_ context.Context,
	deps resource.Dependencies,
	conf resource.Config,
	logger logging.Logger,
) (resource.Resource, error) {
	cfg, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return nil, err
	}
	norm := cfg.normalized()

	a, err := typedDep[arm.Arm](deps, arm.Named(norm.Arm))
	if err != nil {
		return nil, err
	}
	m, err := typedDep[motion.Service](deps, motion.Named(norm.MotionService))
	if err != nil {
		return nil, err
	}

	return &drawer{
		Named:  conf.ResourceName().AsNamed(),
		logger: logger,
		cfg:    norm,
		arm:    a,
		motion: m,
	}, nil
}

func (d *drawer) Close(context.Context) error {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel()
	}
	d.mu.Unlock()
	d.wg.Wait() // let the draw goroutine unwind (and lift the pen) before returning
	return nil
}

// typedDep fetches a dependency by resource name and asserts its interface type.
func typedDep[T resource.Resource](deps resource.Dependencies, name resource.Name) (T, error) {
	var zero T
	res, ok := deps[name]
	if !ok {
		return zero, errors.Errorf("missing dependency: %s", name)
	}
	typed, ok := res.(T)
	if !ok {
		return zero, errors.Errorf("dependency %s is not of the expected type", name)
	}
	return typed, nil
}

// DoCommand is the module's whole API. Supported commands:
//
//	{"command": "draw", "path": "<contour .csv>"}  -> starts a draw
//	{"command": "draw", "point_array": [[x,y],...]} -> same, points sent in the command
//	{"command": "status"}                          -> progress of the current/last draw
//	{"command": "stop"}                            -> cancels the draw and lifts the pen
func (d *drawer) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, _ := cmd["command"].(string)
	switch name {
	case "draw":
		return d.doDraw(cmd)
	case "status":
		return d.doStatus()
	case "stop":
		return d.doStop(ctx)
	case "":
		return nil, errors.New("missing 'command' (expected \"draw\", \"status\", or \"stop\")")
	default:
		return nil, errors.Errorf("unknown command %q", name)
	}
}

func (d *drawer) doStop(ctx context.Context) (map[string]interface{}, error) {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := d.arm.Stop(ctx, nil); err != nil {
		return nil, err
	}
	return map[string]interface{}{"stopped": true}, nil
}

func (d *drawer) doDraw(cmd map[string]interface{}) (map[string]interface{}, error) {
	strokes, err := d.loadInput(cmd)
	if err != nil {
		return nil, err
	}
	if countPoints(strokes) == 0 {
		return nil, errors.New("no points to draw")
	}

	// Fit the drawing into the area (input units -> reference-frame mm) using one
	// shared transform across all strokes, then simplify each stroke in mm so
	// rdp_epsilon is a physical tolerance on the paper.
	penStrokes := fitStrokesToArea(strokes, d.cfg)
	if d.cfg.RDPEpsilon > 0 {
		for i := range penStrokes {
			penStrokes[i] = rdp(penStrokes[i], d.cfg.RDPEpsilon)
		}
	}
	total := countPoints(penStrokes)

	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return nil, errors.New(`a draw is already in progress; send "stop" first`)
	}
	// The draw outlives this RPC, so base its context on Background, not the request.
	ctx, cancel := context.WithCancel(context.Background())
	d.running, d.cancel, d.total, d.done, d.lastErr = true, cancel, total, 0, nil
	d.wg.Add(1)
	d.mu.Unlock()

	go d.runDraw(ctx, penStrokes)

	return map[string]interface{}{"started": true, "points": total, "strokes": len(penStrokes)}, nil
}

// runDraw traces the strokes in the background. For each stroke: travel to its first
// point with the pen up, lower the pen, draw through its points, then lift the pen.
// The pen is up while traveling between strokes, so each contour is a separate mark.
// On any early exit (error or a "stop"/Close cancellation) it lifts the pen best-effort.
func (d *drawer) runDraw(ctx context.Context, strokes []stroke) {
	defer d.wg.Done()

	cfg, ms := d.cfg, d.motion

	// Pen-down segments use a LinearConstraint: it forces straight-line Cartesian
	// motion AND holds the pen orientation (via its own orientation tolerance) along
	// the line, so the drawn segment stays flat and on the paper.
	//
	// Travel moves between strokes are left unconstrained (nil): the pen is up, so we
	// only care about reaching the next start pose, not the path taken. A standalone
	// path-wide OrientationConstraint here pushed the planner onto cbirrt and a very
	// thin orientation manifold, which timed out (~52s) even for short moves; the goal
	// pose's own orientation still lands the pen pointing down.
	drawC := motionplan.NewConstraints(
		[]motionplan.LinearConstraint{{LineToleranceMm: cfg.LineToleranceMM, OrientationToleranceDegs: cfg.OrientationToleranceDegs}},
		nil, nil, nil)
	var travelC *motionplan.Constraints // nil => default free planning

	var curX, curY float64
	penDown := false
	moveTo := func(x, y, z float64, c *motionplan.Constraints) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := ms.Move(ctx, motion.MoveReq{
			ComponentName: cfg.MoveComponent,
			Destination:   d.poseAt(x, y, z),
			Constraints:   c,
		}); err != nil {
			return err
		}
		curX, curY = x, y
		return nil
	}

	err := func() error {
		reached := 0
		for si, s := range strokes {
			if len(s) == 0 {
				continue
			}
			start := s[0]
			// Travel to this stroke's start with the pen up, then lower it.
			if err := moveTo(start[0], start[1], cfg.ZLiftMM, travelC); err != nil {
				return errors.Wrapf(err, "traveling to stroke %d/%d", si+1, len(strokes))
			}
			penDown = true // from here the pen goes down; retract on any failure
			if err := moveTo(start[0], start[1], cfg.ZDrawMM, drawC); err != nil {
				return errors.Wrapf(err, "lowering pen for stroke %d/%d", si+1, len(strokes))
			}
			reached++
			d.setDone(reached)

			for i := 1; i < len(s); i++ {
				if err := moveTo(s[i][0], s[i][1], cfg.ZDrawMM, drawC); err != nil {
					return errors.Wrapf(err, "drawing stroke %d/%d segment %d/%d", si+1, len(strokes), i, len(s)-1)
				}
				reached++
				d.setDone(reached)
			}

			// Lift the pen before traveling to the next stroke.
			if err := moveTo(curX, curY, cfg.ZLiftMM, travelC); err != nil {
				return errors.Wrapf(err, "lifting pen after stroke %d/%d", si+1, len(strokes))
			}
			penDown = false
		}
		return nil
	}()

	// If we stopped with the pen on the paper, lift it. Uses a fresh, bounded context
	// because the draw ctx may already be cancelled (which is why we exited).
	if penDown {
		rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, rerr := ms.Move(rctx, motion.MoveReq{
			ComponentName: cfg.MoveComponent,
			Destination:   d.poseAt(curX, curY, cfg.ZLiftMM),
			Constraints:   travelC,
		}); rerr != nil {
			d.logger.Warnw("failed to lift pen after interrupted draw", "error", rerr)
		}
		rcancel()
	}

	d.mu.Lock()
	d.running, d.cancel, d.lastErr = false, nil, err
	d.mu.Unlock()

	if err != nil {
		d.logger.Warnw("draw ended early", "error", err)
	} else {
		d.logger.Infof("draw complete: %d strokes, %d points", len(strokes), countPoints(strokes))
	}
}

func (d *drawer) setDone(n int) {
	d.mu.Lock()
	d.done = n
	d.mu.Unlock()
}

func (d *drawer) doStatus() (map[string]interface{}, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := map[string]interface{}{
		"running": d.running,
		"drawn":   d.done,
		"total":   d.total,
	}
	if d.lastErr != nil {
		res["error"] = d.lastErr.Error()
	}
	return res, nil
}

// poseAt builds a pen-tip goal pose in the configured reference frame with the fixed pen orientation.
func (d *drawer) poseAt(x, y, z float64) *referenceframe.PoseInFrame {
	ov := &spatialmath.OrientationVectorDegrees{
		Theta: d.cfg.PenTheta, OX: d.cfg.PenOX, OY: d.cfg.PenOY, OZ: d.cfg.PenOZ,
	}
	pose := spatialmath.NewPose(r3.Vector{X: x, Y: y, Z: z}, ov)
	return referenceframe.NewPoseInFrame(d.cfg.ReferenceFrame, pose)
}
