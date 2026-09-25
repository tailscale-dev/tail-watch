# what

what is a service for recording and sharing what you've been working on. It
implements the flows of
[Google's Snippets](http://blog.idonethis.com/google-snippets-internal-tool/) so
that you can record what you worked on after the fact as opposed to recording
what you'd like to work on before the fact.

## Usage

1. Open <http://what>
2. Type words in the box
3. Hit post

## Configuration

Configuration is done primarily with enviroment variables. Here are the relevant
variables for what:

| Variable         | Description                                                                                                            | Default                                   |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------- | ----------------------------------------- |
| `TS_AUTHKEY`     | The authkey to connect your node to Tailscale. This should only be needed once when setting up the service.            |                                           |
| `TSNET_HOSTNAME` | The hostname to use on your tailnet.                                                                                   | `what`                                    |
| `TSNET_VERBOSE`  | Whether to print verbose logs from tsnet. This can be useful to help diagnose connection issues with your what server. | `false`                                   |
| `DATA_DIR`       | The directory to store data in.                                                                                        | `$HOME/.config/tailscale/$TSNET_HOSTNAME` |
| `SLOG_LEVEL`     | The level of logs to print.                                                                                            | `INFO`                                    |

## Development

Tool versions (Go, Bun) are pinned in `mise.toml`. With
[mise](https://mise.jdx.dev) installed, run:

```sh
mise install
mise run css   # install Tailwind and rebuild static/css/base.css
mise run dev   # go run . --tsnet-verbose --hostname what-dev
```

Without mise, install Go 1.27.1 or later and [Bun](https://bun.sh) 1.4 or
later, then run:

```sh
bun install
bun run build:css
go run . --tsnet-verbose --hostname what-dev
```

Tailwind CSS v4 is configured in `css/input.css`; edit the `@theme` block there
to change colors or fonts.

Then authenticate with your Tailscale account and visit your service.

## Making production builds

Build it with Docker or Podman:

```sh
docker build .
```

or

```sh
podman build .
```

## Deployment

You can use the following Docker compose file to deploy this to your
infrastructure:

```yaml
services:
  what:
    image: ghcr.io/tailscale-dev/what:latest
    restart: always
    volumes:
      - what-data:/data
    environment:
        - TS_AUTHKEY=tskey-auth-hunter2-hunter2hunter2hunter2
        - TSNET_HOSTNAME=what
        - TSNET_VERBOSE=true
        - DATA_DIR=/data
        - SLOG_LEVEL=INFO

volumes:
    what-data:
```
