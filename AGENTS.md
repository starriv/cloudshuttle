# CloudShuttle — agent rules

CloudShuttle is a Go desktop app built with MyGo native UI, with rclone compiled in. Read [docs/architecture.md](docs/architecture.md) before changing code.

## Commands

- Go 1.27.3+. Every build must pass with `CGO_ENABLED=0`.
- One environment: debug the production build, `go tool mygo build -skip-dmg -platform darwin/arm64`, then open `build/darwin-arm64/CloudShuttle.app`. Don't use `go tool mygo dev`: it renames the app and its identifier (`.dev`), so its data and keychain items are apart. `macos.signingIdentity` in `mygo.json` signs every build with a certificate; an ad-hoc signature would change on every build, and the keychain would ask each time before the app reads its secrets.
- Before finishing a change: `go vet ./...`, `go tool mygo vet`, `go test ./...`.
- After editing `locales/*.json`: `go tool mygo generate`, then `go tool mygo i18n check`.

## Layers

- `internal/domain` holds the model, validation and errors. It imports no other package of the app.
- `internal/store` is the only package that touches SQLite. Schema changes go in `schema.sql`; a change to an existing table also needs a step appended to `migrations` in `store.go`.
- `internal/engine` is the only package that imports rclone. Add a backend there with a blank import, and build its remotes in memory: never write `rclone.conf` or read rclone's environment (so SMB uses NTLM, never Kerberos).
- `internal/discovery` finds SMB servers with mDNS, in pure Go; only `service` calls it.
- `internal/seal` encrypts the backups with a password (Argon2id, XChaCha20-Poly1305, `golang.org/x/crypto`); only `service` calls it. Plaintext of a backup never leaves the data folder.
- `internal/service` orchestrates. The view calls it, never the store or the engine. Two processes run it on one database: the window (`OwnerApp`) and the scheduler the system starts (`OwnerScheduler`, `RunDue`); anything that runs tasks must hold across both (see `service/schedule.go`).
- `internal/launchagent` registers the scheduler with the system (launchd on macOS); `cmd` hands it to `service` as a `Registrar`.
- `internal/view` is the native UI; `internal/island` is the notch pill's state and view. Both share `internal/icons` and `internal/format`.
- `cmd/cloudshuttle` wires the window, tray, credential store, notifications, the scheduler process (`--scheduler`, no window, no `mygo.App.Run`) and, on macOS, the island's window. AppKit calls go through `purego/objc` in `_darwin.go` files, on the main thread (`mygo.RunOnMain`), with a no-op beside them for other systems.

## Rules

- Secrets go through `service.Secrets` into the system credential store, never into SQLite, logs or messages.
- Validation returns `domain.Problems` with a code per field; the view turns codes into messages. Don't put user-facing text in `domain`, `service` or `engine`.
- No hardcoded user-facing text in `internal/view` or `cmd`: add a message to `locales/en.json` and `locales/zh-Hans.json`.
- The view runs on the main thread. Never block it on the network or a dialog: start a goroutine and return through `Host.Update`.
- Keep build values (`ui.Element`) out of state; keep state in `App`. `go tool mygo vet` checks this.
- Use the theme's colors (`c.Theme()`), not literals; show status with an icon and a word, not color alone. Icons are Tabler outline SVGs copied into `internal/icons/svg`.
- Motion answers a change (navigation, a row arriving, progress); use `Transition`, `AnimateTo` and springs, never timers, so Reduce Motion is respected.
- Document only the current architecture. Update `docs/architecture.md` when behavior or layers change, and `docs/development.md` when commands, signing or data locations change. `README.md` is for people who use the app: product highlights and installation only, no technical detail.
