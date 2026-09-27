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

Each server card opens its own dashboard with **Overview**, **Infrastructure**, and **Console** tabs. Overview shows a copyable `DOMAIN:PORT` address, start and stop controls, online and maximum player counts, and Google Drive backup status and action. Player counts refresh every 15 seconds through the `mc-monitor` tool included in the itzg images. Java may also expose a sample of player names; Bedrock status provides counts without names. Infrastructure groups Docker CPU, memory, network and block I/O, `/data` storage usage, container image and game settings, all environment variables, and the raw Compose YAML. Resource metrics refresh every 15 seconds; storage size refreshes every 60 seconds. Docker stats values describe container usage, and the storage value is the size of `/data`, not free disk capacity. Console sends Java commands through the image's `rcon-cli` and Bedrock commands directly to the container's open stdin through Docker's attach API, with logs refreshed every three seconds. Java command responses appear directly; Bedrock replies appear in the logs. New Bedrock servers enable `stdin_open` and `tty` for console commands; existing Bedrock containers without open stdin need those Compose settings and a stop/start before commands work. The Bedrock console requires access to a local Docker Unix socket. The Docker deployment passes `DOMAIN` as `MCUI_PUBLIC_HOST`; native runs can set `MCUI_PUBLIC_HOST` explicitly. Compose edits are validated before saving and take effect after the server is stopped and started again. Server data editing is planned for later.
Server dashboards have shareable `/servers/<name>` URLs. Refreshing a dashboard keeps the same server open, and browser Back and Forward navigate between pages.

## Google Drive backups

