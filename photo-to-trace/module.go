package photototrace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/golang/geo/r3"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/spatialmath"
)

var (
	Outliner         = resource.NewModel("6d6c7293bc6743e49c8b31c76aac27d3", "photo-to-trace", "outliner")
	errUnimplemented = errors.New("unimplemented")
)

func init() {
	resource.RegisterService(generic.API, Outliner,
		resource.Registration[resource.Resource, Config]{
			Constructor: newPhotoToTraceOutliner,
		},
	)
}

type Config struct {
	// Camera is the name of the camera to capture from. Optional: leave it
	// unset for a service that only traces images already on disk.
	Camera string `json:"camera,omitempty"`

	// MotionService is the motion service name; defaults to "builtin".
	MotionService string `json:"motion_service,omitempty"`
	// MoveComponent is the frame the motion service moves to the capture pose,
	// e.g. the camera or pen-tip frame. Required to move the arm before capturing.
	MoveComponent string `json:"move_component,omitempty"`
	// ReferenceFrame is the frame the capture pose is expressed in. Defaults to "world".
	ReferenceFrame string `json:"reference_frame,omitempty"`

	// CapturePose is where MoveComponent is sent before a capture, in the
	// reference frame: position in mm, orientation as an orientation vector in
	// degrees. Omit the whole block to capture from wherever the arm already is.
	CaptureXMM   *float64 `json:"capture_x_mm,omitempty"`
	CaptureYMM   *float64 `json:"capture_y_mm,omitempty"`
	CaptureZMM   *float64 `json:"capture_z_mm,omitempty"`
	CaptureOX    float64  `json:"capture_ox,omitempty"`
	CaptureOY    float64  `json:"capture_oy,omitempty"`
	CaptureOZ    float64  `json:"capture_oz,omitempty"`
	CaptureTheta float64  `json:"capture_theta_deg,omitempty"`

	// SettleMS is how long to wait after the arm reaches the capture pose
	// before grabbing the frame, letting it stop oscillating. Defaults to 500.
	// Set it to 0 to capture as soon as the move returns.
	SettleMS *float64 `json:"capture_settle_ms,omitempty"`

	// PythonBin is the interpreter portrait-outliner's dependencies are
	// installed into, e.g. "/opt/portrait-outliner/venv/bin/python3". Give an
	// absolute path: the module process does not inherit your shell's PATH, so
	// a bare "python3" finds the system interpreter, not your venv.
	PythonBin string `json:"python_bin,omitempty"`
	// OutlinerScript is the absolute path to portrait-outliner's outline.py.
	// Set it together with PythonBin to enable "outline" and "portrait".
	OutlinerScript string `json:"outliner_script,omitempty"`

	// Defaults for outline.py's stroke budget and shaping, each overridable per
	// call. Leave one unset to keep outline.py's own default.
	OutlineMaxStrokes *float64 `json:"outline_max_strokes,omitempty"`
	OutlineMaxPoints  *float64 `json:"outline_max_points,omitempty"`
	OutlineMinLength  *float64 `json:"outline_min_length,omitempty"`
	OutlineFaceShare  *float64 `json:"outline_face_share,omitempty"`
	OutlineSimplify   *float64 `json:"outline_simplify,omitempty"`

	// OutlineTimeoutS bounds one outline run. Defaults to 300 seconds — the
	// first run of all is the slow one, since rembg downloads its segmentation
	// model before it can start.
	OutlineTimeoutS *float64 `json:"outline_timeout_s,omitempty"`

	// Plotter is the name of the pen-plotter generic service that draws a CSV.
	// Optional: leave it unset for a service that only produces CSVs for
	// something else to draw.
	Plotter string `json:"plotter,omitempty"`
}

// canOutline reports whether the Python outliner was configured.
func (cfg Config) canOutline() bool {
	return cfg.PythonBin != "" && cfg.OutlinerScript != ""
}

// outlineTimeout returns how long a single outline run may take.
func (cfg Config) outlineTimeout() time.Duration {
	if cfg.OutlineTimeoutS == nil || *cfg.OutlineTimeoutS <= 0 {
		return 300 * time.Second
	}
	return time.Duration(*cfg.OutlineTimeoutS * float64(time.Second))
}

