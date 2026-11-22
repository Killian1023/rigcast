# rigcast

Run jobs from a local project on a private Linux GPU host. A single Go binary
provides the client CLI, a Docker-backed daemon, and an MCP server for coding
agents. Jobs run asynchronously on the daemon; only workspace code goes with
each submission. Datasets sync separately.

## Build

Install Go 1.25 or later, then run `make build`. The binary is in `dist/`. Run
`make release VERSION=0.1.0` to build CGO-free binaries for Linux, macOS, and
Windows (amd64 and arm64). You can also build directly with
`CGO_ENABLED=0 go build ./cmd/rigcast` in a POSIX shell.

## Set up the GPU host

1. Install Docker Engine and the NVIDIA Container Toolkit on the Linux host.
   Confirm `docker info` and `nvidia-smi` work. For a CPU-only job, set
   `gpus: "none"`; GPU status still requires `nvidia-smi`.
2. Install Tailscale on the host and client, join the same tailnet, and run
   `tailscale ip -4` on the host. Restrict TCP port 7474 with your tailnet
   grants or ACLs to the users/devices that should have job access.
3. Generate a long random bearer token (for example, `openssl rand -hex 32`).
   Set `RIGCAST_TOKEN` on the host and start:

   ```sh
   rigcast daemon --bind "$(tailscale ip -4):7474" --data-dir "$HOME/.local/share/rigcast"
   ```

   The daemon account needs access to the Docker socket. The daemon also
   accepts `RIGCAST_BIND` and `RIGCAST_DATA_DIR`. For a persistent service,
   install the binary at `/usr/local/bin/rigcast`, create a `rigcast` system
   account/group, and install `deploy/rigcast.service` as
   `/etc/systemd/system/rigcast.service`. Create `/etc/rigcast/daemon.env`
   owned by root with mode `0600`:

   ```dotenv
   RIGCAST_BIND=100.64.10.20:7474
   RIGCAST_TOKEN=YOUR_GENERATED_TOKEN
   ```

   Replace the example IP with the host's Tailscale IP, then run
   `sudo systemctl daemon-reload` and `sudo systemctl enable --now rigcast`.
4. On the client, create `~/.config/rigcast/config.yaml` (or
   `$XDG_CONFIG_HOME/rigcast/config.yaml`) with owner-only access:

   ```yaml
   host: "http://gpu-pc:7474"
   token: "YOUR_GENERATED_TOKEN"
   ```

   Use the host's MagicDNS name or Tailscale IP. `RIGCAST_HOST` and
   `RIGCAST_TOKEN` override the respective fields. Run `rigcast status`.

**Trust boundary:** The daemon accepts shell commands and Docker images from
anyone with its bearer token. Run it on a trusted private host with trusted
users and images; it is not a multi-tenant sandbox. Docker socket access is
effectively host-root access. The daemon serves plain HTTP by default; do not
expose it to the public internet. Use Tailscale or a trusted HTTPS reverse
proxy. Do not put credentials in project YAML, commands, or logs.

## Run a job

```sh
rigcast init
rigcast data push /path/to/market-ticks market-ticks
rigcast data ls
rigcast run --dataset market-ticks -- python train.py --epochs 10
job_id=$(rigcast run --detach --dataset market-ticks -- python train.py --epochs 50)
rigcast ps --status running
rigcast logs -f --tail 30 --sanitize "$job_id"
rigcast pull "$job_id"
```

`run` follows logs by default and propagates the container's exit code.
Ctrl-C stops the local log follower; `rigcast kill <job_id>` cancels the
remote job. Pass separate arguments after `--` for literal argument
boundaries. For shell operators, pass a single shell string, e.g.
`rigcast run -- 'python train.py && cp result.json /outputs/'`.

Submitted code is mounted at `/workspace`. Write artifacts to `/outputs`;
files left only in `/workspace` are not included in `pull`. Artifacts
download to `./outputs/<job_id>` by default; pass a second argument to
choose another directory. Existing files are not overwritten.

### Project configuration

