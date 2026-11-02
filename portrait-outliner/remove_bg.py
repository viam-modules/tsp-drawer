#!/usr/bin/env python3
"""Remove the background from a portrait photo, leaving the subject on white."""

import argparse
import os
import sys

import cv2
import numpy as np

import segment

NAMED_COLORS = {
    "white": (255, 255, 255),
    "black": (0, 0, 0),
    "grey": (128, 128, 128),
    "gray": (128, 128, 128),
}


def parse_color(value):
    """Parse a background spec into a BGR tuple, or None for transparent."""
    text = value.strip().lower()

    if text == "transparent":
        return None

    if text in NAMED_COLORS:
        red, green, blue = NAMED_COLORS[text]
        return (blue, green, red)

    if text.startswith("#"):
        digits = text[1:]
        if len(digits) != 6:
            raise argparse.ArgumentTypeError(f"Expected #RRGGBB, got '{value}'")
        try:
            red, green, blue = (int(digits[i:i + 2], 16) for i in (0, 2, 4))
        except ValueError:
            raise argparse.ArgumentTypeError(f"Invalid hex colour: '{value}'")
        return (blue, green, red)

    parts = text.split(",")
    if len(parts) == 3:
        try:
            red, green, blue = (int(part) for part in parts)
        except ValueError:
            raise argparse.ArgumentTypeError(f"Invalid R,G,B colour: '{value}'")
        if not all(0 <= channel <= 255 for channel in (red, green, blue)):
            raise argparse.ArgumentTypeError(f"Colour channels must be 0-255: '{value}'")
        return (blue, green, red)

    raise argparse.ArgumentTypeError(
        f"Unrecognised background '{value}'; use white, transparent, #RRGGBB or R,G,B"
    )


def parse_args():
    parser = argparse.ArgumentParser(
        description="Segment the person out of a portrait and place them on a "
        "flat background (white by default) or a transparent one."
    )
    parser.add_argument("input", help="Path to the input JPG/PNG portrait")
    parser.add_argument("output", help="Path to write the result (PNG recommended)")
    parser.add_argument(
        "--bg",
        type=parse_color,
        default=NAMED_COLORS["white"],
        help="Background: white, black, grey, transparent, #RRGGBB, or R,G,B "
        "(default: white)",
    )
    parser.add_argument(
        "--model",
        default=segment.DEFAULT_MODEL,
        help="rembg model to segment with; birefnet-portrait is slower but "
        f"keeps more hair detail (default: {segment.DEFAULT_MODEL})",
    )
    parser.add_argument(
        "--matting",
        action="store_true",
        help="Run closed-form alpha matting for finer soft edges. Cost scales "
        "with image size; try it when hair looks cut out.",
    )
    parser.add_argument(
        "--refine",
        type=int,
        default=4,
        help="Guided-filter radius used to re-fit the model mask to the "
        "photo's own edges; 0 disables refinement (default: 4)",
    )
    parser.add_argument(
        "--save-mask",
        metavar="PATH",
        help="Also write the alpha matte as a greyscale image, which is useful "
        "for eyeballing where segmentation went wrong",
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


def write_image(path, image):
    output_dir = os.path.dirname(path)
    if output_dir and not os.path.isdir(output_dir):
        raise FileNotFoundError(f"Output directory does not exist: {output_dir}")

    if not cv2.imwrite(path, image):
        raise IOError(f"Failed to write image to '{path}'")


def remove_background(input_path, output_path, background, model, matting,
                      refine_radius, mask_path):
    image = load_image(input_path)

    session = segment.load_session(model)
    alpha, foreground = segment.subject_cutout(
        image, session, matting=matting, refine_radius=refine_radius
    )

    if background is None:
        # Non-premultiplied BGRA: the foreground colour has already had the old
        # background unmixed out of it, so viewers compositing it themselves
        # get the same clean edges as the flat-background path below.
        result = np.dstack([foreground, (alpha * 255).astype(np.uint8)])
        if not output_path.lower().endswith(".png"):
            print(
                f"Warning: '{output_path}' is not a PNG; transparency may be lost.",
                file=sys.stderr,
            )
    else:
        result = segment.composite(foreground, alpha, background)

    write_image(output_path, result)

    if mask_path:
        write_image(mask_path, (alpha * 255).astype(np.uint8))


def main():
    args = parse_args()

    try:
        remove_background(
            args.input,
            args.output,
            background=args.bg,
            model=args.model,
            matting=args.matting,
            refine_radius=args.refine,
            mask_path=args.save_mask,
        )
    except (FileNotFoundError, ValueError, IOError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(1)

    print(f"Saved background-removed portrait to {args.output}")
    if args.save_mask:
        print(f"Saved alpha matte to {args.save_mask}")


if __name__ == "__main__":
    main()
