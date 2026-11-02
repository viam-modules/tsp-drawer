#!/usr/bin/env python3
"""Portrait subject segmentation: turn a photo into a soft alpha matte.

Shared by remove_bg.py (compositing the subject onto a flat background) and
outline.py (restricting edge detection to the subject). Both need the same
notion of "which pixels are the person", so it lives in one place.
"""

import cv2
import numpy as np
from rembg import new_session, remove

# u2net_human_seg is trained specifically on people rather than generic
# salient objects, which makes it noticeably more reliable on portraits than
# rembg's default model. birefnet-portrait is the higher-quality (and much
# slower, ~1GB) alternative when hair detail matters more than throughput.
DEFAULT_MODEL = "u2net_human_seg"


def load_session(model=DEFAULT_MODEL):
    """Create a reusable rembg session.

    Loading the ONNX model is the expensive part, so callers processing more
    than one image should build the session once and pass it in repeatedly.
    """
    return new_session(model)


def guided_filter(guide, src, radius, eps):
    """Edge-aware smoothing of `src` using `guide` as the reference image.

    The segmentation models infer at a fixed low resolution (320x320 for
    u2net), so the mask rembg hands back has been upscaled and its boundary
    sits a few pixels off the real one. A guided filter re-fits the mask to
    intensity edges in the original photo, which both sharpens the boundary
    and turns the hard mask into fractional coverage across it -- exactly what
    hair and glasses need to composite without a cut-out-with-scissors look.

    Implemented directly from box filters because cv2.ximgproc.guidedFilter
    lives in opencv-contrib, which this project does not depend on.
    """
    guide = guide.astype(np.float32)
    src = src.astype(np.float32)
    ksize = (2 * radius + 1, 2 * radius + 1)

    mean_guide = cv2.boxFilter(guide, -1, ksize)
    mean_src = cv2.boxFilter(src, -1, ksize)
    var_guide = cv2.boxFilter(guide * guide, -1, ksize) - mean_guide * mean_guide
    cov = cv2.boxFilter(guide * src, -1, ksize) - mean_guide * mean_src

    # Per-window linear model src ~= scale * guide + offset; eps penalises
    # large scales, so flat regions stay flat and edges stay sharp.
    scale = cov / (var_guide + eps)
    offset = mean_src - scale * mean_guide

    return cv2.boxFilter(scale, -1, ksize) * guide + cv2.boxFilter(offset, -1, ksize)


def refine_alpha(image, alpha, radius, eps=1e-4):
    """Re-align a model alpha to the photo's own edges. No-op if radius <= 0."""
    if radius <= 0:
        return alpha

    guide = cv2.cvtColor(image, cv2.COLOR_BGR2GRAY).astype(np.float32) / 255.0
    return np.clip(guided_filter(guide, alpha, radius, eps), 0.0, 1.0)


def subject_alpha(image, session, refine_radius=4):
    """Return the subject coverage of a BGR image as float32 HxW in [0, 1].

    Uses rembg's mask-only path, which skips foreground colour estimation.
    Callers that only need to know *where* the subject is (outline.py) should
    prefer this over subject_cutout. The saving is small at headshot sizes
    (both are dominated by the ONNX forward pass) but foreground estimation
    scales with pixel count, so it grows on full-resolution photos.
    """
    # The models are trained on RGB. Handing them OpenCV's native BGR order
    # silently degrades the mask rather than failing, so convert explicitly.
    rgb = cv2.cvtColor(image, cv2.COLOR_BGR2RGB)

    mask = remove(rgb, session=session, only_mask=True)
    alpha = mask.astype(np.float32) / 255.0

    return refine_alpha(image, alpha, refine_radius)


def subject_cutout(image, session, matting=False, refine_radius=4):
    """Return (alpha, foreground_bgr) for a BGR image.

    A pixel on a soft edge was captured as a blend of the subject and whatever
    was behind them, so reusing the original colours there drags the old
    background into the composite as a halo. Both paths below unmix that blend
    to recover the subject's true colour, which is what makes the alpha safe
    to composite over a different background.

    matting=True runs closed-form alpha matting, which recovers more soft
    detail than the model mask alone. It solves a linear system over the
    boundary band, so its cost scales with the image size -- negligible on a
    small headshot, seconds on a full-resolution photo. Note that rembg
    catches ValueError from this path and quietly falls back to the plain
    cutout, so a mask with no ambiguous band produces no visible change rather
    than an error.
    """
    rgb = cv2.cvtColor(image, cv2.COLOR_BGR2RGB)

    rgba = remove(
        rgb,
        session=session,
        alpha_matting=matting,
        decontaminate=True,
    )

    alpha = rgba[:, :, 3].astype(np.float32) / 255.0
    foreground = cv2.cvtColor(rgba[:, :, :3], cv2.COLOR_RGB2BGR)

    return refine_alpha(image, alpha, refine_radius), foreground


def composite(foreground, alpha, color):
    """Blend a BGR foreground over a flat BGR `color` using `alpha` coverage."""
    weights = alpha[:, :, np.newaxis]
    background = np.full(foreground.shape, color, dtype=np.float32)
    blended = foreground.astype(np.float32) * weights + background * (1.0 - weights)

    return np.clip(blended, 0, 255).astype(np.uint8)


def binary_mask(alpha, threshold=0.5, erode=2):
    """Threshold an alpha matte into a solid mask, optionally eroded.

    Eroding pulls the mask inside the true boundary. That matters for edge
    detection: the strongest gradient in the whole photo is usually the
    subject-vs-background boundary itself, and it should be drawn from the
    silhouette rather than picked up twice as a noisy parallel line.
    """
    mask = (alpha >= threshold).astype(np.uint8) * 255

    if erode > 0:
        kernel = np.ones((3, 3), np.uint8)
        mask = cv2.erode(mask, kernel, iterations=erode)

    return mask
