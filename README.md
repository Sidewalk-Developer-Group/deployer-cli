# deployer

The command-line client for [Deployer](https://deployer.sidewalkdevelopers.io) — trigger
deployments to your own servers, stream their logs, and get the outcome back as an exit
code, so the same command works at a prompt and in CI.

```
$ deployer deploy billing-api production
billing-api → production  ◌ queued
deployment 0198f3c1-6b2f-7a41-9c88-3f1c0d5e7a22  branch main
› following — press Ctrl-C to stop watching (the deployment keeps running)
connect   web-01  ed25519 key verified
clone     releases/v129            2.1s
deps      composer install        14.8s
build     npm ci && npm run build 31.2s
migrate   artisan migrate --force  3.4s
activate  current -> releases/v129
✓ 0198f3c1-6b2f-7a41-9c88-3f1c0d5e7a22 (v129) in 52.4s
```

The CLI is a thin client over the instance's REST API: it holds no deployment logic of
its own, so every permission check, approval gate and audit entry is the server's.

## Install

```sh
brew install sidewalk-developer-group/tap/deployer-cli
```

The formula is `deployer-cli` because homebrew-core already has a `deployer`
(PHP Deployer); the command it installs is still `deployer`.

Or download a binary for macOS/Linux (amd64/arm64) from the
[releases page](https://github.com/Sidewalk-Developer-Group/deployer-cli/releases).

## Log in

Deployer is self-hosted, so start by pointing the CLI at your instance. Create a
personal access token in the web app under **Settings › Tokens** with at least the
`projects:read`, `deployments:read` and `deployments:create` abilities — add
`deployments:approve` to approve or reject from the terminal.

```sh
deployer login --api https://deployer.example.com
```

The token is read from a hidden prompt (or from stdin) and stored in your OS keychain —
never in a flag, which would leave it in shell history and in the process list. Where no
keyring exists, it falls back to a `0600` file and says so.

The instance is saved as a *context*. Add as many as you work with:

```sh
deployer context ls
deployer context use staging --org acme
```

## Use

```sh
deployer projects ls
deployer envs ls billing-api
deployer status billing-api                     # every environment at a glance
deployer status billing-api production          # the last deployment in detail

deployer deploy billing-api production
deployer deploy billing-api staging -b fix/timeout -m "hotfix for the 502s"
deployer deploy billing-api production --dry-run
deployer deploy billing-api production --no-follow      # return once accepted

deployer logs billing-api production            # the latest deployment's log
deployer logs <deployment-id> --follow

deployer cancel <deployment-id>
deployer retry <deployment-id>
deployer rollback <deployment-id>
deployer approve <deployment-id> -m "release window agreed"
deployer reject <deployment-id> -m "waiting on the DB migration review"
```

Add `--json` to any command for machine-readable output. While following, `--json` emits
newline-delimited events (`{"type":"log",…}`, `{"type":"status",…}`) so you can pipe it
into `jq`.

## In CI

Following is on by default everywhere, terminal or not, so a CI job gets the real
outcome without asking for it:

```sh
export DEPLOYER_API_URL=https://deployer.example.com
export DEPLOYER_TOKEN=$DEPLOYER_DEPLOY_TOKEN   # a scoped token from Settings › Tokens
export DEPLOYER_ORG=acme

deployer deploy billing-api production --json
```

The exit code is the outcome:

| Code | Meaning |
|---|---|
| 0 | Deployed — the release is live |
| 1 | The deployment failed, was cancelled, or finished degraded |
| 2 | Usage error: bad flags, unknown project/environment, ambiguous organization |
| 3 | Auth error: no token, revoked token, missing ability |
| 4 | Could not reach the instance, or the API refused the request (e.g. a 409 conflict) |
| 5 | The deployment is waiting on an approval gate (pass `--wait-approval` to wait) |
| 130 | Interrupted — the deployment keeps running server-side |

`degraded` (some targets failed) is deliberately **not** a success: it exits 1.

## Configuration

| Setting | Flag | Environment | Config file |
|---|---|---|---|
| Instance URL | `--api` | `DEPLOYER_API_URL` | `contexts.<name>.api_url` |
| Context | `--context` | `DEPLOYER_CONTEXT` | `current_context` |
| Organization | `--org` | `DEPLOYER_ORG` | `contexts.<name>.organization` |
| Token | — (never a flag) | `DEPLOYER_TOKEN` | OS keychain |

Flags beat environment variables, which beat the config file. The config lives in
`$DEPLOYER_CONFIG_DIR`, else `$XDG_CONFIG_HOME/deployer`, else `~/.config/deployer`.
Set `DEPLOYER_NO_KEYRING=1` to skip the keyring (useful on headless Linux where a
keyring prompt would block forever).

## Build from source

```sh
go build -o deployer .
go test ./...
```

## Licence

Apache-2.0. See [LICENSE](LICENSE).
