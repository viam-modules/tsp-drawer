package main

import (
	"context"
	"sync"

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

	// InputPath is the default file or directory to read a tour from when a "draw"
	// command doesn't specify its own path. Optional.
	InputPath string `json:"input_path"`

	// Affine map from input (x,y) units to robot base-frame millimeters on the paper:
	//   robotX = origin_x_mm + x*mm_per_unit_x
	//   robotY = origin_y_mm + y*mm_per_unit_y   (NEGATIVE mm_per_unit_y flips the image upright)
	OriginXMM  float64 `json:"origin_x_mm"`
	OriginYMM  float64 `json:"origin_y_mm"`
	MMPerUnitX float64 `json:"mm_per_unit_x"`
	MMPerUnitY float64 `json:"mm_per_unit_y"` // default: mm_per_unit_x

	// Pen Z heights in robot base-frame millimeters.
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

	// RDPEpsilon downsamples each tour with Ramer–Douglas–Peucker before drawing.
	// Epsilon is in INPUT units; 0 disables. Collapses near-collinear runs so a dense
	// tour becomes far fewer motion calls with no visible change to the drawing.
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
	if cfg.MMPerUnitX == 0 {
		return nil, nil, resource.NewConfigValidationError(path, errors.New("mm_per_unit_x must be non-zero"))
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
	if cfg.MMPerUnitY == 0 {
		cfg.MMPerUnitY = cfg.MMPerUnitX
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

	mu     sync.Mutex
	cfg    Config
	arm    arm.Arm
	motion motion.Service

	// cancel interrupts an in-progress draw when a "stop" command arrives.
	cancel context.CancelFunc
}

func newDrawer(
	ctx context.Context,
	deps resource.Dependencies,
	conf resource.Config,
	logger logging.Logger,
) (resource.Resource, error) {
	d := &drawer{Named: conf.ResourceName().AsNamed(), logger: logger}
	if err := d.Reconfigure(ctx, deps, conf); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *drawer) Reconfigure(_ context.Context, deps resource.Dependencies, conf resource.Config) error {
	cfg, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return err
	}
	norm := cfg.normalized()

	a, err := typedDep[arm.Arm](deps, arm.Named(norm.Arm))
	if err != nil {
		return err
	}
	m, err := typedDep[motion.Service](deps, motion.Named(norm.MotionService))
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg, d.arm, d.motion = norm, a, m
	return nil
}

func (d *drawer) Close(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
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
//	{"command": "draw", "points": [[x,y],...], "order": [i,...], ...overrides}
//	{"command": "stop"}
func (d *drawer) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, _ := cmd["command"].(string)
	switch name {
	case "draw":
		return d.doDraw(ctx, cmd)
	case "stop":
		return d.doStop(ctx)
	case "":
		return nil, errors.New("missing 'command' (expected \"draw\" or \"stop\")")
	default:
		return nil, errors.Errorf("unknown command %q", name)
	}
}

func (d *drawer) doStop(ctx context.Context) (map[string]interface{}, error) {
	d.mu.Lock()
	cancel, a := d.cancel, d.arm
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := a.Stop(ctx, nil); err != nil {
		return nil, err
	}
	return map[string]interface{}{"stopped": true}, nil
}

func (d *drawer) doDraw(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	d.mu.Lock()
	cfg, ms := d.cfg, d.motion
	d.mu.Unlock()

	// Resolve the ordered list of points for this tour from the input file(s).
	ordered, err := d.loadInput(cmd)
	if err != nil {
		return nil, err
	}
	if cfg.RDPEpsilon > 0 {
		ordered = rdp(ordered, cfg.RDPEpsilon)
	}
	if len(ordered) == 0 {
		return nil, errors.New("no points to draw")
	}

	// Map input units -> robot base-frame millimeters (pen-tip XY on the paper).
	world := make([][2]float64, len(ordered))
	for i, p := range ordered {
		world[i] = [2]float64{
			cfg.OriginXMM + p[0]*cfg.MMPerUnitX,
			cfg.OriginYMM + p[1]*cfg.MMPerUnitY,
		}
	}

	// Constraints: keep the pen pointing down, and force straight-line Cartesian
	// motion while the pen is on the paper so segments don't bow off it.
	orient := []motionplan.OrientationConstraint{{OrientationToleranceDegs: cfg.OrientationToleranceDegs}}
	drawC := motionplan.NewConstraints(
		[]motionplan.LinearConstraint{{LineToleranceMm: cfg.LineToleranceMM, OrientationToleranceDegs: cfg.OrientationToleranceDegs}},
		nil, orient, nil)
	travelC := motionplan.NewConstraints(nil, nil, orient, nil)

	// Make this draw cancellable by a concurrent "stop".
	ctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel() // supersede any prior draw
	}
	d.cancel = cancel
	d.mu.Unlock()
	defer cancel()

	moveTo := func(x, y, z float64, c *motionplan.Constraints) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := ms.Move(ctx, motion.MoveReq{
			ComponentName: cfg.MoveComponent,
			Destination:   d.poseAt(x, y, z),
			Constraints:   c,
		})
		return err
	}

	// TSP art is one continuous stroke: travel to start, pen down, trace, pen up.
	first, last := world[0], world[len(world)-1]
	if err := moveTo(first[0], first[1], cfg.ZLiftMM, travelC); err != nil {
		return nil, errors.Wrap(err, "moving to start")
	}
	if err := moveTo(first[0], first[1], cfg.ZDrawMM, drawC); err != nil {
		return nil, errors.Wrap(err, "lowering pen")
	}
	for i := 1; i < len(world); i++ {
		if err := moveTo(world[i][0], world[i][1], cfg.ZDrawMM, drawC); err != nil {
			return nil, errors.Wrapf(err, "drawing segment %d/%d", i, len(world)-1)
		}
	}
	if err := moveTo(last[0], last[1], cfg.ZLiftMM, travelC); err != nil {
		return nil, errors.Wrap(err, "lifting pen")
	}

	return map[string]interface{}{"drew_points": len(world)}, nil
}

// poseAt builds a pen-tip goal pose in the world frame with the fixed pen orientation.
func (d *drawer) poseAt(x, y, z float64) *referenceframe.PoseInFrame {
	ov := &spatialmath.OrientationVectorDegrees{
		Theta: d.cfg.PenTheta, OX: d.cfg.PenOX, OY: d.cfg.PenOY, OZ: d.cfg.PenOZ,
	}
	pose := spatialmath.NewPose(r3.Vector{X: x, Y: y, Z: z}, ov)
	return referenceframe.NewPoseInFrame(d.cfg.ReferenceFrame, pose)
}
