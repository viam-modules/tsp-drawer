#!/usr/bin/env python3
"""Luminance-driven cross-contour hatching: shading for a single-color pen.

With one pen and no fills, value has to come from line density. Strokes here
are traced directly as polylines through the edge tangent flow field (the same
field FDoG filters along, so shading and structural lines share one "hand"),
with spacing between neighbouring strokes inversely proportional to local
luminance: darker means denser. Because strokes follow the flow they curve
with the form -- around a cheek, along the nose -- which reads as 3D shading
rather than flat texture.

Placement is a simplified Jobard-Lehmann streamline seeding: candidate seeds
are visited darkest-first, a streamline is grown in both directions until it
hits something (too bright, off the subject, too close to an existing stroke),
and accepted strokes register their points in a coarse occupancy grid that
enforces the spacing for everyone after them.
"""

import cv2
import numpy as np


def smooth_direction_field(tx, ty, sigma=6.0):
    """Gaussian-smooth a tangent field for use as a hatching direction.

    The raw ETF is exactly right for edge filtering but too twitchy for
    shading: in flat regions the gradient is noise, and hatch strokes traced
    through it curl like fingerprints. Tangents are sign-free (t and -t mean
    the same line), so the components are averaged in doubled-angle space --
    blurring (cos 2a, sin 2a) and halving the result -- which is the standard
    structure-tensor trick to stop opposite tangents cancelling.
    """
    if sigma <= 0:
        return tx, ty
    angle2 = 2.0 * np.arctan2(ty, tx)
    vx = cv2.GaussianBlur(np.cos(angle2), (0, 0), sigma)
    vy = cv2.GaussianBlur(np.sin(angle2), (0, 0), sigma)
    half = 0.5 * np.arctan2(vy, vx)
    return np.cos(half).astype(np.float32), np.sin(half).astype(np.float32)


