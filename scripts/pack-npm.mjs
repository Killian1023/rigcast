import { execFileSync } from "node:child_process";
import {
  chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync,
  readdirSync, rmSync, writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";

const go = process.env.GO || "go";
const dist = resolve(process.argv[2] || "dist");
const readJSON = (path) => JSON.parse(readFileSync(path, "utf8"));
const pkg = readJSON("npm/package.json");
const server = readJSON("server.json");
const entry = server.packages.find(({ registryType }) => registryType === "npm");

if (server.name !== pkg.mcpName || entry?.identifier !== pkg.name ||
    server.version !== pkg.version || entry.version !== pkg.version ||
    entry.transport?.type !== "stdio") {
  throw new Error("npm/package.json and server.json disagree on the release metadata");
}

function buildInfo(binary) {
  return execFileSync(go, ["version", "-m", binary], { encoding: "utf8" });
}

function licenseNotices(info) {
  const modules = [...new Set(info.flatMap((text) =>
    [...text.matchAll(/^\tdep\t([^\t]+)\t/gm)].map((match) => match[1])))].sort();
  const directories = new Map(execFileSync(go, ["list", "-m", "-f", '{{printf "%s\\t%s" .Path .Dir}}', ...modules],
    { encoding: "utf8" }).trimEnd().split(/\r?\n/).map((line) => line.split("\t")));
  const goRoot = execFileSync(go, ["env", "GOROOT"], { encoding: "utf8" }).trim();
  const licenseFile = /^(licen[cs]e(?:[-._].*)?|copying(?:[-._].*)?|notice(?:[-._].*)?|copyright|patents)$/i;
  const entries = [];
  const add = (label, dir, files) => {
    if (!files.length) {
      throw new Error(`No license or notice found for ${label}`);
    }
    for (const file of files.sort()) {
      entries.push(`--- ${label}: ${file} ---\n${readFileSync(join(dir, file), "utf8").trimEnd()}`);
    }
  };
  add("Go standard library", goRoot, ["LICENSE", "PATENTS"]);
  const vendorDir = join(goRoot, "src", "vendor");
  add("Go toolchain vendored dependencies", vendorDir,
    readdirSync(vendorDir, { recursive: true, withFileTypes: true })
      .filter((item) => item.isFile() && licenseFile.test(item.name))
      .map((item) => join(item.parentPath.slice(vendorDir.length + 1), item.name)));
  for (const name of modules) {
    const dir = directories.get(name);
    if (!dir) {
      throw new Error(`No module directory found for ${name}`);
    }
    add(name, dir, readdirSync(dir, { withFileTypes: true })
      .filter((item) => item.isFile() && licenseFile.test(item.name)).map(({ name }) => name));
  }
  return `${entries.join("\n\n")}\n`;
}

const stage = mkdtempSync(join(tmpdir(), "rigcast-npm-"));
try {
  const binDir = join(stage, "bin");
  mkdirSync(binDir);
  for (const file of ["README.md", "launch.mjs"]) {
    copyFileSync(join("npm", file), join(stage, file));
  }
  // Only the staged package with binaries is publishable.
  const { private: _private, ...publicPackage } = pkg;
  writeFileSync(join(stage, "package.json"), `${JSON.stringify(publicPackage, null, 2)}\n`);
  chmodSync(join(stage, "launch.mjs"), 0o755);
  if (existsSync("LICENSE")) {
    copyFileSync("LICENSE", join(stage, "LICENSE"));
  }

  const info = [];
  for (const platform of ["linux", "darwin", "windows"]) {
    for (const arch of ["amd64", "arm64"]) {
      const file = `rigcast-${platform}-${arch}${platform === "windows" ? ".exe" : ""}`;
      const binary = join(dist, "npm-binaries", file);
      const details = buildInfo(binary);
      const settings = new Set(details.split(/\r?\n/).filter((line) => line.startsWith("\tbuild\t"))
        .map((line) => line.slice(7)));
      if (!settings.has(`GOOS=${platform}`) || !settings.has(`GOARCH=${arch}`) ||
          !settings.has("CGO_ENABLED=0")) {
        throw new Error(`Unexpected build settings: ${basename(binary)}`);
      }
      info.push(details);
      const target = join(binDir, file);
      copyFileSync(binary, target);
      chmodSync(target, 0o755);
    }
  }
  writeFileSync(join(stage, "THIRD_PARTY_NOTICES.txt"), licenseNotices(info));
  execFileSync("npm", ["pack", "--ignore-scripts", "--pack-destination", dist, stage], { stdio: "inherit" });
} finally {
  rmSync(stage, { recursive: true });
}
