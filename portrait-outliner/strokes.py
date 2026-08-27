#!/usr/bin/env python3
"""Turn a portrait into plotter-ready pen strokes.

The unit of work here is a *stroke*: one continuous pen-down polyline. The
robot arm draws a stroke without lifting, then lifts and travels to the start
of the next one, so the quality measures that matter are stroke count, point
count, and total pen-up travel -- not how the result looks rasterized.
"""

from collections import namedtuple

import cv2
import numpy as np
from skimage.morphology import skeletonize

# points: float32 Nx2 array of (x, y) pixel coordinates.
# closed: whether the last point should connect back to the first.
# protected: exempt from budget pruning. Used for the silhouette, which
#            defines the portrait and must survive however tight the budget.
Stroke = namedtuple("Stroke", "points closed protected")

# 8-connected neighbourhood, split so diagonal adjacency can be de-prioritised
# when tracing (see build_adjacency).
ORTHOGONAL = ((-1, 0), (1, 0), (0, -1), (0, 1))
DIAGONAL = ((-1, -1), (-1, 1), (1, -1), (1, 1))


def arc_length(points, closed=False):
    """Length of a polyline in pixels."""
    if len(points) < 2:
        return 0.0
    return float(cv2.arcLength(points.reshape(-1, 1, 2).astype(np.float32), closed))


def edge_skeleton(image, interior_mask, clahe_clip, blur, canny_low, canny_high,
                  close_iterations):
    """Detect interior feature edges and thin them to single-pixel centerlines.

    Skeletonizing is the step that makes the output drawable. Canny returns
    edges as thin regions, and tracing their *boundary* (what findContours
    does) yields a loop that runs up one side of every line and back down the
    other -- the arm would draw each line twice. The skeleton is the line
    itself, so tracing it yields one stroke per line.
    """
    gray = cv2.cvtColor(image, cv2.COLOR_BGR2GRAY)

    # Faces are frequently lit so that eyes, nostrils and lips sit in a narrow
    # band of the histogram, and a global Canny threshold misses them while
    # still firing on the brightly lit cheek. CLAHE equalises locally, which
    # brings out those features without amplifying noise across the whole frame.
    if clahe_clip > 0:
        clahe = cv2.createCLAHE(clipLimit=clahe_clip, tileGridSize=(8, 8))
        gray = clahe.apply(gray)

    # Bilateral filtering smooths flat regions (skin, background) while
    # preserving strong edges, which keeps facial-feature boundaries crisp
    # while suppressing noise that would otherwise show up as stray outlines.
    smoothed = cv2.bilateralFilter(gray, d=blur, sigmaColor=75, sigmaSpace=75)
    edges = cv2.Canny(smoothed, canny_low, canny_high)

    # Canny cannot tell a cheekbone from a door frame, so edges outside the
    # subject are discarded. The mask is eroded by the caller, which also drops
    # the strong subject-vs-background gradient -- that boundary gets drawn
    # once, cleanly, as the silhouette instead of twice as parallel lines.
    if interior_mask is not None:
        edges = cv2.bitwise_and(edges, interior_mask)

    # Closing (dilate then erode) bridges the small gaps Canny leaves in an
    # otherwise continuous edge, so tracing produces a few long strokes rather
    # than many short fragments. Unlike a plain dilation it does not leave the
    # lines permanently thickened, which would merge neighbouring features.
    if close_iterations > 0:
        kernel = np.ones((3, 3), np.uint8)
        edges = cv2.morphologyEx(
            edges, cv2.MORPH_CLOSE, kernel, iterations=close_iterations
        )

    return skeletonize(edges > 0)


def build_adjacency(skeleton):
    """Map each skeleton pixel to its neighbours, with staircases resolved.

    A skeleton one pixel wide still contains diagonal steps, and at each step
    the diagonal neighbour and an orthogonal neighbour are themselves adjacent.
    That triangle makes an ordinary line pixel look like a degree-3 junction
    and would chop every diagonal line into fragments. Dropping the diagonal
    edge whenever the two pixels are already connected through an orthogonal
    one leaves the true junctions -- where features genuinely meet -- intact.
    """
    pixels = set(zip(*np.nonzero(skeleton)))
    adjacency = {}

    for pixel in pixels:
        row, col = pixel
        orthogonal = [
            (row + dr, col + dc)
            for dr, dc in ORTHOGONAL
            if (row + dr, col + dc) in pixels
        ]
        neighbours = list(orthogonal)

        for dr, dc in DIAGONAL:
            diagonal = (row + dr, col + dc)
            if diagonal not in pixels:
                continue
            # Redundant if some orthogonal neighbour also touches the diagonal.
            shared = any(
                abs(diagonal[0] - o[0]) <= 1 and abs(diagonal[1] - o[1]) <= 1
                for o in orthogonal
            )
            if not shared:
                neighbours.append(diagonal)

        adjacency[pixel] = neighbours

    return adjacency