class _Occupancy:
    """Coarse grid of accepted stroke points for nearest-neighbour queries."""

    def __init__(self, cell):
        self.cell = max(1.0, float(cell))
        self.grid = {}

    def _key(self, x, y):
        return (int(x // self.cell), int(y // self.cell))

    def add(self, x, y):
        self.grid.setdefault(self._key(x, y), []).append((x, y))

    def too_close(self, x, y, radius):
        reach = int(np.ceil(radius / self.cell))
        cx, cy = self._key(x, y)
        r2 = radius * radius
        for gx in range(cx - reach, cx + reach + 1):
            for gy in range(cy - reach, cy + reach + 1):
                for px, py in self.grid.get((gx, gy), ()):
                    dx, dy = px - x, py - y
                    if dx * dx + dy * dy < r2:
                        return True
        return False


def _bilinear(field, x, y):
    height, width = field.shape
    x = min(max(x, 0.0), width - 1.001)
    y = min(max(y, 0.0), height - 1.001)
    x0, y0 = int(x), int(y)
    fx, fy = x - x0, y - y0
    top = field[y0, x0] * (1 - fx) + field[y0, x0 + 1] * fx
    bottom = field[y0 + 1, x0] * (1 - fx) + field[y0 + 1, x0 + 1] * fx
    return top * (1 - fy) + bottom * fy


def hatch_strokes(gray, tx, ty, mask=None, darkness_min=0.35,
                  spacing_min=4.0, spacing_max=14.0, min_length=6.0,
                  max_length=80.0, cross=False, cross_darkness_min=0.65,
                  max_strokes=100000, seed_step=3, smooth_sigma=6.0):
    """Trace hatch polylines over the dark regions of a grayscale image.

    gray is the *preprocessed* image (0..255); tx/ty the unit tangent field
    from lineart.edge_tangent_flow. Only pixels inside `mask` (if given) and
    darker than darkness_min (0 = white, 1 = black) are hatched. Local stroke
    spacing runs from spacing_max at the threshold down to spacing_min in the
    darkest shadows. Strokes are capped at max_length so shading stays made of
    discrete pen marks rather than one long scribble.

    cross=True adds a second, perpendicular pass over regions darker than
    cross_darkness_min, deepening the darkest shadows. All lengths are pixels
    at the working scale.

    Returns a list of float32 Nx2 (x, y) polylines.
    """
    darkness = 1.0 - gray.astype(np.float32) / 255.0
    if mask is not None:
        darkness = np.where(mask > 0, darkness, 0.0)

    tx, ty = smooth_direction_field(tx, ty, smooth_sigma)

    strokes = _hatch_pass(darkness, tx, ty, darkness_min, spacing_min,
                          spacing_max, min_length, max_length, max_strokes,
                          seed_step)
    if cross:
        remaining = max_strokes - len(strokes)
        if remaining > 0:
            # Perpendicular field; spacing re-mapped over the darker range.
            strokes += _hatch_pass(darkness, -ty, tx, cross_darkness_min,
                                   spacing_min, spacing_max, min_length,
                                   max_length, remaining, seed_step)
    return strokes


def _hatch_pass(darkness, tx, ty, level, spacing_min, spacing_max,
                min_length, max_length, max_strokes, seed_step):
    height, width = darkness.shape

    def spacing_at(value):
        t = (value - level) / max(1e-6, 1.0 - level)
        t = min(max(t, 0.0), 1.0)
        return spacing_max + (spacing_min - spacing_max) * t

    # Seeds darkest-first, so the densest shading claims its space before
    # lighter regions fill in around it.
    ys, xs = np.mgrid[0:height:seed_step, 0:width:seed_step]
    ys, xs = ys.ravel(), xs.ravel()
    values = darkness[ys, xs]
    eligible = values > level
    order = np.argsort(-values[eligible])
    seeds = np.column_stack([xs[eligible][order], ys[eligible][order]])

    occupancy = _Occupancy(spacing_min)
    strokes = []

    for sx, sy in seeds.astype(np.float32):
        if len(strokes) >= max_strokes:
            break
        seed_dark = _bilinear(darkness, sx, sy)
        if seed_dark <= level:
            continue
        if occupancy.too_close(sx, sy, spacing_at(seed_dark)):
            continue

        halves = []
        for direction in (1.0, -1.0):
            halves.append(_trace(darkness, tx, ty, sx, sy, direction, level,
                                 occupancy, spacing_at, max_length / 2.0))
        # Join the two half-streamlines through the seed.
        points = halves[1][::-1] + [(sx, sy)] + halves[0]
        if _polyline_length(points) < min_length:
            continue

        stroke = np.array(points, dtype=np.float32)
        strokes.append(stroke)
        for x, y in points:
            occupancy.add(x, y)

    return strokes


def _trace(darkness, tx, ty, sx, sy, direction, level, occupancy, spacing_at,
           max_length):
    """Grow one half of a streamline; returns points beyond the seed."""
    height, width = darkness.shape
    x, y = sx, sy
    dx = _bilinear(tx, x, y) * direction
    dy = _bilinear(ty, x, y) * direction
    points = []
    travelled = 0.0
    step = 1.0

    while travelled < max_length:
        norm = np.hypot(dx, dy)
        if norm < 1e-6:
            break
        dx, dy = dx / norm, dy / norm

        # Midpoint (RK2) step through the direction field.
        mx, my = x + 0.5 * step * dx, y + 0.5 * step * dy
        if not (0 <= mx < width - 1 and 0 <= my < height - 1):
            break
        ndx, ndy = _bilinear(tx, mx, my), _bilinear(ty, mx, my)
        if ndx * dx + ndy * dy < 0:
            ndx, ndy = -ndx, -ndy
        mnorm = np.hypot(ndx, ndy)
        if mnorm < 1e-6:
            break
        nx = x + step * ndx / mnorm
        ny = y + step * ndy / mnorm
        if not (0 <= nx < width - 1 and 0 <= ny < height - 1):
            break

        value = _bilinear(darkness, nx, ny)
        if value <= level:
            break
        # 0.75: allow a stroke to run alongside a neighbour slightly closer
        # than seeding allows, so lines do not stop the moment they converge.
        if occupancy.too_close(nx, ny, 0.75 * spacing_at(value)):
            break

        points.append((nx, ny))
        travelled += step
        x, y = nx, ny
        dx, dy = ndx, ndy

    return points


def _polyline_length(points):
    total = 0.0
    for (x0, y0), (x1, y1) in zip(points, points[1:]):
        total += np.hypot(x1 - x0, y1 - y0)
    return total
