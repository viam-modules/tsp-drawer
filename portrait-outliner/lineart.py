#!/usr/bin/env python3
"""Structural line extraction that reads as drawn rather than detected.

This module is the alternative to the Canny stage in strokes.edge_skeleton.
Canny answers "where is there a gradient?", which fragments lines wherever
contrast dips and fires on pores and JPEG noise. The operators here answer
"where would an illustrator ink a line?":

- xdog: difference of Gaussians pushed through a soft tanh threshold, which
  produces bold, filled-in line work instead of one-pixel gradient ridges.
- edge_tangent_flow + fdog: first smooth the image's tangent field so nearby
  edges agree on a direction, then run the DoG *along* that flow. Edges then
  reinforce each other lengthwise, so a lip line or jaw line comes out as one
  continuous curling stroke even where its contrast fades.

Both return a soft response in [0, 1] (1 = paper, 0 = confident ink) rather
than a binary map, so binarize() can apply a spatially varying threshold: the
feature_tau_map built from face landmarks lowers the bar (raises tau) around
eyes, brows, nose and mouth, letting real-but-faint feature edges survive a
global threshold that would have dropped them.

suppress_highlights removes lines where light hits the face directly, which is
what illustrators do: an outline that continues through the lit side of the
nose reads as flat.
"""

import cv2
import numpy as np


def preprocess(gray, d=9, sigma_color=75, sigma_space=75, passes=1):
    """Bilateral-filter a grayscale image before line extraction.

    Smooths pores, stubble and compression artifacts (which otherwise become
    false edges) while keeping structural boundaries sharp. More passes for
    noisier or higher-resolution photos; too many erases eyelid creases and
    nostril definition.
    """
    out = gray
    for _ in range(max(0, passes)):
        out = cv2.bilateralFilter(out, d=d, sigmaColor=sigma_color,
                                  sigmaSpace=sigma_space)
    return out


def xdog(gray, sigma=1.2, k=1.6, p=40.0, epsilon=0.1, phi=10.0):
    """eXtended Difference of Gaussians, returning a soft response in [0, 1].

    sigma picks the detail scale, k the ratio between the two blurs, p how
    bold the lines are, and phi how sharply the soft threshold cuts (low phi
    keeps sketchy grey transitions -- useful while tuning to see where the
    detector is uncertain -- high phi approaches binary).

    The blurs run on float32: on uint8 the g1 - g2 subtraction would wrap
    around and turn every negative lobe into noise.

    Defaults were tuned on real photos: the often-quoted epsilon of 0.01
    keeps almost nothing on a normally exposed portrait, since the luminance
    term dominates the normalized diff. Around 0.1 with a strong p the DoG
    term decides instead. Note high epsilon turns whole dark regions solid,
    which a pen cannot fill.
    """
    f = gray.astype(np.float32)
    g1 = cv2.GaussianBlur(f, (0, 0), sigma)
    g2 = cv2.GaussianBlur(f, (0, 0), sigma * k)
    diff = (g1 - p * (g1 - g2)) / 255.0
    soft = np.where(diff >= epsilon, 1.0, 1.0 + np.tanh(phi * (diff - epsilon)))
    return np.clip(soft, 0.0, 1.0).astype(np.float32)


