package photototrace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// outline turns the portrait photo at "path" into pen strokes at "out" by
// running the portrait-outliner Python program. Its CSV has the same shape as
// the one "trace" writes, so either can feed the same drawing code.
func (s *photoToTraceOutliner) outline(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if !s.cfg.canOutline() {
		return nil, errors.New(`outline: this service has no "python_bin" and "outliner_script" configured`)
	}

	path, _ := cmd["path"].(string)
	if path == "" {
		return nil, errors.New(`outline: "path" is required (the portrait photo to outline)`)
	}

	out, _ := cmd["out"].(string)
	if out == "" {
		return nil, errors.New(`outline: "out" is required (path of the CSV to write)`)
	}

	return s.runOutliner(ctx, path, out, cmd)
}

// portrait is the whole pipeline in one call: move to the capture pose, take a
// photo to "photo", then outline it to "out". The photo is left on disk, so a
// portrait that comes out wrong can be re-outlined with different settings
// without asking the subject to sit again.
func (s *photoToTraceOutliner) portrait(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if !s.cfg.canOutline() {
		return nil, errors.New(`portrait: this service has no "python_bin" and "outliner_script" configured`)
	}

	// Check both paths before moving the arm: failing on a missing argument
	// after the subject has already been photographed helps nobody.
	photo, _ := cmd["photo"].(string)
	if photo == "" {
		return nil, errors.New(`portrait: "photo" is required (path of the PNG to capture to)`)
	}

	out, _ := cmd["out"].(string)
	if out == "" {
		return nil, errors.New(`portrait: "out" is required (path of the CSV to write)`)
	}

	captureCmd := map[string]interface{}{"out": photo}
	for _, key := range []string{"source", "copy_to"} {
		if v, ok := cmd[key]; ok {
			captureCmd[key] = v
		}
	}
	shot, err := s.capture(ctx, captureCmd)
	if err != nil {
		return nil, err
	}

	res, err := s.runOutliner(ctx, photo, out, cmd)
	if err != nil {
		return nil, err
	}

	res["photo"] = photo
	res["width"] = shot["width"]
	res["height"] = shot["height"]
	if copyTo, ok := shot["copy_to"]; ok {
		res["copy_to"] = copyTo
	}
	return res, nil
}

// runOutliner runs portrait-outliner over the image at in, writing strokes to
// out, and reports what it produced.
func (s *photoToTraceOutliner) runOutliner(ctx context.Context, in, out string, cmd map[string]interface{}) (map[string]interface{}, error) {
	timeout := s.cfg.outlineTimeout()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := s.outlineArgs(in, out, cmd)
	s.logger.Debugf("running %s %s", s.cfg.PythonBin, strings.Join(args, " "))

	var stdout, stderr bytes.Buffer
	proc := exec.CommandContext(runCtx, s.cfg.PythonBin, args...)
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	// Killing the interpreter on timeout does not close the output pipes if it
	// left a child of its own holding them, and reading them is the last thing
	// Run waits on. Cap that wait so a timeout is actually a timeout.
	proc.WaitDelay = 5 * time.Second

	if err := proc.Run(); err != nil {
		// outline.py reports its own failures as "Error: ..." on stderr; an
		// unhandled one arrives as a traceback, whose last lines are the part
		// that says what actually went wrong.
		detail := tail(strings.TrimSpace(stderr.String()), 2000)
		if detail == "" {
			detail = err.Error()
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("outline: gave up after %s: %s", timeout, detail)
		}
		return nil, fmt.Errorf("outline: %s", detail)
	}

	stats := strings.TrimSpace(stdout.String())
	res := map[string]interface{}{"out": out, "stats": stats}

	// The first line of that report is "Saved <n> strokes / <n> points to ...".
	// Lift the two counts out of it so callers need not parse the prose.
	var strokes, points int
	if _, err := fmt.Sscanf(stats, "Saved %d strokes / %d points", &strokes, &points); err == nil {
		res["strokes"] = strokes
		res["points"] = points
	}

	s.logger.Infof("outlined %s -> %s: %s", in, out, firstLine(stats))
	return res, nil
}

// outlineArgs builds outline.py's argument list. Each option is taken from the
// DoCommand call if it names one, otherwise from the configured default,
// otherwise left off entirely so outline.py applies its own default.
func (s *photoToTraceOutliner) outlineArgs(in, out string, cmd map[string]interface{}) []string {
	args := []string{s.cfg.OutlinerScript, in, out}

	// outline.py parses these two with int(), which rejects "50.0" — and every
	// number arrives over the wire as a float64.
	for _, f := range []struct {
		flag, key string
		def       *float64
	}{
		{"--max-strokes", "max_strokes", s.cfg.OutlineMaxStrokes},
		{"--max-points", "max_points", s.cfg.OutlineMaxPoints},
	} {
		if v, ok := outlineArg(cmd, f.key, f.def); ok {
			args = append(args, f.flag, strconv.Itoa(int(v)))
		}
	}

	for _, f := range []struct {
		flag, key string
		def       *float64
	}{
		{"--min-length", "min_length", s.cfg.OutlineMinLength},
		{"--face-min-length", "face_min_length", nil},
		{"--face-share", "face_share", s.cfg.OutlineFaceShare},
		{"--simplify", "simplify", s.cfg.OutlineSimplify},
	} {
		if v, ok := outlineArg(cmd, f.key, f.def); ok {
			args = append(args, f.flag, strconv.FormatFloat(v, 'f', -1, 64))
		}
	}

	// Diagnostics, per call only: --preview renders the strokes as the arm will
	// draw them, --face-map shows where the face was found and how the image
	// was weighted.
	for _, f := range []struct{ flag, key string }{
		{"--preview", "preview"},
		{"--face-map", "face_map"},
	} {
		if v, _ := cmd[f.key].(string); v != "" {
			args = append(args, f.flag, v)
		}
	}

	for _, f := range []struct{ flag, key string }{
		{"--no-mask", "no_mask"},
		{"--no-face", "no_face"},
	} {
		if on, _ := cmd[f.key].(bool); on {
			args = append(args, f.flag)
		}
	}

	return args
}

// outlineArg resolves one numeric outline.py option from the call, then the
// config, reporting whether either supplied it.
func outlineArg(cmd map[string]interface{}, key string, def *float64) (float64, bool) {
	if v, ok := cmd[key].(float64); ok {
		return v, true
	}
	if def != nil {
		return *def, true
	}
	return 0, false
}

// tail returns the last n bytes of s, marking anything it cut.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
