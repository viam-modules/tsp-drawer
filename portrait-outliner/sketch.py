#!/usr/bin/env python3
"""Illustrator-style portrait pipeline: XDoG/FDoG lines plus hatched shading.

The A/B alternative to outline.py's Canny path (which is left untouched).
Same input, same contour,x,y CSV output, so everything downstream of edge
generation -- including the drawing robot -- works with either. Additionally
writes a layered SVG (structural lines / hatching) for plotter tools such as
vpype.

Pipeline: bilateral preprocess -> XDoG or flow-based DoG line extraction ->
landmark-guided threshold relaxation on facial features -> highlight
suppression -> luminance-driven cross-contour hatching along the same flow
field -> skeleton tracing / streamlines into polylines -> budget, order,
write. See lineart.py and hatching.py for the why of each stage.

Stage debugging: --debug-dir writes each intermediate (preprocessed image,
soft response, binarized/suppressed edges, flow field, hatch layer) so stages
can be judged independently before blaming the whole chain.
"""

import argparse
import os
import sys

import cv2
import numpy as np
from skimage.morphology import skeletonize

import face as facelib
import hatching
import lineart
import segment
import strokes as strokelib

FACE_THRESHOLD = 2.5  # same meaning as outline.py


def parse_args():
    parser = argparse.ArgumentParser(
        description="Convert a headshot photo into illustrator-style plotter "
        "strokes (XDoG/FDoG lines + hatched shading).",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("input", help="Path to the input JPG/PNG headshot")
    parser.add_argument("output", help="Path to write the contour,x,y CSV")
    parser.add_argument("--preview", metavar="PATH",
                        help="Render the strokes to an image for eyeballing")
    parser.add_argument("--svg", metavar="PATH",
                        help="Write a layered SVG (structural + hatching) "
                        "for vpype / plotter software")
    parser.add_argument("--debug-dir", metavar="DIR",
                        help="Write every intermediate stage as an image here")
    parser.add_argument(
        "--max-dim", type=int, default=1200,
        help="Work at most at this resolution (longest side); output "
        "coordinates are scaled back to the input image's frame. Pixel-unit "
        "parameters below apply at the working scale",
    )
    parser.add_argument(
        "--min-dim", type=int, default=1000,
        help="Upscale smaller inputs so the longest side reaches this before "
        "processing. Facial features only a few pixels wide trace to stubs; "
        "giving the flow field room turns them into readable curves",
    )

    engine = parser.add_argument_group("line engine")
    engine.add_argument("--engine", choices=("xdog", "fdog"), default="fdog",
                        help="fdog runs the DoG along a smoothed tangent flow "
                        "for coherent, flowing lines; xdog is the simpler "
                        "isotropic variant")
    engine.add_argument("--pre-d", type=int, default=9,
                        help="Bilateral filter diameter for preprocessing")
    engine.add_argument("--pre-passes", type=int, default=1,
                        help="Bilateral passes; 2 for very noisy photos")
    engine.add_argument("--sigma", type=float, default=1.0,
                        help="Base Gaussian scale: fine vs. coarse lines "
                        "(xdog sigma / fdog sigma_c)")
    engine.add_argument("--xdog-k", type=float, default=1.6,
                        help="xdog: ratio between the two blur scales")
    engine.add_argument("--xdog-p", type=float, default=40.0,
                        help="xdog: edge boldness")
    engine.add_argument("--xdog-epsilon", type=float, default=0.1,
                        help="xdog: soft-threshold crossover on the "
                        "normalized diff; higher keeps more (and starts "
                        "filling dark regions solid)")
    engine.add_argument("--phi", type=float, default=None,
                        help="Soft-threshold sharpness; lower keeps sketchy "
                        "grey transitions (visible in --debug-dir), higher "
                        "is closer to binary. Defaults to 3 for fdog, 10 "
                        "for xdog (their response scales differ)")
    engine.add_argument("--fdog-sigma-m", type=float, default=3.0,
                        help="fdog: how far coherence reaches along the flow")
    engine.add_argument("--fdog-iters", type=int, default=2,
                        help="fdog: re-ink iterations; more connects and "
                        "thickens lines")
    engine.add_argument("--etf-radius", type=int, default=5,
                        help="Tangent-flow smoothing kernel radius")
    engine.add_argument("--etf-iters", type=int, default=3,
                        help="Tangent-flow smoothing iterations")
    engine.add_argument("--despeckle", type=int, default=12,
                        help="Drop ink blobs smaller than this many pixels")

    thresh = parser.add_argument_group("thresholding")
    thresh.add_argument("--tau", type=float, default=0.5,
                        help="Base binarization threshold on the soft "
                        "response (higher keeps fainter lines)")
    thresh.add_argument("--feature-tau", type=float, default=0.82,
                        help="Threshold inside landmark feature zones (eyes, "
                        "brows, nose, mouth), so faint feature edges survive")
    thresh.add_argument("--no-landmarks", action="store_true",
                        help="Skip landmark-guided threshold relaxation")
    thresh.add_argument("--highlight-percentile", type=float, default=92.0,
                        help="Drop lines where the subject is brighter than "
                        "this percentile of its own pixels; 100 disables")

    hatch = parser.add_argument_group("hatching")
    hatch.add_argument("--no-hatch", action="store_true",
                       help="Structural lines only, no shading layer")
    hatch.add_argument("--hatch-min", type=float, default=0.35,
                       help="Hatch only where darkness (0 white, 1 black) "
                       "exceeds this")
    hatch.add_argument("--hatch-spacing", type=float, default=4.0,
                       help="Stroke spacing in the darkest shadows, px")
    hatch.add_argument("--hatch-spacing-max", type=float, default=14.0,
                       help="Stroke spacing at the hatch threshold, px")
    hatch.add_argument("--hatch-max-length", type=float, default=80.0,
                       help="Cap on a single hatch stroke's length, px")
    hatch.add_argument("--cross-hatch", action="store_true",
                       help="Add a perpendicular second pass in the darkest "
                       "regions (adds plot time)")
    hatch.add_argument("--max-hatch-strokes", type=int, default=500,
                       help="Cap on hatch strokes, densest kept first")

    budget = parser.add_argument_group("drawing budget (structural layer)")
    budget.add_argument("--max-strokes", type=int, default=250,
                        help="Cap on structural pen-down strokes")
    budget.add_argument("--max-points", type=int, default=20000,
                        help="Cap on structural points; met by coarsening "
                        "simplification")
    budget.add_argument("--min-length", type=float, default=10.0,
                        help="Discard structural strokes shorter than this "
                        "off the face")
    budget.add_argument("--face-min-length", type=float, default=4.0,
                        help="Length limit on the face instead of "
                        "--min-length")
    budget.add_argument("--face-share", type=float, default=0.5,
                        help="Fraction of the structural budget reserved for "
                        "the face")
    budget.add_argument("--simplify", type=float, default=1.2,
                        help="Douglas-Peucker tolerance, px")
    budget.add_argument("--smooth", type=int, default=1,
                        help="Chaikin smoothing passes")
    budget.add_argument("--bold-offset", type=float, default=0.7,
                        help="Duplicate important contours (silhouette, "
                        "long face lines) offset by this many px so they "
                        "plot bolder; 0 disables")
    budget.add_argument("--thickness", type=int, default=2,
                        help="Structural stroke thickness in the preview "
                        "image only")

    mask = parser.add_argument_group("subject mask / face")
    mask.add_argument("--no-mask", action="store_true",
                      help="Skip segmentation and process the whole frame")
    mask.add_argument("--mask-model", default=segment.DEFAULT_MODEL,
                      help="rembg model used to segment the subject")
    mask.add_argument("--mask-erode", type=int, default=2,
                      help="Erosion passes on the interior mask")
    mask.add_argument("--refine", type=int, default=4,
                      help="Guided-filter radius re-fitting the mask")
    mask.add_argument("--face-model", default=facelib.DEFAULT_MODEL_PATH,
                      help="YuNet ONNX model for landmarks")
    return parser.parse_args()


def save_debug(debug_dir, name, image):
    if not debug_dir:
        return
    os.makedirs(debug_dir, exist_ok=True)
    cv2.imwrite(os.path.join(debug_dir, name), image)


def flow_visualization(tx, ty, mag):
    """Tangent direction as hue, gradient magnitude as brightness."""
    angle = (np.arctan2(ty, tx) % np.pi) / np.pi  # direction is sign-free
    hsv = np.stack([
        (angle * 179).astype(np.uint8),
        np.full(tx.shape, 255, dtype=np.uint8),
        (np.sqrt(mag) * 255).astype(np.uint8),
    ], axis=-1)
    return cv2.cvtColor(hsv, cv2.COLOR_HSV2BGR)


def embolden(strokes, weights, offset, min_arc=20.0):
    """Duplicate important contours slightly offset, to fake a bolder line.

    The pen has no pressure control, so weight variation comes from drawing a
    contour twice, sub-millimetre apart. Applied to the silhouette and to
    substantial on-face lines (jaw, nose bridge, eye outlines); short interior
    ticks stay single so the hierarchy reads.
    """
    if offset <= 0:
        return []

    doubled = []
    for stroke in strokes:
        important = stroke.protected or (
            strokelib.stroke_weight(stroke, weights) >= FACE_THRESHOLD
            and strokelib.arc_length(stroke.points, stroke.closed) >= min_arc
        )
        if not important or len(stroke.points) < 3:
            continue

        # Per-point normals from the central-difference tangent.
        tangents = np.gradient(stroke.points, axis=0)
        norms = np.linalg.norm(tangents, axis=1, keepdims=True)
        tangents /= np.maximum(norms, 1e-6)
        normals = np.column_stack([-tangents[:, 1], tangents[:, 0]])
        shifted = (stroke.points + offset * normals).astype(np.float32)
        doubled.append(strokelib.Stroke(shifted, stroke.closed, False))

    return doubled


def write_svg(path, layers, size):
    """Write strokes as a layered SVG that vpype/Inkscape read as layers."""
    width, height = size
    parts = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        f'<svg xmlns="http://www.w3.org/2000/svg" '
        f'xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" '
        f'width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}">',
    ]
    for index, (name, strokes) in enumerate(layers, start=1):
        parts.append(
            f'<g inkscape:groupmode="layer" inkscape:label="{index} {name}" '
            f'id="layer{index}" fill="none" stroke="black" stroke-width="1" '
            f'stroke-linecap="round" stroke-linejoin="round">'
        )
        for stroke in strokes:
            coords = " L ".join(f"{x:.2f} {y:.2f}" for x, y in stroke.points)
            closed = " Z" if stroke.closed else ""
            parts.append(f'<path d="M {coords}{closed}"/>')
        parts.append("</g>")
    parts.append("</svg>")

    with open(path, "w") as handle:
        handle.write("\n".join(parts))


