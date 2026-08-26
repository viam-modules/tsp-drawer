import * as VIAM from "@viamrobotics/sdk";

const statusEl = document.getElementById("status") as HTMLSpanElement;
const cameraEl = document.getElementById("camera") as HTMLVideoElement;
const sensorEl = document.getElementById("sensor") as HTMLPreElement;
const startBtn = document.getElementById("start") as HTMLButtonElement;
const stopBtn = document.getElementById("stop") as HTMLButtonElement;
const imageUploadEl = document.getElementById("imageUpload") as HTMLInputElement;
const uploadedImageEl = document.getElementById("uploadedImage") as HTMLImageElement;

let machine: VIAM.RobotClient;

async function startCamera() {
  const streamClient = new VIAM.StreamClient(machine);
  const mediaStream = await streamClient.getStream("cam-1");
  cameraEl.srcObject = mediaStream;
}

async function getImageData(file: File): Promise<ImageData> {
  // TODO: change this so instead it runs the scripts to get
  // points and the tour order from the image
  // need to figure out how to call a python script from ts
  // then, add the ability to send files to robot and/or run do-command w 
  // string input

  // maybe the next step should just be the do-command, which for now
  // will just run the default pikachu

  const bitmap = await createImageBitmap(file);
  const canvas = document.createElement("canvas");
  canvas.width = bitmap.width;
  canvas.height = bitmap.height;
  const ctx = canvas.getContext("2d")!;
  ctx.drawImage(bitmap, 0, 0);
  console.log(ctx.getImageData(0, 0, bitmap.width, bitmap.height));
  return ctx.getImageData(0, 0, bitmap.width, bitmap.height);
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
    const data = await getImageData(file);
    console.log(`Image is ${data.width}x${data.height}px`);

    // Release the memory once the browser has rendered it
    uploadedImageEl.onload = () => URL.revokeObjectURL(objectUrl);
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

  statusEl.textContent = "Connected";
  statusEl.className = "connected";

  await startCamera();
}

setupImageUpload();

main().catch((err) => {
  statusEl.textContent = `Connection failed: ${err.message ?? err}`;
  statusEl.className = "disconnected";
});