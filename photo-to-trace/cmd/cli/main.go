package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"photototrace"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
)

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func realMain() error {
	thresh := flag.Int("thresh", 40, "how far from white a pixel must be to count as ink (0-255)")
	minLen := flag.Int("min", 8, "discard contours shorter than this many pixels")
	eps := flag.Float64("simplify", 0, "Douglas-Peucker tolerance in pixels (0 = keep every pixel)")
	out := flag.String("out", "", "path of the CSV to write (required)")
	imgOut := flag.String("img", "", "path of the PNG outline image to write (optional)")
	flag.Parse()

	if flag.NArg() != 1 || *out == "" {
		return fmt.Errorf("usage: cli -out points.csv [-img outline.png] [-thresh 40] [-min 8] [-simplify 1.5] image.png")
	}

	ctx := context.Background()
	logger := logging.NewLogger("cli")

	deps := resource.Dependencies{}
	// can load these from a remote machine if you need

	cfg := photototrace.Config{}

	thing, err := photototrace.NewOutliner(ctx, deps, generic.Named("foo"), cfg, logger)
	if err != nil {
		return err
	}
	defer thing.Close(ctx)

	res, err := thing.DoCommand(ctx, map[string]interface{}{
		"command":  "trace",
		"path":     flag.Arg(0),
		"out":      *out,
		"thresh":   float64(*thresh),
		"min":      float64(*minLen),
		"simplify": *eps,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "wrote %d point(s) in %d contour(s) to %s\n", res["points"], res["contours"], *out)

	if *imgOut != "" {
		traceResult, err := photototrace.TraceFile(flag.Arg(0), *thresh, *minLen, *eps)
		if err != nil {
			return err
		}
		if err := photototrace.RenderTraceFile(*imgOut, traceResult); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote outline image to %s\n", *imgOut)
	}

	return nil
}