```yaml
image: "pytorch/pytorch:2.4.0-cuda12.4-cudnn9-runtime"
gpus: "all"
datasets: ["market-ticks"]
env:
  PYTHONUNBUFFERED: "1"
workdir: "/workspace"
timeout: "4h"
memory_limit: "32g"
```

- `image` is required unless supplied with `--image`. The daemon pulls an
  image if it isn't available locally. The image must have `/bin/sh`.
- `gpus` accepts `all` (default), `none`, or comma-separated device indices
  like `0,1`. Pass `--gpus` to override. Rigcast does not reserve GPUs
  or schedule jobs by VRAM; avoid overcommitting devices.
- `datasets` are mounted read-only under `/data/<name>`. `--dataset`
  replaces the YAML list; repeat it to mount multiple datasets.
- `env` sets container environment variables. `workdir` defaults to
  `/workspace` and may be a subdirectory.
- `timeout` is an optional Go duration, starting before the image pull.
  `memory_limit` is an optional Docker-style memory size.

Workspace archives exclude `.git`, `.venv`, `venv`, `__pycache__`,
`node_modules`, `.pytest_cache`, and `.mypy_cache` directories. Root
`.gitignore` and `.rigcastignore` patterns are also applied, in that order.
Use `.rigcastignore` for secrets, local outputs, and large files. Symlinks
and special files are not transferred. Workspace uploads are limited to
2 GiB, both as an HTTP request and after decompression.

### Datasets

`data push <local_dir> <name>` compares SHA-256 hashes and uploads only new
or modified regular files. **Files absent locally are deleted remotely.**
This is a mirror, not an append operation. Serialise pushes to the same
dataset and avoid changing the local tree during a push. Even though jobs
cannot modify datasets, an active job can see changes from a concurrent
push; use a new dataset name when a run needs a stable snapshot.

## MCP

`rigcast mcp` uses stdio with the same client config as the CLI. It exposes
`gpu_status`, `list_datasets`, `submit_job`, `job_status`, `tail_logs`,
`cancel_job`, `list_artifacts`, and `read_artifact`. `submit_job` waits for
workspace upload and queuing, not for the job. Pass `workdir` if the
MCP process wasn't started in the project directory. `tail_logs` defaults
to 50 sanitized lines, with a maximum of 500 lines and 128 KiB of text.
`read_artifact` accepts UTF-8 text up to 100 KiB. Dataset pushes remain a
human CLI operation.

With Claude Code, register the executable using its absolute path:

```sh
claude mcp add --transport stdio rigcast -- /absolute/path/to/rigcast mcp
```

For Cursor, add this to `.cursor/mcp.json` (or `~/.cursor/mcp.json` for
global configuration) and use the appropriate executable path:

```json
{
  "mcpServers": {
    "rigcast": {
      "command": "/absolute/path/to/rigcast",
      "args": ["mcp"]
    }
  }
}
```

Prefer the user-level client config over putting tokens in MCP project
files. Only grant an agent access to project directories you intend to
upload and review commands before letting it start remote jobs.

## Verification

`make test` runs the SQLite, archive, HTTP, CLI, and MCP tests without
Docker. To test actual container execution, cleanup, and read-only dataset
mounts, run the following on a Linux host whose Docker daemon sees the
same filesystem and can pull `alpine:3.22`:

```sh
RIGCAST_TEST_DOCKER=1 go test -count=1 -v ./internal/daemon ./internal/cli -run Docker
```

These tests deliberately use `gpus: "none"` so they also run on a
CPU-only Docker host. On the GPU host, use NVIDIA Container Toolkit and
run `rigcast run --gpus all --image nvidia/cuda:12.4.1-base-ubuntu22.04 -- nvidia-smi`
from a small workspace to verify GPU passthrough. The daemon retains
workspaces, artifacts, and logs under its data directory; plan retention
and disk capacity before running large workloads. Use a local filesystem
for this directory and run only one daemon per data directory. Running
containers are left in place at daemon shutdown and reattached on startup;
jobs whose containers are no longer running are marked failed on recovery.
