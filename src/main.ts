import * as VIAM from "@viamrobotics/sdk";
import { photoToPoints, type Points } from "./photo_to_points";

const statusEl = document.getElementById("status") as HTMLSpanElement;
const cameraEl = document.getElementById("camera") as HTMLVideoElement;
const drawStatusEl = document.getElementById("drawstatus") as HTMLPreElement;
const startDefaultBtn = document.getElementById("startdefault") as HTMLButtonElement;
const startCustomBtn = document.getElementById("startcustom") as HTMLButtonElement;
const stopBtn = document.getElementById("stop") as HTMLButtonElement;
const imageUploadEl = document.getElementById("imageUpload") as HTMLInputElement;
const uploadedImageEl = document.getElementById("uploadedImage") as HTMLImageElement;
const pointsPreviewEl = document.getElementById("pointsPreview") as HTMLCanvasElement;

let machine: VIAM.RobotClient;
let plotter: VIAM.GenericServiceClient;
const targetRes = 40;
const showLines = false;
// Ordered [x, y] pairs, the shape the module's "draw" command expects.
let points_for_drawing: number[][] = [];

async function startCamera() {
  const streamClient = new VIAM.StreamClient(machine);
  const mediaStream = await streamClient.getStream("cam-1");
  cameraEl.srcObject = mediaStream;
}

// TODO: derive the tour order from these points, then send them to the plotter
// (send files to the robot, or a do-command that takes the points directly).
// TODO: also add input field for resolution/number of points
async function getImageData(file: File): Promise<Points> {
  return photoToPoints(file, { contrastEnhance: 3, targetResolution: [targetRes, targetRes] });
}

/** Stands in for the script's plt.show() debug preview. */
function drawPointsPreview({ x, y }: Points, showLines = false, resolution = targetRes) {
  const size = pointsPreviewEl.width;
  const ctx = pointsPreviewEl.getContext("2d");
  if (!ctx) return;

  ctx.clearRect(0, 0, size, size);
  ctx.fillStyle = "#111";

  const scale = size / resolution;
  const radius = Math.max(0.5, scale / 3);

  ctx.beginPath();
  for (let i = 0; i < x.length; i++) {
    if (!showLines || i==0) {
      ctx.beginPath();
      // Canvas y grows downward, so flip back for display.
      ctx.arc(x[i] * scale, size - y[i] * scale, radius, 0, Math.PI * 2);
      ctx.fill();
    }
    else {
      ctx.lineTo(x[i] * scale, size - y[i] * scale,)
      ctx.fill();
    }
  }

  pointsPreviewEl.hidden = false;
}

function setupImageUpload() {
  imageUploadEl.addEventListener("change", async () => {
    const file = imageUploadEl.files?.[0];
    if (!file) return;

    // In-memory URL — lives only for this page session, never uploaded anywhere
    const objectUrl = URL.createObjectURL(file);
    uploadedImageEl.src = objectUrl;
    uploadedImageEl.hidden = false;

    // Do something with the visual info
    const points = await getImageData(file);
    console.log(`Extracted ${points.x.length} points`);
    drawPointsPreview(points, showLines);

    points_for_drawing = points.x.map((x, i) => [x, points.y[i]]);
    console.log(points_for_drawing);

    // Release the memory once the browser has rendered it
    uploadedImageEl.onload = () => URL.revokeObjectURL(objectUrl);
  });
}

async function drawDefault(path: string) {
  startDefaultBtn.addEventListener("click", async () => {
    try {
    const res = await plotter.doCommand({ command: "draw", path: path });
    console.log(res); // { started: true, points: 1234 }
    }
    catch (error: unknown) {
      console.log(error instanceof Error ? error.message : String(error));
    }
  });
}

async function drawCustom() {
  startCustomBtn.addEventListener("click", async () => {
    try {
    const res = await plotter.doCommand({ command: "draw", point_array: points_for_drawing});
    console.log(res); // { started: true, points: 1234 }
    }
    catch (error: unknown) {
      console.log(error instanceof Error ? error.message : String(error));
    }
  });
}

async function status() {
  try {
    return await plotter.doCommand({ command: "status" });
    // { running, drawn, total, error? }
  }
  catch (error: unknown) {
    console.log(error instanceof Error ? error.message : String(error));
  }
}

async function stop() {
  stopBtn.addEventListener("click", async () => {
    try {
      await plotter.doCommand({ command: "stop" });
    }
    catch (error: unknown) {
      console.log(error instanceof Error ? error.message : String(error));
    }
  });
}

async function main() {
  machine = await VIAM.createRobotClient({
    host: import.meta.env.VITE_HOST,
    credentials: {
      type: "api-key",
      authEntity: import.meta.env.VITE_API_KEY_ID_PORTRAIT,
      payload: import.meta.env.VITE_API_KEY_PORTRAIT,
    },
    signalingAddress: "https://app.viam.com:443",
  });

  plotter = new VIAM.GenericServiceClient(machine, "plotter");
  statusEl.textContent = "Connected";
  statusEl.className = "connected";

  console.log("main")

  await startCamera();
}

setupImageUpload();
drawDefault("/root/pika/pikachu.tsp");
drawCustom();
stop();

main().catch((err) => {
  statusEl.textContent = `Connection failed: ${err.message ?? err}`;
  statusEl.className = "disconnected";
});