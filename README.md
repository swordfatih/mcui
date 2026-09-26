# MCUI

A small dashboard for self-hosted Minecraft servers. The first version creates Bedrock or Java servers, imports an existing world, creates Google Drive backups, and starts or stops each server with Docker Compose. Bedrock is the default in the UI.

## Requirements

- Linux host with Docker and the Docker Compose plugin
- Go 1.26.2 and Node 24 for local development
- Access to the Docker daemon for the account running MCUI

The dashboard can create and control containers, so run it on a trusted host. The supplied deployment exposes it through an existing `nginx-proxy` network.

## Run locally

```sh
cd web
npm ci
npm run build
cd ..
MCUI_ADDR=127.0.0.1:8080 MCUI_SERVERS_DIR=./servers go run ./cmd/mcui
```

Open `http://127.0.0.1:8080`. For frontend development, run `npm run dev` in `web/`; Vite proxies `/api` to the Go server.

## Run MCUI with Docker Compose

The root [docker-compose.yaml](docker-compose.yaml) follows the `nginx-proxy` and Let's Encrypt setup shown in the example. Set `DOMAIN`, `LETSENCRYPT_EMAIL`, and `MCUI_SERVERS_DIR` in `.env` (see [.env.example](.env.example)). The proxy network must already exist. `MCUI_SERVERS_DIR` must be an **absolute host path**; MCUI mounts it at the same path inside its container so nested server Compose projects use valid host bind paths.

```sh
cp .env.example .env
# Edit .env: set DOMAIN, LETSENCRYPT_EMAIL, MCUI_SERVERS_DIR=$PWD/servers,
# and MCUI_HTTP_PASSWORD to a long random password
mkdir -p "$PWD/servers"
docker compose up --build -d
```

Open `https://<DOMAIN>` and sign in with `MCUI_HTTP_PASSWORD`. MCUI uses a 12-hour session cookie; signing out ends that session. The Docker Compose deployment requires a password. A native run enables the sign-in page when `MCUI_HTTP_PASSWORD` is set. Use HTTPS to protect credentials and sessions. The dashboard container needs the Docker socket to manage the server projects. Restrict access to the dashboard and protect the Docker socket.

## Google Drive backups

MCUI creates an encrypted [restic](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html#other-services-via-rclone) repository for each server at `rclone:<remote>:<path>/<server-name>`. The default is `rclone:drive:mcui/<server-name>`. [rclone](https://rclone.org/drive/) handles Google Drive access. The repository password is required to access backups later; keep a separate secure copy of it.

1. Install rclone on the host and configure a Google Drive remote named `drive` in `backup/rclone.conf`. Use `mkdir -p backup` followed by `rclone config --config "$PWD/backup/rclone.conf"`. rclone's shared Google client ID is being retired, so configure your own Google OAuth client ID as its Drive guide recommends. If you use a different remote name, set `MCUI_BACKUP_REMOTE` in `.env`.
2. Create `backup/restic-password` with a long random secret, for example `openssl rand -base64 48 > backup/restic-password`, then run `chmod 600 backup/rclone.conf backup/restic-password`. Save that password somewhere outside this host as well.
3. Test the remote with `rclone lsd --config "$PWD/backup/rclone.conf" drive:`. Start or rebuild MCUI with `docker compose up --build -d`. The dashboard will show when backup configuration is ready.

The `backup/` directory is mounted into MCUI and ignored by Git and the image build. It must be writable because rclone may refresh its OAuth token. The Compose setup installs both restic and rclone in the MCUI image. For a native Go run, install those commands locally and set `MCUI_BACKUP_DIR` to the absolute path of `backup/`.

Click **Back up now** on a server for an on-demand snapshot. `MCUI_BACKUP_INTERVAL` schedules backups for every server; the default is `24h`, and `0` disables scheduling. Jobs run one at a time. The interval starts when MCUI starts. MCUI stops a running server, copies its `compose.yaml` and `data/` into a temporary folder under `servers/.backup-stage/`, then restarts the server before uploading the copy. Ensure there is enough free space for one full server copy. If the server was already stopped, MCUI leaves it stopped. The dashboard shows job progress and the last successful snapshot ID; the latter is also stored in `servers/<name>/last-backup.json`. A failed capture or upload is reported in the dashboard. Restoring and deleting old snapshots are outside this version's scope.

## Create a server

1. Pick Bedrock (default) or Java, a lowercase name, and an unused host port. Defaults are `19132/udp` for Bedrock and `25565/tcp` for Java.
2. Optionally supply an absolute path on the **host** to an existing world directory, `.zip`, `.tar.gz`, or `.tgz` archive. If MCUI runs in Docker, the path must also be mounted into the MCUI container; the `servers` mount is already available. For imports elsewhere, add a read-only bind mount to the root `docker-compose.yaml`.
3. Confirm the Minecraft EULA, create the server, then click Start. The first start may take a while because it downloads the image and Minecraft server files.

A valid world has a `level.dat` file. Archives may contain a single enclosing folder. MCUI rejects links, special files, absolute or parent-traversal archive entries, multiple worlds, and archives whose declared extracted contents exceed 4 GiB. Imported worlds are copied; the source stays in place. Bedrock worlds go to `data/worlds/world`, with `LEVEL_NAME=world`; Java worlds go to `data/world`.

Each server has this layout after its first successful backup:

```text
servers/server1/
├── compose.yaml
├── data/
└── last-backup.json
```

`last-backup.json` records the last successful snapshot ID and time. The backup contents are stored in that server's encrypted Google Drive restic repository. The folder has its own Compose file and `data/`; `last-backup.json` appears after the first successful backup. Those files are the source of truth. MCUI has no database. It uses `docker compose config` to find a single service using an itzg Minecraft image; the service name and published port can vary, and Docker Compose's standard Compose filenames are supported. Keep these folders together when moving to another host, then start the projects there. The dashboard does not delete servers in this version.

MCUI uses [itzg/minecraft-bedrock-server](https://github.com/itzg/docker-minecraft-bedrock-server) for Bedrock and [itzg/minecraft-server](https://github.com/itzg/docker-minecraft-server) for Java. Both images mount the server's `data/` at `/data`.

## API contract

JSON requests and responses:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/servers` | List `{name, edition, status}` objects |
| `POST` | `/api/servers` | Create from `{name, edition, port, worldPath, acceptEula}` |
| `POST` | `/api/servers/{name}/start` | Run `docker compose up -d` |
| `POST` | `/api/servers/{name}/stop` | Run `docker compose stop` |
| `GET` | `/api/backups/config` | Backup readiness and schedule |
| `GET` | `/api/servers/{name}/backup` | Current or last backup status |
| `POST` | `/api/servers/{name}/backup` | Queue a Google Drive backup |

`edition` is `bedrock` or `java`; `worldPath` may be empty. Errors have an `error` string. Status is polled every five seconds and may be `running`, `stopped`, another Docker state, or `unknown` if Docker cannot be queried. Start and stop errors are returned to the UI.

## Build and test

```sh
go test ./...
cd web && npm ci && npm run build
```

## Future design

Bedrock pack management can operate on `data/resource_packs`, `data/behavior_packs`, and the active world's pack JSON files. Order and activation belong in those world files. Backup capture already includes each server's data folder and Compose file. A future restore flow should stop the server first, and retention needs a separate policy. Java may alternatively use `itzg/mc-backup`, but that image does not support Bedrock. None of that requires a database for the current server inventory.