MCUI uploads a standard, unencrypted `.tar.gz` archive for each backup to `<remote>:<path>/<server-name>/`. The default is `drive:mcui/<server-name>/`. Each archive contains only persistent user files under `data/`, selected by the same embedded policy used for server reset. This includes worlds, settings, allow lists, and custom packs. Runtime files, built-in packs, generated copies, and Compose are excluded. [rclone](https://rclone.org/drive/) handles Google Drive access.

1. Install rclone on the host and configure a Google Drive remote named `drive` in `backup/rclone.conf`. Use `mkdir -p backup` followed by `rclone config --config "$PWD/backup/rclone.conf"`. rclone's shared Google client ID is being retired, so configure your own Google OAuth client ID as its Drive guide recommends. If you use a different remote name, set `MCUI_BACKUP_REMOTE` in `.env`.
2. Set `chmod 600 backup/rclone.conf` to protect the Google Drive credentials.
3. Test the remote with `rclone lsd --config "$PWD/backup/rclone.conf" drive:`. Start or rebuild MCUI with `docker compose up --build -d`. The dashboard will show when backup configuration is ready.

The `backup/` directory is mounted into MCUI and ignored by Git and the image build. It must be writable because rclone may refresh its OAuth token. The Compose setup installs rclone in the MCUI image. For a native Go run, install rclone locally and set `MCUI_BACKUP_DIR` to the absolute path of `backup/`.

Click **Back up now** on a server for an on-demand archive. `MCUI_BACKUP_INTERVAL` schedules backups for every server; the default is `24h`, and `0` disables scheduling. Jobs run one at a time. The interval starts when MCUI starts. MCUI stops a running server, finds its `/data` mount through `docker compose config`, and stages only persistent files under `servers/.backup-stage/`. Bind mount sources must be accessible inside MCUI; named volumes are copied through the server container before filtering. MCUI then restarts a server that was running before compressing and uploading the copy. Ensure there is enough free space for one full server copy and its compressed archive; named-volume capture briefly needs room for an unfiltered copy as well. If the server was already stopped, MCUI leaves it stopped. The dashboard shows job progress and the last successful archive filename; the latter is also stored in `servers/<name>/last-backup.json`. A failed capture or upload is reported in the dashboard. Existing encrypted restic repositories are left on Drive; MCUI does not delete or convert them. Restoring and deleting old backups are outside this version's scope.

## Create a server

1. Pick Bedrock (default) or Java, a server folder name, and an unused host port. Defaults are `19132/udp` for Bedrock and `25565/tcp` for Java.
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

`last-backup.json` records the last successful archive filename and time. Each Google Drive archive contains persistent user files in `data/`; the Compose file stays in the server directory and must be retained separately when moving to another host. `last-backup.json` appears after the first successful backup. MCUI has no database. It uses `docker compose config` to find a single service using an itzg Minecraft image; the service name and published port can vary, and Docker Compose's standard Compose filenames are supported. The dashboard does not delete servers in this version.

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
| `GET` | `/api/server-details/{name}/logs` | Last 200 Compose log lines |
| `POST` | `/api/server-details/{name}/command` | Send one console command to a running server |
| `GET` | `/api/server-details/{name}/players` | Live player counts, version, and available Java player sample |
| `GET` | `/api/server-details/{name}/resources` | Live Docker container CPU, memory, network and block I/O |
| `GET` | `/api/server-details/{name}/storage` | Size of the container's `/data` folder |
| `GET`, `PUT` | `/api/server-details/{name}/settings` | Read or edit image and environment variables |
| `GET`, `PUT` | `/api/server-details/{name}/compose` | Read or edit validated Compose YAML |
| `GET` | `/api/server-reset/{name}` | Review files kept and deleted, with a revision token |
| `POST` | `/api/server-reset/{name}` | Reset stopped server data with `{confirm, revision}` |

`edition` is `bedrock` or `java`; `worldPath` may be empty. Errors have an `error` string. Status is polled every five seconds and may be `running`, `stopped`, another Docker state, or `unknown` if Docker cannot be queried. Start and stop errors are returned to the UI.

## Build and test

```sh
go test ./...
cd web && npm ci && npm run build
```

## Pack management

The Bedrock **Resources** tab lists installed resource and behavior packs, resolves localized names from `texts/en_US.lang`, and shows activation from the world's `world_resource_packs.json` and `world_behavior_packs.json`. Inactive packs are collapsed, with Bedrock-provided vanilla, chemistry, editor, experimental, and server library folders grouped separately by folder name. Numbered versions of a base folder, such as `vanilla_1.21.60`, are collapsed beneath the base pack. Active packs can be reordered with a mouse, touch, or keyboard; **Save changes** writes the two JSON files and does not restart the server. Restart the server to load the changes. Resource packs show their world JSON selection; behavior packs use recent Pack Stack lines in Docker logs for live load evidence. Pack icons are shown when `pack_icon.png` exists. Custom packs have detail pages; deleting one requires a stopped server, removes its folder, and removes any active world reference. The resource pack detail page has a searchable asset browser with a main-pack/subpack selector, image previews, and batch archive/restore. Archived files and a current-state index live in the server root under `.mcui`; restoring an asset removes its index entry. Archive preview shows exact entries removed from supported terrain and flipbook texture catalogs and `textures_list.json`. Other JSON references are flagged for review rather than automatically rewritten. Asset changes require a stopped server and a restart to apply. Bedrock-provided packs are shown read-only without activation controls or detail pages. Upload a `.zip`, `.mcaddon`, `.mcpack`, `.tar.gz`, or `.tgz` file, or provide a public HTTPS download URL. Installations extract into a temporary folder and remove the archive afterward. Pack management requires a bind-mounted `/data` folder containing exactly one world.

The **Files** tab browses the server directory, supports file upload, file download, folder ZIP download, and confirmed deletion. Uploads and deletion require a stopped server; existing files are never overwritten. The cleanup review identifies `backup-pre-*` directories and `*.backup-*` pack copies, with their sizes and reasons. It also identifies custom packs whose UUID is absent from the active world's pack JSON, while noting that they may be kept for later or another world. It does not mark Bedrock's versioned vanilla folders as unused. The active world, Bedrock-provided pack folders, server compose files, and MCUI metadata are protected from deletion.

**Reset server** in the Files tab requires the server to be stopped. It shows the exact files to keep and delete and requires the server name as confirmation. Reset preserves persistent user files, including worlds, settings, allow lists, and custom behavior and resource packs. It deletes built-in packs and all other files under `/data`, including runtime files and old generated copies. Empty directories are removed. Compose remains outside `/data`, so MCUI can start the server again. Reset supports bind mounts and named volumes. The two embedded YAML policies for built-in content and persistent user data are shared with backups; built-in rules win when both match. Java directories containing a `level.dat` file are treated as custom worlds. Symbolic links and special files in `/data` block reset and backup until they are removed or replaced.

## Future design

Backup capture includes persistent user files from the server's data folder. A future restore flow should stop the server first, and retention needs a separate policy. Java may alternatively use `itzg/mc-backup`, but that image does not support Bedrock. None of that requires a database for the current server inventory.