def trace_skeleton(skeleton):
    """Walk a skeleton into open polylines, one per line segment.

    Paths are cut at endpoints (degree 1) and junctions (degree 3+), so a
    feature that forks becomes several strokes meeting at the fork rather than
    one stroke that doubles back.
    """
    adjacency = build_adjacency(skeleton)
    degree = {pixel: len(nbrs) for pixel, nbrs in adjacency.items()}
    junctions = {pixel for pixel, count in degree.items() if count >= 3}
    used = set()
    paths = []

    def walk(start, first_step):
        path = [start, first_step]
        used.add(frozenset((start, first_step)))
        previous, current = start, first_step

        # Stop on anything that is not a plain through-pixel.
        while current not in junctions and degree[current] == 2:
            nexts = [
                n
                for n in adjacency[current]
                if n != previous and frozenset((current, n)) not in used
            ]
            if not nexts:
                break
            step = nexts[0]
            used.add(frozenset((current, step)))
            path.append(step)
            previous, current = current, step

        return path

    endpoints = [pixel for pixel, count in degree.items() if count == 1]

    # Endpoints first so open lines are traced end to end; junctions next to
    # pick up the segments between forks.
    for start in sorted(endpoints) + sorted(junctions):
        for neighbour in adjacency[start]:
            if frozenset((start, neighbour)) not in used:
                paths.append(walk(start, neighbour))

    # Whatever remains is a closed loop of degree-2 pixels, which has neither
    # an endpoint nor a junction to start from.
    for pixel in sorted(adjacency):
        for neighbour in adjacency[pixel]:
            if frozenset((pixel, neighbour)) not in used:
                paths.append(walk(pixel, neighbour))

    # Skeleton coordinates are (row, col); strokes are (x, y).
    return [
        np.array([(col, row) for row, col in path], dtype=np.float32)
        for path in paths
        if len(path) >= 2
    ]


def silhouette_strokes(mask, margin=1, min_length=8.0):
    """Trace the subject's silhouette, dropping the frame-cropped parts.

    Portraits are usually cropped through the shoulders, so the mask runs off
    the bottom of the frame. The mask boundary there is an artifact of the crop
    rather than an edge of the person, and drawing it puts a hard line across
    the bottom of the portrait. Points on the frame border are dropped and the
    silhouette is emitted as open polylines around them.

    CHAIN_APPROX_NONE keeps every boundary pixel; the compressed
    representation would only retain the corners of a border run, which is too
    coarse to split reliably.

    Contours shorter than min_length are discarded. An isolated speck in the
    mask is traced as a valid one- or two-point closed contour, and because
    silhouette strokes are exempt from later length filtering it would survive
    all the way to the output, spending a stroke of the budget on a dot.
    """
    contours, _ = cv2.findContours(mask, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_NONE)
    height, width = mask.shape
    result = []

    for contour in contours:
        points = contour.reshape(-1, 2).astype(np.float32)
        on_border = (
            (points[:, 0] <= margin)
            | (points[:, 0] >= width - 1 - margin)
            | (points[:, 1] <= margin)
            | (points[:, 1] >= height - 1 - margin)
        )

        if not on_border.any():
            if arc_length(points, True) >= min_length:
                result.append(Stroke(points, True, True))
            continue

        # Contours are cyclic, so rotate to start on a border point. That way a
        # run of real silhouette is never split across the array ends.
        start = int(np.argmax(on_border))
        points = np.roll(points, -start, axis=0)
        on_border = np.roll(on_border, -start)

        def flush(run):
            if len(run) < 2:
                return
            segment = np.array(run, dtype=np.float32)
            if arc_length(segment, False) >= min_length:
                result.append(Stroke(segment, False, True))

        run = []
        for point, is_border in zip(points, on_border):
            if is_border:
                flush(run)
                run = []
            else:
                run.append(point)
        flush(run)

    return result


