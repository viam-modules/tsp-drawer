#!/usr/bin/env python3
"""Locate the face and score how much each part of the image matters.

The portrait has to be recognizable, which makes the eyes, eyebrows, nose and
mouth the content worth spending the stroke budget on -- hair and clothing are
the first thing to sacrifice. That priority is expressed here as a weight map
over the image, which the budget then uses to rank and filter strokes.
"""

import os

import cv2
import numpy as np

MODEL_FILENAME = "face_detection_yunet_2023mar.onnx"
DEFAULT_MODEL_PATH = os.path.join(os.path.dirname(__file__), "models", MODEL_FILENAME)
MODEL_URL = (
    "https://github.com/opencv/opencv_zoo/raw/main/models/"
    "face_detection_yunet/" + MODEL_FILENAME
)

# Weight assigned to plain background/hair/clothing. Everything else is a
# multiple of this, so a weight of 1.0 means "no special treatment".
BASE_WEIGHT = 1.0


def detect_face(image, model_path=DEFAULT_MODEL_PATH, confidence=0.6):
    """Return the largest detected face as a dict of landmark points, or None.

    YuNet reports five landmarks -- both eyes, the nose tip and both mouth
    corners -- which is all that is needed to place the features. It is used in
    preference to deriving them from the subject mask's proportions because
    that approach fails outright on common portraits: wide hair at the crown or
    hair merging into the shoulders both break the width profile it relies on,
    and it then reports the face as being somewhere in the hair.
    """
    if not os.path.isfile(model_path):
        return None

    height, width = image.shape[:2]
    detector = cv2.FaceDetectorYN_create(model_path, "", (width, height),
                                         confidence, 0.3, 5000)
    count, faces = detector.detect(image)
    if faces is None or len(faces) == 0:
        return None

    # Largest by box area: portraits put the subject in front, and a bystander
    # in the background should not steer the drawing.
    face = max(faces, key=lambda f: f[2] * f[3])
    landmarks = face[4:14].reshape(5, 2).astype(np.float32)

    return {
        "box": face[:4].astype(np.float32),
        "right_eye": landmarks[0],
        "left_eye": landmarks[1],
        "nose": landmarks[2],
        "mouth_right": landmarks[3],
        "mouth_left": landmarks[4],
        "confidence": float(face[14]),
    }


def priority_map(shape, face, core_weight=4.0, feature_weight=9.0):
    """Build a per-pixel importance map from the five face landmarks.

    Distances are all expressed in eye-spans -- the gap between the two eyes --
    so the map scales with the subject's size in frame and needs no tuning per
    photo. Everything is drawn rotated to the eye line, so a tilted head is
    handled without a separate case.
    """
    height, width = shape[:2]
    weights = np.full((height, width), BASE_WEIGHT, dtype=np.float32)

    right_eye, left_eye = face["right_eye"], face["left_eye"]
    eye_mid = (right_eye + left_eye) / 2.0
    eye_span = float(np.linalg.norm(left_eye - right_eye))
    if eye_span < 1.0:
        return weights

    # Angle of the eye line, so the regions below follow a tilted head.
    delta = left_eye - right_eye
    angle = float(np.degrees(np.arctan2(delta[1], delta[0])))

    mouth_mid = (face["mouth_right"] + face["mouth_left"]) / 2.0

    def ellipse(center, half_width, half_height, value):
        cv2.ellipse(
            weights,
            (int(round(center[0])), int(round(center[1]))),
            (int(round(half_width)), int(round(half_height))),
            angle, 0, 360, float(value), -1,
        )

    # Face core: forehead down to chin. The top reaches well above the eye line
    # so forehead and hairline creases are included -- they were previously cut
    # off, and they matter to the likeness.
    core_top = eye_mid[1] - 1.35 * eye_span
    core_bottom = mouth_mid[1] + 1.25 * eye_span
    core_center = ((eye_mid[0] + mouth_mid[0]) / 2.0, (core_top + core_bottom) / 2.0)
    ellipse(core_center, 1.40 * eye_span, (core_bottom - core_top) / 2.0, core_weight)

    # Individual features, weighted above the core so that when the budget is
    # tight the eyes and mouth outrank a cheek or jaw line.
    for eye in (right_eye, left_eye):
        ellipse(eye, 0.55 * eye_span, 0.40 * eye_span, feature_weight)
        # Eyebrow, just above the eye. Short strokes, high value.
        brow = eye - np.array([0.0, 0.45 * eye_span], dtype=np.float32)
        ellipse(brow, 0.60 * eye_span, 0.30 * eye_span, feature_weight)

    ellipse(face["nose"], 0.50 * eye_span, 0.55 * eye_span, feature_weight)
    ellipse(mouth_mid, 0.85 * eye_span, 0.40 * eye_span, feature_weight)

    # Soften the region edges so a stroke crossing a boundary is scored by how
    # much of it lies inside rather than by a hard in/out step.
    blur = max(3, int(round(0.15 * eye_span)) | 1)
    return cv2.GaussianBlur(weights, (blur, blur), 0)


def fallback_priority_map(shape, mask, core_weight=4.0):
    """Approximate the face from the subject mask when detection is unavailable.

    Used only when the model file is missing or no face is found. This is the
    proportional row-width estimate: it assumes an upright, centred subject
    whose head is narrower than their shoulders, and it is known to misplace
    the band on subjects with wide hair at the crown. Better than nothing, but
    the detector is what should normally run.
    """
    height, width = shape[:2]
    weights = np.full((height, width), BASE_WEIGHT, dtype=np.float32)

    widths = (mask > 0).sum(axis=1).astype(np.float32)
    rows = np.nonzero(widths)[0]
    if len(rows) == 0:
        return weights

    profile = cv2.GaussianBlur(widths.reshape(-1, 1), (1, 15), 0).ravel()
    head_top = int(rows[0])
    limit = head_top + max(1, (len(profile) - head_top) // 2)
    widest = head_top + int(np.argmax(profile[head_top:limit]))

    neck = widest
    for row in range(widest + 1, len(profile)):
        if profile[row] > profile[neck]:
            break
        neck = row

    head_height = neck - head_top
    columns = np.nonzero(mask[widest])[0]
    if head_height <= 0 or len(columns) == 0:
        return weights

    left, right = int(columns[0]), int(columns[-1])
    span = right - left
    weights[
        int(head_top + 0.20 * head_height): int(head_top + 0.95 * head_height),
        int(left + 0.10 * span): int(right - 0.10 * span),
    ] = core_weight

    return weights


def render_priority_map(image, weights):
    """Tint an image by its weight map, for checking where the face was found."""
    normalized = weights / max(weights.max(), 1e-6)
    heat = cv2.applyColorMap((normalized * 255).astype(np.uint8), cv2.COLORMAP_JET)
    return cv2.addWeighted(image, 0.55, heat, 0.45, 0)