// settle returns how long to wait after the move before capturing.
func (cfg Config) settle() time.Duration {
	if cfg.SettleMS == nil {
		return 500 * time.Millisecond
	}
	if *cfg.SettleMS <= 0 {
		return 0
	}
	return time.Duration(*cfg.SettleMS * float64(time.Millisecond))
}

// hasCapturePose reports whether a full capture position was configured.
func (cfg Config) hasCapturePose() bool {
	return cfg.CaptureXMM != nil && cfg.CaptureYMM != nil && cfg.CaptureZMM != nil
}

// normalized returns a copy with defaults filled in.
func (cfg Config) normalized() Config {
	if cfg.MotionService == "" {
		cfg.MotionService = "builtin"
	}
	if cfg.ReferenceFrame == "" {
		cfg.ReferenceFrame = referenceframe.World
	}
	if cfg.CaptureOX == 0 && cfg.CaptureOY == 0 && cfg.CaptureOZ == 0 {
		cfg.CaptureOZ = -1 // pointing straight down
	}
	return cfg
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns three values:
//  1. Required dependencies: other resources that must exist for this resource to work.
//  2. Optional dependencies: other resources that may exist but are not required.
//  3. An error if any Config fields are missing or invalid.
//
// The `path` parameter indicates
// where this resource appears in the machine's JSON configuration
// (for example, "components.0"). You can use it in error messages
// to indicate which resource has a problem.
//
// Note: Validate receives a copy of the config; mutations to it will do
// nothing. Fill in any default values in your resource's constructor function
// instead.
func (cfg Config) Validate(path string) ([]string, []string, error) {
	var deps []string
	if cfg.Camera != "" {
		deps = append(deps, camera.Named(cfg.Camera).String())
	}
	if cfg.hasCapturePose() {
		if cfg.MoveComponent == "" {
			return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "move_component")
		}
		deps = append(deps, motion.Named(cfg.normalized().MotionService).String())
	}
	// The outliner is a subprocess, not a resource, so it adds no dependency —
	// but half a configuration would fail only once someone ran "outline".
	if cfg.PythonBin != "" && cfg.OutlinerScript == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "outliner_script")
	}
	if cfg.OutlinerScript != "" && cfg.PythonBin == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "python_bin")
	}
	if cfg.Plotter != "" {
		deps = append(deps, generic.Named(cfg.Plotter).String())
	}
	return deps, nil, nil // tracing files needs no hardware at all
}

type photoToTraceOutliner struct {
	resource.AlwaysRebuild
	resource.Named

	name resource.Name

	logger  logging.Logger
	cfg     Config
	cam     camera.Camera
	motion  motion.Service
	plotter generic.Service

	cancelCtx  context.Context
	cancelFunc func()
}

func newPhotoToTraceOutliner(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[Config](rawConf)
	if err != nil {
		return nil, err
	}

	return NewOutliner(ctx, deps, rawConf.ResourceName(), conf, logger)

}

func NewOutliner(ctx context.Context, deps resource.Dependencies, name resource.Name, conf Config, logger logging.Logger) (resource.Resource, error) {

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &photoToTraceOutliner{
		name:       name,
		logger:     logger,
		cfg:        conf.normalized(),
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}

	if s.cfg.Camera != "" {
		cam, err := camera.FromProvider(deps, s.cfg.Camera)
		if err != nil {
			cancelFunc()
			return nil, err
		}
		s.cam = cam
	}

	if s.cfg.hasCapturePose() {
		ms, err := motion.FromDependencies(deps, s.cfg.MotionService)
		if err != nil {
			cancelFunc()
			return nil, err
		}
		s.motion = ms
	}

	if s.cfg.Plotter != "" {
		plotter, err := generic.FromProvider(deps, s.cfg.Plotter)
		if err != nil {
			cancelFunc()
			return nil, err
		}
		s.plotter = plotter
	}

	return s, nil
}

func (s *photoToTraceOutliner) Name() resource.Name {
	return s.name
}