def render_layers(shape, structural, hatch, thickness):
    """Preview with the structural/hatching hierarchy the plotter will have."""
    height, width = shape[:2]
    canvas = np.full((height, width, 3), 255, dtype=np.uint8)
    for strokes, weight in ((hatch, 1), (structural, thickness)):
        for stroke in strokes:
            cv2.polylines(
                canvas,
                [np.round(stroke.points).astype(np.int32).reshape(-1, 1, 2)],
                isClosed=stroke.closed, color=(0, 0, 0), thickness=weight,
                lineType=cv2.LINE_AA,
            )
    return canvas


def scale_strokes(strokes, factor):
    if factor == 1.0:
        return strokes
    return [s._replace(points=s.points * factor) for s in strokes]


def extract_structural(gray_pre, flow, args, tau_map, interior_mask,
                       highlight_protect, debug_dir):
    """Run the line engine and return (edges uint8, soft response, highlight)."""
    if args.engine == "fdog":
        tx, ty, _ = flow
        phi = 3.0 if args.phi is None else args.phi
        soft = lineart.fdog(gray_pre, tx, ty, sigma_c=args.sigma,
                            sigma_m=args.fdog_sigma_m, phi=phi,
                            iterations=args.fdog_iters)
    else:
        phi = 10.0 if args.phi is None else args.phi
        soft = lineart.xdog(gray_pre, sigma=args.sigma, k=args.xdog_k,
                            p=args.xdog_p, epsilon=args.xdog_epsilon,
                            phi=phi)
    save_debug(debug_dir, "02_soft_response.png",
               (soft * 255).astype(np.uint8))

    edges = lineart.binarize(soft, tau_map)
    save_debug(debug_dir, "03_edges_binarized.png", edges)

    if interior_mask is not None:
        edges = cv2.bitwise_and(edges, interior_mask)

    edges, highlight = lineart.suppress_highlights(
        edges, gray_pre, mask=interior_mask,
        percentile=args.highlight_percentile, protect=highlight_protect,
    )
    edges = lineart.despeckle(edges, args.despeckle)
    save_debug(debug_dir, "04_edges_final.png", edges)

    return edges, highlight


