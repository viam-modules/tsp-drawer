export interface Points {
  x: number[];
  y: number[];
}

export interface PhotoToPointsOptions {
  /** Grid the image is downsampled to before weighting pixels. */
  targetResolution?: [number, number];
  /** >1 pushes values away from mid-grey, matching PIL's ImageEnhance.Contrast. */
  contrastEnhance?: number;
}

/**
 * Converts a photo to a pointilism representation: darker pixels emit more
 * points, each jittered within its cell. Port of photo_to_points.py.
 */
export async function photoToPoints(
  file: Blob,
  { targetResolution = [100, 100], contrastEnhance = 3 }: PhotoToPointsOptions = {},
): Promise<Points> {
  const [width, height] = targetResolution;
  const grey = await toGreyscaleGrid(file, width, height, contrastEnhance);

  // Same divisor as the Python: average of the two dimensions.
  const scale = ((width + height) / 2) | 0;

  const x: number[] = [];
  const y: number[] = [];

  for (let px = 0; px < width; px++) {
    for (let py = 0; py < height; py++) {
      const value = grey[py * width + px];
      const weight = Math.floor((2 * (255 - value)) / scale);

      for (let i = 0; i < weight; i++) {
        x.push(px + Math.random());
        // Flip so the points read the same way up as the source image.
        y.push(height - (py + Math.random()));
      }
    }
  }

  return { x, y };
}

async function toGreyscaleGrid(
  file: Blob,
  width: number,
  height: number,
  contrastEnhance: number,
): Promise<Uint8Array> {
  const bitmap = await createImageBitmap(file);
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;

  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  if (!ctx) throw new Error("Could not get a 2D canvas context");

  try {
    ctx.drawImage(bitmap, 0, 0, width, height);
  } finally {
    bitmap.close();
  }

  const { data } = ctx.getImageData(0, 0, width, height);
  const grey = new Uint8Array(width * height);

  for (let i = 0; i < grey.length; i++) {
    const r = data[i * 4];
    const g = data[i * 4 + 1];
    const b = data[i * 4 + 2];

    // ITU-R 601-2 luma, the transform PIL's convert("L") uses.
    const luma = 0.299 * r + 0.587 * g + 0.114 * b;
    grey[i] = luma;
  }

  return applyContrast(grey, contrastEnhance);
}

function applyContrast(grey: Uint8Array, factor: number): Uint8Array {
  if (factor === 1) return grey;

  let sum = 0;
  for (const value of grey) sum += value;
  const mean = sum / grey.length;

  const out = new Uint8Array(grey.length);
  for (let i = 0; i < grey.length; i++) {
    out[i] = clampByte(mean + (grey[i] - mean) * factor);
  }
  return out;
}

function clampByte(value: number): number {
  return Math.max(0, Math.min(255, Math.round(value)));
}