// DoCommand supports two commands.
//
// "capture" grabs a frame from the configured camera and writes it as a PNG,
// optionally to a second path too (e.g. a cloud-sync folder):
//
//	{"command": "capture", "out": "/data/photo.png", "source": "color", "copy_to": "/sync/photo.png"}
//	=> {"width": 1280, "height": 720, "out": "/data/photo.png", "copy_to": "/sync/photo.png"}
//
// "trace" writes the outline points of each shape in an image to a CSV — a
// "contour,x,y" header followed by one point per line, in draw order:
//
//	{"command": "trace", "path": "shape.png", "out": "/data/shape.csv",
//	 "thresh": 40, "min": 8, "simplify": 0}
//	=> {"width": 640, "height": 480, "contours": 3, "points": 812, "out": "/data/shape.csv"}
//
// "outline" turns a portrait photo into pen strokes by running the
// portrait-outliner Python program, which writes the same CSV shape:
//
//	{"command": "outline", "path": "/data/photo.png", "out": "/data/strokes.csv"}
//	=> {"strokes": 50, "points": 3421, "out": "/data/strokes.csv", "stats": "..."}
//
// "portrait" is the whole pipeline in one call: "capture" then "outline".
//
//	{"command": "portrait", "photo": "/data/photo.png", "out": "/data/strokes.csv"}
//	=> {"width": 1280, "height": 720, "photo": "/data/photo.png",
//	    "strokes": 50, "points": 3421, "out": "/data/strokes.csv", "stats": "..."}
//
// "draw" forwards a CSV already on disk to the configured plotter:
//
//	{"command": "draw", "path": "/data/shape.csv"}
//	=> whatever the plotter's own "draw" DoCommand returns
//
// "draw_portrait" is the whole pipeline in one call: "portrait" then "draw".
//
//	{"command": "draw_portrait", "photo": "/data/photo.png", "out": "/data/strokes.csv"}
//	=> {"width": 1280, "height": 720, "photo": "/data/photo.png",
//	    "strokes": 50, "points": 3421, "out": "/data/strokes.csv", "stats": "...",
//	    "draw": <whatever the plotter's own "draw" DoCommand returns>}
func (s *photoToTraceOutliner) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, _ := cmd["command"].(string)
	switch name {
	case "capture":
		return s.capture(ctx, cmd)
	case "trace":
		return s.trace(cmd)
	case "outline":
		return s.outline(ctx, cmd)
	case "portrait":
		return s.portrait(ctx, cmd)
	case "draw":
		return s.draw(ctx, cmd)
	case "draw_portrait":
		return s.drawPortrait(ctx, cmd)
	default:
		return nil, fmt.Errorf("unknown command %q, expected \"capture\", \"trace\", \"outline\", \"portrait\", \"draw\" or \"draw_portrait\"", name)
	}
}

// capture writes one frame from the configured camera to "out" as a PNG, and
// to "copy_to" too if given — e.g. a directory a data manager syncs to the
// cloud, so the same photo lands there without a separate upload step.
//
// A camera with several streams (a RealSense reports colour and depth) returns
// whichever it lists first unless "source" names the one you want.
func (s *photoToTraceOutliner) capture(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if s.cam == nil {
		return nil, errors.New("capture: this service has no \"camera\" configured")
	}

	out, _ := cmd["out"].(string)
	if out == "" {
		return nil, errors.New("capture: \"out\" is required (path of the PNG to write)")
	}
	copyTo, _ := cmd["copy_to"].(string)

	if err := s.moveToCapturePose(ctx); err != nil {
		return nil, err
	}

	var sources []string
	if src, _ := cmd["source"].(string); src != "" {
		sources = []string{src}
	}

	img, err := camera.DecodeImageFromCamera(ctx, s.cam, sources, nil)
	if err != nil {
		return nil, err
	}

	// Encode once, write the identical bytes to both paths.
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}

	paths := []string{out}
	if copyTo != "" {
		paths = append(paths, copyTo)
	}
	for _, p := range paths {
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", p, err)
		}
	}

	b := img.Bounds()
	if copyTo != "" {
		s.logger.Infof("captured %dx%d from %s -> %s, %s", b.Dx(), b.Dy(), s.cfg.Camera, out, copyTo)
	} else {
		s.logger.Infof("captured %dx%d from %s -> %s", b.Dx(), b.Dy(), s.cfg.Camera, out)
	}
	res := map[string]interface{}{
		"width":  b.Dx(),
		"height": b.Dy(),
		"out":    out,
	}
	if copyTo != "" {
		res["copy_to"] = copyTo
	}
	return res, nil
}