def convert(args):
    image = cv2.imread(args.input)
    if image is None:
        raise ValueError(f"Could not read '{args.input}' as an image")

    # Work at a bounded resolution: the flow field and hatching parameters
    # are tuned in pixels, and coherence does not improve past ~1200px.
    # Small inputs are upscaled instead -- see --min-dim.
    full_h, full_w = image.shape[:2]
    longest = max(full_h, full_w)
    scale = 1.0
    if longest > args.max_dim:
        scale = args.max_dim / longest
    elif longest < args.min_dim:
        scale = args.min_dim / longest
    if scale != 1.0:
        work = cv2.resize(
            image,
            (int(round(full_w * scale)), int(round(full_h * scale))),
            interpolation=cv2.INTER_AREA if scale < 1.0 else cv2.INTER_CUBIC,
        )
    else:
        work = image

    debug_dir = args.debug_dir

    # Subject mask + silhouette (same approach as outline.py).
    interior_mask = None
    silhouette = []
    if not args.no_mask:
        session = segment.load_session(args.mask_model)
        alpha = segment.subject_alpha(work, session, refine_radius=args.refine)
        boundary_mask = segment.binary_mask(alpha, erode=0)
        interior_mask = segment.binary_mask(alpha, erode=args.mask_erode)
        silhouette = strokelib.silhouette_strokes(boundary_mask)

    # Face landmarks drive both the budget weights and the threshold map.
    face = facelib.detect_face(work, args.face_model)
    weights = facelib.priority_map(work.shape, face) if face else None

    gray = cv2.cvtColor(work, cv2.COLOR_BGR2GRAY)
    gray_pre = lineart.preprocess(gray, d=args.pre_d, passes=args.pre_passes)
    save_debug(debug_dir, "01_preprocessed.png", gray_pre)

    # One flow field shared by FDoG and hatching, so both layers follow the
    # same grain and read as one hand.
    flow = lineart.edge_tangent_flow(gray_pre, radius=args.etf_radius,
                                     iterations=args.etf_iters)
    save_debug(debug_dir, "05_flow_field.png", flow_visualization(*flow))

    if args.no_landmarks or face is None:
        tau_map = np.full(gray.shape, args.tau, dtype=np.float32)
        protect = None
    else:
        tau_map = lineart.feature_tau_map(gray.shape, face, base_tau=args.tau,
                                          feature_tau=args.feature_tau)
        protect = tau_map > args.tau + 0.01
        save_debug(debug_dir, "06_tau_map.png",
                   (tau_map / max(args.feature_tau, args.tau) * 255)
                   .astype(np.uint8))

    edges, _ = extract_structural(gray_pre, flow, args, tau_map,
                                  interior_mask, protect, debug_dir)

    # XDoG/FDoG ink regions are several pixels wide; skeletonizing them (as
    # strokes.edge_skeleton does for Canny) yields one centerline per line.
    skeleton = skeletonize(edges > 0)
    traced = [strokelib.Stroke(points, False, False)
              for points in strokelib.trace_skeleton(skeleton)]

    structural = silhouette + traced
    structural = strokelib.filter_by_length(
        structural, weights, args.min_length, args.face_min_length,
        face_threshold=FACE_THRESHOLD,
    )
    structural, dropped, epsilon = strokelib.apply_budget(
        structural, max_strokes=args.max_strokes, max_points=args.max_points,
        epsilon=args.simplify, smooth_iterations=args.smooth, weights=weights,
        face_share=args.face_share, face_threshold=FACE_THRESHOLD,
    )
    if not structural:
        raise ValueError("No structural strokes survived; try raising --tau "
                         "or lowering --min-length.")

    structural += embolden(structural, weights, args.bold_offset)

    hatch = []
    if not args.no_hatch:
        tx, ty, _ = flow
        polylines = hatching.hatch_strokes(
            gray_pre, tx, ty, mask=interior_mask,
            darkness_min=args.hatch_min, spacing_min=args.hatch_spacing,
            spacing_max=args.hatch_spacing_max,
            max_length=args.hatch_max_length, cross=args.cross_hatch,
            max_strokes=args.max_hatch_strokes,
        )
        hatch = [
            strokelib.Stroke(
                strokelib.smooth(strokelib.simplify(points, 0.75), 1), False,
                False)
            for points in polylines
        ]
        if debug_dir:
            save_debug(debug_dir, "07_hatch_layer.png",
                       render_layers(work.shape, [], hatch, 1))

    # Back to the input image's frame, then order for pen travel: structure
    # first, shading second, each greedily nearest-neighbour.
    structural = scale_strokes(structural, 1.0 / scale if scale else 1.0)
    hatch = scale_strokes(hatch, 1.0 / scale if scale else 1.0)
    structural = strokelib.order_strokes(structural)
    origin = (structural[-1].points[-1] if structural else np.zeros(2))
    hatch = strokelib.order_strokes(hatch, origin=tuple(origin)) if hatch else []
    combined = structural + hatch

    strokelib.write_csv(args.output, combined)
    if args.svg:
        write_svg(args.svg, [("structural", structural), ("hatching", hatch)],
                  (full_w, full_h))
    if args.preview:
        preview = render_layers(image.shape, structural, hatch, args.thickness)
        if not cv2.imwrite(args.preview, preview):
            raise IOError(f"Failed to write preview to '{args.preview}'")

    return {
        "engine": args.engine,
        "face": "landmarks" if face else "none detected",
        "structural": len(structural),
        "hatch": len(hatch),
        "dropped": dropped,
        "points": strokelib.total_points(combined),
        "epsilon": epsilon,
        "travel": strokelib.travel_distance(combined),
        "size": (full_w, full_h),
    }


def main():
    args = parse_args()
    try:
        stats = convert(args)
    except (FileNotFoundError, ValueError, IOError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(1)

    total = stats["structural"] + stats["hatch"]
    print(f"Saved {total} strokes / {stats['points']} points to {args.output}")
    print(f"  engine       {stats['engine']}, face: {stats['face']}")
    print(f"  structural   {stats['structural']} strokes "
          f"({stats['dropped']} dropped over budget)")
    print(f"  hatching     {stats['hatch']} strokes")
    print(f"  simplify     {stats['epsilon']:.2f} px tolerance")
    print(f"  pen-up trav. {stats['travel']:.0f} px")


if __name__ == "__main__":
    main()
