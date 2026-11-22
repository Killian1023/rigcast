#!/usr/bin/env node
import { spawn } from "node:child_process";
import { constants } from "node:os";
import { fileURLToPath } from "node:url";

const platform = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];

if (!platform || !arch) {
  console.error(`rigcast-mcp: unsupported platform ${process.platform}/${process.arch}`);
  process.exit(1);
}

const suffix = platform === "windows" ? ".exe" : "";
const binary = fileURLToPath(new URL(`./bin/rigcast-${platform}-${arch}${suffix}`, import.meta.url));
const child = spawn(binary, ["mcp"], { stdio: "inherit", windowsHide: true });

child.on("error", (err) => {
  console.error(`rigcast-mcp: could not launch Rigcast: ${err.message}`);
  process.exitCode = 1;
});
child.on("exit", (code, signal) => {
  process.exitCode = code ?? (128 + (constants.signals[signal] ?? 0));
});
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    if (child.exitCode === null && child.signalCode === null && child.pid !== undefined) {
      child.kill(signal);
    }
  });
}
