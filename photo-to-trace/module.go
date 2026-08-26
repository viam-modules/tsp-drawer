package photototrace

import (
	"context"
	"errors"
	"fmt"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
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
	/*
		Put config attributes here. There should be public/exported fields
		with a `json` parameter at the end of each attribute.

		Example config struct:
			type Config struct {
				Pin   string `json:"pin"`
				Board string `json:"board"`
				MinDeg *float64 `json:"min_angle_deg,omitempty"`
			}

		If your model does not need a config, replace Config in the init
		function with resource.NoNativeConfig
	*/
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
	// Add config validation code here
	return nil, nil, nil
}

type photoToTraceOutliner struct {
	resource.AlwaysRebuild
	resource.Named

	name resource.Name

	logger logging.Logger
	cfg    Config

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
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}
	return s, nil
}

func (s *photoToTraceOutliner) Name() resource.Name {
	return s.name
}

// DoCommand supports one command:
//
//	{"command": "trace", "path": "shape.png", "out": "/data/shape.csv",
//	 "thresh": 40, "min": 8, "simplify": 0}
//
// It writes the outline points of each shape to "out" as CSV — a "contour,x,y"
// header followed by one point per line, in draw order — and returns a summary:
//
//	{"width": 640, "height": 480, "contours": 3, "points": 812, "out": "/data/shape.csv"}
func (s *photoToTraceOutliner) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if name, _ := cmd["command"].(string); name != "trace" {
		return nil, fmt.Errorf("unknown command %q, expected \"trace\"", name)
	}

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