// moveToCapturePose sends the configured frame to the capture pose and waits
// for the move to finish. It is a no-op when no capture pose is configured, so
// the arm stays wherever it already is.
func (s *photoToTraceOutliner) moveToCapturePose(ctx context.Context) error {
	if s.motion == nil {
		return nil
	}

	pose := spatialmath.NewPose(
		r3.Vector{X: *s.cfg.CaptureXMM, Y: *s.cfg.CaptureYMM, Z: *s.cfg.CaptureZMM},
		&spatialmath.OrientationVectorDegrees{
			Theta: s.cfg.CaptureTheta, OX: s.cfg.CaptureOX, OY: s.cfg.CaptureOY, OZ: s.cfg.CaptureOZ,
		},
	)

	s.logger.Infof("moving %s to capture pose %v", s.cfg.MoveComponent, pose.Point())
	_, err := s.motion.Move(ctx, motion.MoveReq{
		ComponentName: s.cfg.MoveComponent,
		Destination:   referenceframe.NewPoseInFrame(s.cfg.ReferenceFrame, pose),
	})
	if err != nil {
		return fmt.Errorf("moving %s to capture pose: %w", s.cfg.MoveComponent, err)
	}

	// Move returns once the arm reports it is there, which is a moment before it
	// has actually stopped ringing. Let it settle so the frame isn't smeared.
	if d := s.cfg.settle(); d > 0 {
		s.logger.Debugf("settling for %s before capture", d)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

// trace writes the outline points of the image at "path" to "out" as CSV.
func (s *photoToTraceOutliner) trace(cmd map[string]interface{}) (map[string]interface{}, error) {
	path, _ := cmd["path"].(string)
	if path == "" {
		return nil, errors.New("trace: \"path\" is required")
	}

	out, _ := cmd["out"].(string)
	if out == "" {
		return nil, errors.New("trace: \"out\" is required (path of the CSV to write)")
	}

	res, err := TraceFile(path, intArg(cmd, "thresh", 40), intArg(cmd, "min", 8), floatArg(cmd, "simplify", 0))
	if err != nil {
		return nil, err
	}

	if err := WriteTraceFile(out, res); err != nil {
		return nil, err
	}

	points := 0
	for _, c := range res.Contours {
		points += len(c)
	}

	s.logger.Infof("traced %s -> %s: %d contour(s), %d point(s)", path, out, len(res.Contours), points)
	return map[string]interface{}{
		"width":    res.Width,
		"height":   res.Height,
		"contours": len(res.Contours),
		"points":   points,
		"out":      out,
	}, nil
}

// draw forwards a CSV already on disk to the configured plotter's own "draw"
// DoCommand and returns whatever it returns.
func (s *photoToTraceOutliner) draw(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if s.plotter == nil {
		return nil, errors.New("draw: this service has no \"plotter\" configured")
	}

	path, _ := cmd["path"].(string)
	if path == "" {
		return nil, errors.New("draw: \"path\" is required (the CSV to draw)")
	}

	s.logger.Infof("drawing %s -> %s", path, s.cfg.Plotter)
	return s.plotter.DoCommand(ctx, map[string]interface{}{"command": "draw", "path": path})
}

// drawPortrait runs "portrait" then hands the CSV it wrote straight to
// "draw". It takes every argument "portrait" does.
func (s *photoToTraceOutliner) drawPortrait(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if s.plotter == nil {
		return nil, errors.New("draw_portrait: this service has no \"plotter\" configured")
	}

	res, err := s.portrait(ctx, cmd)
	if err != nil {
		return nil, err
	}

	out, _ := res["out"].(string)
	drawRes, err := s.draw(ctx, map[string]interface{}{"path": out})
	if err != nil {
		return nil, err
	}

	res["draw"] = drawRes
	return res, nil
}

// floatArg reads a numeric DoCommand argument, falling back to def when absent.
// Values arrive over the wire as float64.
func floatArg(cmd map[string]interface{}, key string, def float64) float64 {
	if v, ok := cmd[key].(float64); ok {
		return v
	}
	return def
}

func intArg(cmd map[string]interface{}, key string, def int) int {
	return int(floatArg(cmd, key, float64(def)))
}

func (s *photoToTraceOutliner) Status(ctx context.Context) (map[string]interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *photoToTraceOutliner) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}