def stroke_weight(stroke, weights):
    """Mean importance of the pixels a stroke passes through.

    Averaging along the whole stroke rather than testing its centroid means a
    long hair strand that merely clips the face scores low, while a short
    eyebrow stroke sitting entirely on a feature scores high.
    """
    if weights is None:
        return 1.0

    height, width = weights.shape
    columns = np.clip(np.round(stroke.points[:, 0]).astype(int), 0, width - 1)
    rows = np.clip(np.round(stroke.points[:, 1]).astype(int), 0, height - 1)

    return float(weights[rows, columns].mean())


def filter_by_length(strokes, weights, min_length, face_min_length, face_threshold):
    """Drop strokes too short to be worth a pen lift, with a lower bar on the face.

    A single global threshold is what previously erased the eyebrows and
    eyelids: those strokes are only a handful of pixels long, so any threshold
    high enough to suppress hair and fabric speckle also discards the features
    that make the portrait recognizable. Scoring the stroke's location first
    lets the face keep its short strokes while the rest of the frame is held to
    the stricter limit.
    """
    kept = []
    for stroke in strokes:
        on_face = stroke_weight(stroke, weights) >= face_threshold
        limit = face_min_length if on_face else min_length
        if stroke.protected or arc_length(stroke.points, stroke.closed) >= limit:
            kept.append(stroke)

    return kept


def simplify(points, epsilon, closed=False):
    """Douglas-Peucker reduction: drop points that add no shape information.

    A traced skeleton has one point per pixel, which is far more resolution
    than the arm needs and makes the CSV needlessly large.
    """
    if epsilon <= 0 or len(points) < 3:
        return points

    reduced = cv2.approxPolyDP(points.reshape(-1, 1, 2), epsilon, closed)
    reduced = reduced.reshape(-1, 2).astype(np.float32)

    # A stroke only a few pixels long can collapse to a single point, which
    # would silently drop it. Those are exactly the eyebrow and eyelid strokes
    # worth protecting, so keep them as a straight dash between their ends.
    if len(reduced) < 2:
        return np.array([points[0], points[-1]], dtype=np.float32)

    return reduced


def smooth(points, iterations, closed=False):
    """Chaikin corner cutting, to round the staircase left by pixel tracing.

    Simplification leaves polylines with hard corners at pixel-grid angles,
    which the arm has to decelerate into. Cutting each corner twice per pass
    approximates a quadratic B-spline through the same shape.
    """
    if iterations <= 0 or len(points) < 3:
        return points

    for _ in range(iterations):
        if closed:
            starts = points
            ends = np.roll(points, -1, axis=0)
        else:
            starts = points[:-1]
            ends = points[1:]

        cut = np.empty((len(starts) * 2, 2), dtype=np.float32)
        cut[0::2] = starts * 0.75 + ends * 0.25
        cut[1::2] = starts * 0.25 + ends * 0.75

        if closed:
            points = cut
        else:
            # Endpoints are anchors: moving them would shorten the stroke.
            points = np.vstack([points[:1], cut, points[-1:]])

    return points


def order_strokes(strokes, origin=(0.0, 0.0)):
    """Greedy nearest-neighbour ordering, reversing strokes when that helps.

    Every gap between consecutive strokes is a pen-up move the arm has to make
    but does not draw, so ordering strokes by proximity cuts drawing time
    without changing the picture at all. A stroke's direction is free to flip,
    which roughly halves the candidate distances.
    """
    remaining = list(strokes)
    ordered = []
    position = np.array(origin, dtype=np.float32)

    while remaining:
        best_index, best_reversed, best_distance = 0, False, float("inf")

        for index, stroke in enumerate(remaining):
            to_start = float(np.linalg.norm(stroke.points[0] - position))
            if to_start < best_distance:
                best_index, best_reversed, best_distance = index, False, to_start

            # A closed loop is drawn from wherever we enter it, so reversing an
            # open stroke is the only direction choice that matters here.
            if not stroke.closed:
                to_end = float(np.linalg.norm(stroke.points[-1] - position))
                if to_end < best_distance:
                    best_index, best_reversed, best_distance = index, True, to_end

        stroke = remaining.pop(best_index)
        if best_reversed:
            stroke = stroke._replace(points=stroke.points[::-1])

        ordered.append(stroke)
        position = stroke.points[0] if stroke.closed else stroke.points[-1]

    return ordered


def travel_distance(strokes, origin=(0.0, 0.0)):
    """Total pen-up distance for a stroke order, for reporting."""
    position = np.array(origin, dtype=np.float32)
    total = 0.0

    for stroke in strokes:
        total += float(np.linalg.norm(stroke.points[0] - position))
        position = stroke.points[0] if stroke.closed else stroke.points[-1]

    return total