def edge_tangent_flow(gray, radius=5, iterations=3):
    """Smoothed tangent field: the local "grain direction" of the image.

    Returns (tx, ty, magnitude): unit tangents (perpendicular to the local
    gradient) plus the normalized gradient magnitude. Each iteration replaces
    a pixel's tangent with a weighted average over its disc neighbourhood,
    where neighbours count more when their gradient is stronger (wm) and when
    they already point the same way (wd); the sign flip (phi) stops opposite
    but parallel tangents from cancelling. This is the ETF construction from
    Kang et al., "Coherent Line Drawing" (NPAR 2007).

    fdog() runs its filter along this field, and hatching uses it as the
    stroke direction, so both layers share the same "hand".
    """
    f = gray.astype(np.float32) / 255.0
    gx = cv2.Sobel(f, cv2.CV_32F, 1, 0, ksize=3)
    gy = cv2.Sobel(f, cv2.CV_32F, 0, 1, ksize=3)
    mag = np.sqrt(gx * gx + gy * gy)
    mag = mag / (mag.max() + 1e-12)

    # Initial tangent: gradient rotated 90 degrees, normalized.
    norm = np.sqrt(gx * gx + gy * gy) + 1e-12
    tx = -gy / norm
    ty = gx / norm

    height, width = f.shape
    offsets = [
        (dy, dx)
        for dy in range(-radius, radius + 1)
        for dx in range(-radius, radius + 1)
        if 0 < dy * dy + dx * dx <= radius * radius
    ]

    for _ in range(max(0, iterations)):
        pad_tx = cv2.copyMakeBorder(tx, radius, radius, radius, radius,
                                    cv2.BORDER_REFLECT)
        pad_ty = cv2.copyMakeBorder(ty, radius, radius, radius, radius,
                                    cv2.BORDER_REFLECT)
        pad_mag = cv2.copyMakeBorder(mag, radius, radius, radius, radius,
                                     cv2.BORDER_REFLECT)

        # Centre pixel: phi = 1, wd = 1, wm = 0.5.
        acc_x = 0.5 * tx
        acc_y = 0.5 * ty
        for dy, dx in offsets:
            ntx = pad_tx[radius + dy:radius + dy + height,
                         radius + dx:radius + dx + width]
            nty = pad_ty[radius + dy:radius + dy + height,
                         radius + dx:radius + dx + width]
            nmag = pad_mag[radius + dy:radius + dy + height,
                           radius + dx:radius + dx + width]
            dot = tx * ntx + ty * nty
            weight = np.where(dot >= 0, 1.0, -1.0) \
                * 0.5 * (1.0 + np.tanh(nmag - mag)) \
                * np.abs(dot)
            acc_x = acc_x + weight * ntx
            acc_y = acc_y + weight * nty

        norm = np.sqrt(acc_x * acc_x + acc_y * acc_y)
        keep = norm > 1e-12
        tx = np.where(keep, acc_x / np.maximum(norm, 1e-12), tx)
        ty = np.where(keep, acc_y / np.maximum(norm, 1e-12), ty)

    return tx.astype(np.float32), ty.astype(np.float32), mag.astype(np.float32)


def _gauss(s, sigma):
    return np.exp(-(s * s) / (2.0 * sigma * sigma)) / (np.sqrt(2.0 * np.pi) * sigma)


def fdog(gray, tx, ty, sigma_c=1.0, sigma_m=3.0, rho=0.99, phi=3.0,
         iterations=2):
    """Flow-based DoG along an edge tangent flow. Soft response in [0, 1].

    Two separable passes per iteration (Kang et al. 2007): a 1D DoG sampled
    across the flow (along the gradient), then a 1D Gaussian accumulated by
    walking the streamline of the tangent field in both directions. The
    along-flow pass is what joins fragments into continuous strokes: a weak
    response at one point is reinforced by strong responses further along the
    same flow line.

    Extra iterations re-ink detected lines into the image and filter again,
    which thickens and further connects them. sigma_c sets line scale, sigma_m
    how far coherence reaches along the flow, rho edge sensitivity, phi the
    sharpness of the soft threshold.

    The filter response is normalized by a high percentile of its negative
    lobe before the tanh, so phi and binarize() thresholds behave consistently
    across photos.
    """
    src = gray.astype(np.float32) / 255.0
    height, width = src.shape
    xs, ys = np.meshgrid(np.arange(width, dtype=np.float32),
                         np.arange(height, dtype=np.float32))

    sigma_s = 1.6 * sigma_c
    across = max(2, int(np.ceil(2.0 * sigma_s)))
    along = max(2, int(np.ceil(2.0 * sigma_m)))

    # Gradient direction = tangent rotated back 90 degrees.
    gxd, gyd = ty, -tx

    def sample(img, mapx, mapy):
        return cv2.remap(img, mapx, mapy, cv2.INTER_LINEAR,
                         borderMode=cv2.BORDER_REPLICATE)

    soft = np.ones_like(src)
    for _ in range(max(1, iterations)):
        # Pass 1: 1D DoG across the flow.
        response = np.zeros_like(src)
        for s in range(-across, across + 1):
            weight = _gauss(s, sigma_c) - rho * _gauss(s, sigma_s)
            response += weight * sample(src, xs + s * gxd, ys + s * gyd)

        # Pass 2: Gaussian accumulated along the streamline, both directions.
        acc = _gauss(0, sigma_m) * response
        for direction in (1.0, -1.0):
            px, py = xs.copy(), ys.copy()
            step_x, step_y = direction * tx, direction * ty
            for k in range(1, along + 1):
                px += step_x
                py += step_y
                acc += _gauss(k, sigma_m) * sample(response, px, py)
                # Follow the flow: re-sample the tangent at the new position,
                # flipped where needed so the walk never reverses on itself.
                ntx = sample(tx, px, py)
                nty = sample(ty, px, py)
                flip = np.where(ntx * step_x + nty * step_y < 0, -1.0, 1.0)
                step_x, step_y = ntx * flip, nty * flip

        negative = acc[acc < 0]
        scale = np.percentile(-negative, 95) if negative.size else 1.0
        normalized = acc / max(float(scale), 1e-6)
        soft = np.where(normalized >= 0, 1.0,
                        1.0 + np.tanh(phi * normalized)).astype(np.float32)
        soft = np.clip(soft, 0.0, 1.0)

        # Re-ink: draw the detected lines into the source and filter again.
        src = np.minimum(src, soft)

    return soft


