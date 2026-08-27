package photototrace

import (
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
	return deps, nil, nil // tracing files needs no hardware at all
}

type photoToTraceOutliner struct {
	resource.AlwaysRebuild
	resource.Named

	name resource.Name

	logger logging.Logger
	cfg    Config
	cam    camera.Camera
	motion motion.Service

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

	return s, nil
}

func (s *photoToTraceOutliner) Name() resource.Name {
	return s.name
}

// DoCommand supports two commands.
//
// "capture" grabs a frame from the configured camera and writes it as a PNG:
//
//	{"command": "capture", "out": "/data/photo.png", "source": "color"}
//	=> {"width": 1280, "height": 720, "out": "/data/photo.png"}
//
// "trace" writes the outline points of each shape in an image to a CSV — a
// "contour,x,y" header followed by one point per line, in draw order:
//
//	{"command": "trace", "path": "shape.png", "out": "/data/shape.csv",
//	 "thresh": 40, "min": 8, "simplify": 0}
//	=> {"width": 640, "height": 480, "contours": 3, "points": 812, "out": "/data/shape.csv"}
func (s *photoToTraceOutliner) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, _ := cmd["command"].(string)
	switch name {
	case "capture":
		return s.capture(ctx, cmd)
	case "trace":
		return s.trace(cmd)
	default:
		return nil, fmt.Errorf("unknown command %q, expected \"capture\" or \"trace\"", name)
	}
}

// capture writes one frame from the configured camera to "out" as a PNG.
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

	f, err := os.Create(out)
	if err != nil {
		return nil, err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	b := img.Bounds()
	s.logger.Infof("captured %dx%d from %s -> %s", b.Dx(), b.Dy(), s.cfg.Camera, out)
	return map[string]interface{}{
		"width":  b.Dx(),
		"height": b.Dy(),
		"out":    out,
	}, nil
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
