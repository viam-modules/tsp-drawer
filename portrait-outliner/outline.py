#!/usr/bin/env python3
"""Convert a portrait photo into pen strokes for a drawing robot.

Outputs a CSV of contour,x,y rows: one row per point, with `contour`
incrementing whenever a new stroke starts. Read the file top to bottom, draw
each contour as a connected polyline, and lift the pen when `contour` changes.
Coordinates are pixels in the input image's frame, x right and y down.
"""

import argparse
import os
import sys

import cv2
import numpy as np

import face as facelib
import segment
import strokes as strokelib


# Mean importance above which a stroke counts as being "on the face". The
# priority map uses 1.0 for hair/clothing/background and 4.0 for the face core,
# so this sits between them and tolerates a stroke overhanging the boundary.
FACE_THRESHOLD = 2.5


def parse_args():
    parser = argparse.ArgumentParser(
        description="Convert a headshot photo into plotter-ready pen strokes.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("input", help="Path to the input JPG/PNG headshot")
    parser.add_argument("output", help="Path to write the contour,x,y CSV")
    parser.add_argument(
        "--preview",
        metavar="PATH",
        help="Also render the strokes to an image, drawn exactly as the arm "
        "will draw them",
    )

    budget = parser.add_argument_group("drawing budget")
    budget.add_argument(
        "--max-strokes",
        type=int,
        default=50,
        help="Cap on pen-down strokes. The budget is split between the face "
        "and everything else (see --face-share); within each group the "
        "shortest strokes are dropped first",
    )
    budget.add_argument(
        "--max-points",
        type=int,
        default=3500,
        help="Cap on total points; simplification is made coarser until the "
        "output fits, which shortens lines' descriptions but removes no lines",
    )
    budget.add_argument(
        "--min-length",
        type=float,
        default=14.0,
        help="Discard strokes shorter than this many pixels of arc length. "
        "Applies off the face only; see --face-min-length for the limit used "
        "on the features themselves",
    )

    budget.add_argument(
        "--face-min-length",
        type=float,
        default=4.0,
        help="Length limit applied to strokes on the face instead of "
        "--min-length. Eyebrows and eyelids are only a few pixels long, so a "
        "single global limit erases them while hair strands pass easily",
    )
    budget.add_argument(
        "--face-share",
        type=float,
        default=0.8,
        help="Fraction of the stroke budget reserved for the face, so hair and "
        "clothing compete only with each other for what is left",
    )

    shape = parser.add_argument_group("stroke shaping")
    shape.add_argument(
        "--simplify",
        type=float,
        default=1.2,
        help="Douglas-Peucker tolerance in pixels; the starting point for the "
        "point budget, which may raise it",
    )
    shape.add_argument(
        "--smooth",
        type=int,
        default=1,
        help="Chaikin smoothing passes, to round the pixel-grid staircase the "
        "arm would otherwise decelerate into at every corner",
    )
    shape.add_argument(
        "--thickness",
        type=int,
        default=2,
        help="Stroke thickness in the preview image only",
    )

    detect = parser.add_argument_group("edge detection")
    detect.add_argument(
        "--clahe",
        type=float,
        default=2.0,
        help="Local contrast boost before edge detection, which is what makes "
        "eyes and nostrils register on evenly lit faces; 0 disables it",
    )
    detect.add_argument(
        "--blur",
        type=int,
        default=7,
        help="Bilateral filter diameter; smooths noise while keeping edges sharp",
    )
    detect.add_argument("--canny-low", type=int, default=40,
                        help="Lower hysteresis threshold for Canny")
    detect.add_argument("--canny-high", type=int, default=110,
                        help="Upper hysteresis threshold for Canny")
    detect.add_argument(
        "--close",
        type=int,
        default=2,
        help="Morphological closing passes to bridge gaps in broken edges so "
        "tracing yields long strokes instead of many fragments",
    )

    face_group = parser.add_argument_group("face priority")
    face_group.add_argument(
        "--face-model",
        default=facelib.DEFAULT_MODEL_PATH,
        help="YuNet ONNX model used to locate the face and its landmarks",
    )
    face_group.add_argument(
        "--no-face",
        action="store_true",
        help="Treat the whole subject as equally important. Hair and clothing "
        "will then outcompete the facial features, since they are far longer",
    )
    face_group.add_argument(
        "--face-map",
        metavar="PATH",
        help="Write a heat-map overlay showing where the face was found and "
        "how the image was weighted; the first thing to check if a portrait "
        "comes out wrong",
    )

    mask = parser.add_argument_group("subject mask")
    mask.add_argument(
        "--no-mask",
        action="store_true",
        help="Skip segmentation and trace the whole frame, including any "
        "background. Only sensible if the background is already flat white",
    )
    mask.add_argument("--mask-model", default=segment.DEFAULT_MODEL,
                      help="rembg model used to segment the subject")
    mask.add_argument(
        "--mask-erode",
        type=int,
        default=2,
        help="Erosion passes shrinking the mask before interior edges are kept, "
        "so the subject boundary is drawn once as the silhouette",
    )
    mask.add_argument(
        "--refine",
        type=int,
        default=4,
        help="Guided-filter radius re-fitting the mask to the photo's edges; "
        "0 disables refinement",
    )
    return parser.parse_args()


def load_image(path):
    if not os.path.isfile(path):
        raise FileNotFoundError(f"Input file not found: {path}")

    image = cv2.imread(path)
    if image is None:
        raise ValueError(
            f"Could not read '{path}' as an image (unsupported or corrupt file)"
        )
    return image


def check_output_dir(path):
    output_dir = os.path.dirname(path)
    if output_dir and not os.path.isdir(output_dir):
        raise FileNotFoundError(f"Output directory does not exist: {output_dir}")


def build_weights(image, boundary_mask, args):
    """Importance map over the image, and a note on how it was derived."""
    if args.no_face:
        return None, "disabled"

    face = facelib.detect_face(image, args.face_model)
    if face is not None:
        return facelib.priority_map(image.shape, face), \
            f"landmarks (confidence {face['confidence']:.2f})"

    if not os.path.isfile(args.face_model):
        source = "mask proportions (face model missing)"
    else:
        source = "mask proportions (no face detected)"

    if boundary_mask is None:
        return None, "disabled (no subject mask)"

    return facelib.fallback_priority_map(image.shape, boundary_mask), source


def extract_strokes(image, args):
    """Build the stroke list: silhouette plus interior feature lines."""
    interior_mask = None
    boundary_mask = None
    result = []

    if not args.no_mask:
        session = segment.load_session(args.mask_model)
        alpha = segment.subject_alpha(image, session, refine_radius=args.refine)

        # Two masks from one matte: the un-eroded one marks the true boundary to
        # draw the silhouette along, the eroded one bounds which interior edges
        # survive.
        boundary_mask = segment.binary_mask(alpha, erode=0)
        interior_mask = segment.binary_mask(alpha, erode=args.mask_erode)
        result.extend(strokelib.silhouette_strokes(boundary_mask))

    skeleton = strokelib.edge_skeleton(
        image,
        interior_mask,
        clahe_clip=args.clahe,
        blur=args.blur,
        canny_low=args.canny_low,
        canny_high=args.canny_high,
        close_iterations=args.close,
    )
    result.extend(
        strokelib.Stroke(points, False, False)
        for points in strokelib.trace_skeleton(skeleton)
    )

    return result, boundary_mask


def convert_to_strokes(args):
    image = load_image(args.input)
    check_output_dir(args.output)
    if args.preview:
        check_output_dir(args.preview)

    raw, boundary_mask = extract_strokes(image, args)
    weights, weight_source = build_weights(image, boundary_mask, args)

    if args.face_map:
        overlay = facelib.render_priority_map(
            image, weights if weights is not None else np.ones(image.shape[:2],
                                                               dtype=np.float32)
        )
        if not cv2.imwrite(args.face_map, overlay):
            raise IOError(f"Failed to write face map to '{args.face_map}'")

    long_enough = strokelib.filter_by_length(
        raw, weights, args.min_length, args.face_min_length,
        face_threshold=FACE_THRESHOLD,
    )

    budgeted, dropped, epsilon = strokelib.apply_budget(
        long_enough,
        max_strokes=args.max_strokes,
        max_points=args.max_points,
        epsilon=args.simplify,
        smooth_iterations=args.smooth,
        weights=weights,
        face_share=args.face_share,
        face_threshold=FACE_THRESHOLD,
    )

    if not budgeted:
        raise ValueError(
            "No strokes survived filtering. Try lowering --min-length or "
            "--canny-low, or raising --max-strokes."
        )

    ordered = strokelib.order_strokes(budgeted)
    strokelib.write_csv(args.output, ordered)

    if args.preview:
        preview = strokelib.render_preview(image.shape, ordered, args.thickness)
        if not cv2.imwrite(args.preview, preview):
            raise IOError(f"Failed to write preview image to '{args.preview}'")

    on_face = sum(
        1 for s in ordered
        if strokelib.stroke_weight(s, weights) >= FACE_THRESHOLD
    )

    return {
        "weights": weight_source,
        "on_face": on_face,
        "traced": len(raw),
        "short": len(raw) - len(long_enough),
        "dropped": dropped,
        "strokes": len(ordered),
        "points": strokelib.total_points(ordered),
        "epsilon": epsilon,
        "travel": strokelib.travel_distance(ordered),
        "size": (image.shape[1], image.shape[0]),
    }


def main():
    args = parse_args()

    try:
        stats = convert_to_strokes(args)
    except (FileNotFoundError, ValueError, IOError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(1)

    width, height = stats["size"]
    print(f"Saved {stats['strokes']} strokes / {stats['points']} points to {args.output}")
    print(f"  image        {width}x{height} px")
    print(f"  face         {stats['weights']}, "
          f"{stats['on_face']}/{stats['strokes']} strokes on the face")
    print(f"  traced       {stats['traced']} strokes "
          f"({stats['short']} below --min-length, {stats['dropped']} over budget)")
    print(f"  simplify     {stats['epsilon']:.2f} px tolerance")
    print(f"  pen-up trav. {stats['travel']:.0f} px")
    if args.preview:
        print(f"  preview      {args.preview}")


if __name__ == "__main__":
    main()