def total_points(strokes):
    return sum(len(stroke.points) for stroke in strokes)


def allocate_strokes(strokes, slots, weights, face_share, face_threshold):
    """Pick `slots` strokes, reserving a share of them for the face.

    Within the face group strokes are ranked by importance times length, so an
    eyebrow outranks a cheek line of the same size. Outside it, plain length
    decides. Whichever group cannot fill its share hands the remainder to the
    other, so a budget is never left unspent.
    """
    if slots <= 0:
        return []
    if weights is None:
        ordered = sorted(strokes, key=lambda s: arc_length(s.points, s.closed),
                         reverse=True)
        return ordered[:slots]

    scored = [(s, stroke_weight(s, weights)) for s in strokes]
    face = [(s, w) for s, w in scored if w >= face_threshold]
    rest = [(s, w) for s, w in scored if w < face_threshold]

    face.sort(key=lambda sw: sw[1] * arc_length(sw[0].points, sw[0].closed),
              reverse=True)
    rest.sort(key=lambda sw: arc_length(sw[0].points, sw[0].closed), reverse=True)

    face_slots = min(len(face), int(round(slots * face_share)))
    rest_slots = slots - face_slots

    # Hand back anything the other group cannot use.
    if rest_slots > len(rest):
        face_slots = min(len(face), face_slots + rest_slots - len(rest))
        rest_slots = slots - face_slots

    return [s for s, _ in face[:face_slots]] + [s for s, _ in rest[:rest_slots]]


def apply_budget(strokes, max_strokes, max_points, epsilon, smooth_iterations,
                 weights=None, face_share=0.8, face_threshold=2.5,
                 epsilon_cap=12.0):
    """Fit strokes into a stroke/point budget, keeping the most salient lines.

    Two separate pressures, handled separately. Too many strokes means too many
    pen lifts, and the fix is to drop the least significant ones. Rather than
    ranking every stroke together -- which lets a few hundred pixels of hair
    outbid an eyebrow -- the budget is split, so a guaranteed share is spent on
    the face and hair and clothing compete only with each other. Too many
    points means an oversized path, and the fix is coarser simplification,
    which shortens the file without removing any line.

    Budgeting rather than fixed thresholds keeps draw time predictable across
    subjects, since contrast and lighting otherwise swing the edge count wildly.
    """
    protected = [s for s in strokes if s.protected]
    optional = [s for s in strokes if not s.protected]

    if max_strokes > 0:
        slots = max(0, max_strokes - len(protected))
        optional = allocate_strokes(optional, slots, weights, face_share,
                                    face_threshold)

    kept = protected + optional
    dropped = len(strokes) - len(kept)

    # Escalate simplification until the point budget is met. The cap stops a
    # tight budget from degenerating strokes into straight lines.
    while True:
        processed = [
            s._replace(
                points=smooth(
                    simplify(s.points, epsilon, s.closed), smooth_iterations, s.closed
                )
            )
            for s in kept
        ]
        processed = [s for s in processed if len(s.points) >= 2]

        if max_points <= 0 or total_points(processed) <= max_points:
            return processed, dropped, epsilon
        if epsilon >= epsilon_cap:
            return processed, dropped, epsilon

        epsilon = min(epsilon * 1.4 if epsilon > 0 else 0.5, epsilon_cap)


def write_csv(path, strokes):
    """Write contour,x,y rows with contour increasing down the file."""
    with open(path, "w") as handle:
        handle.write("contour,x,y\n")

        for index, stroke in enumerate(strokes):
            points = stroke.points
            if stroke.closed:
                # Repeat the first point so the arm closes the loop rather than
                # leaving a gap; nothing downstream knows the stroke was closed.
                points = np.vstack([points, points[:1]])

            for x, y in points:
                handle.write(f"{index},{x:.2f},{y:.2f}\n")


def render_preview(shape, strokes, thickness=2):
    """Draw the strokes exactly as the arm will, for eyeballing the result."""
    height, width = shape[:2]
    canvas = np.full((height, width, 3), 255, dtype=np.uint8)

    for stroke in strokes:
        cv2.polylines(
            canvas,
            [np.round(stroke.points).astype(np.int32).reshape(-1, 1, 2)],
            isClosed=stroke.closed,
            color=(0, 0, 0),
            thickness=thickness,
            lineType=cv2.LINE_AA,
        )

    return canvas