def feature_tau_map(shape, face, base_tau=0.5, feature_tau=0.82):
    """Per-pixel binarization threshold, more permissive on facial features.

    Mirrors the geometry of face.priority_map: ellipses over eyes, brows,
    nose and mouth sized in eye-spans, rotated to the eye line. Inside them
    the threshold rises to feature_tau, so fainter responses still become
    lines -- the landmarks nudge the real edge detector rather than stamping
    template curves over it. Returns a float32 map of tau values.
    """
    height, width = shape[:2]
    taus = np.full((height, width), base_tau, dtype=np.float32)
    if face is None:
        return taus

    right_eye, left_eye = face["right_eye"], face["left_eye"]
    eye_span = float(np.linalg.norm(left_eye - right_eye))
    if eye_span < 1.0:
        return taus

    delta = left_eye - right_eye
    angle = float(np.degrees(np.arctan2(delta[1], delta[0])))
    mouth_mid = (face["mouth_right"] + face["mouth_left"]) / 2.0

    def ellipse(center, half_width, half_height):
        cv2.ellipse(
            taus,
            (int(round(center[0])), int(round(center[1]))),
            (int(round(half_width)), int(round(half_height))),
            angle, 0, 360, float(feature_tau), -1,
        )

    for eye in (right_eye, left_eye):
        ellipse(eye, 0.55 * eye_span, 0.40 * eye_span)
        brow = eye - np.array([0.0, 0.45 * eye_span], dtype=np.float32)
        ellipse(brow, 0.60 * eye_span, 0.30 * eye_span)
    ellipse(face["nose"], 0.50 * eye_span, 0.55 * eye_span)
    ellipse(mouth_mid, 0.85 * eye_span, 0.40 * eye_span)

    blur = max(3, int(round(0.15 * eye_span)) | 1)
    return cv2.GaussianBlur(taus, (blur, blur), 0)


def binarize(soft, tau_map):
    """Threshold a soft response into a 0/255 ink map using a per-pixel tau."""
    return ((soft < tau_map) * 255).astype(np.uint8)


def suppress_highlights(edges, gray, mask=None, percentile=90.0,
                        protect=None):
    """Drop lines in the brightest regions, where an illustrator lifts the pen.

    The brightness cut is taken as a percentile of the subject's own pixels
    (via mask) so background brightness does not skew it. `protect` is a
    boolean map of regions exempt from suppression -- pass the facial feature
    zones, since a catchlight on an eyeball must not erase the eye outline.
    Returns (edges, highlight_mask).
    """
    if percentile >= 100.0:
        return edges, np.zeros(gray.shape, dtype=bool)

    subject = gray[mask > 0] if mask is not None else gray
    if subject.size == 0:
        return edges, np.zeros(gray.shape, dtype=bool)

    threshold = np.percentile(subject, percentile)
    highlight = gray >= threshold
    if protect is not None:
        highlight &= ~protect

    edges = edges.copy()
    edges[highlight] = 0
    return edges, highlight


def despeckle(edges, min_area=8):
    """Remove connected components smaller than min_area pixels.

    XDoG output includes isolated flecks wherever noise crossed the soft
    threshold; each would survive skeletonization as a stroke-stealing dot.
    """
    if min_area <= 1:
        return edges
    count, labels, stats, _ = cv2.connectedComponentsWithStats(edges, 8)
    keep = np.zeros(count, dtype=bool)
    keep[1:] = stats[1:, cv2.CC_STAT_AREA] >= min_area
    return np.where(keep[labels], 255, 0).astype(np.uint8)
