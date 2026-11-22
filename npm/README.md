# Rigcast MCP

An MCP stdio launcher for a private Rigcast GPU daemon. The package includes
native binaries for macOS, Linux, and Windows on x64 and arm64. It doesn't
install a daemon or include server credentials.

On the workstation, install Node.js 22 or later and write
`~/.config/rigcast/config.yaml` with your daemon's URL and shared token:

```yaml
host: "http://gpu-host.example:7474"
token: "YOUR_RIGCAST_TOKEN"
```

Alternatively set `RIGCAST_HOST` and `RIGCAST_TOKEN`. Keep the token out of
checked-in configuration. Only use a trusted private network such as Tailscale,
or a trusted HTTPS endpoint.

For Codex, register the launcher and check it with `/mcp`:

```sh
codex mcp add rigcast -- npx -y rigcast-mcp@0.1.0
```

The launcher runs in your workstation's context so `submit_job` can package
its workspace. For daemon setup and the tool list, see the
[Rigcast documentation](https://github.com/Killian1023/rigcast).
