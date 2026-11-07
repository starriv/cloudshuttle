# 开发

只需要 Go 1.27.3 或更高版本。构建不依赖 cgo，也不需要 Node。代码结构见 [architecture.md](architecture.md)。

```sh
# 打包并运行：调试和发布是同一个应用（同一个标识、数据目录和钥匙串条目）
pkill -x CloudShuttle; go tool mygo build -skip-dmg -platform darwin/arm64 && open build/darwin-arm64/CloudShuttle.app
go test ./...               # 单元、集成和界面测试
CLOUDSHUTTLE_TEST_SMB=smb://user:pass@nas.local/share go test ./internal/engine  # 再连一台真实的 SMB 服务器
CLOUDSHUTTLE_TEST_LAUNCHD=$(mktemp -d) go test ./internal/launchagent  # 向 launchd 登记一个临时任务再删除（macOS）
go vet ./... && go tool mygo vet
go tool mygo generate       # 修改 locales/*.json 之后执行
```

构建后打开应用默认只显示状态栏图标，不弹主窗口、不显示 Dock 图标（还没有存储和任务时会打开主窗口，方便第一次配置）；点击状态栏菜单中的「打开云梭」进入主界面。关闭主窗口后应用继续运行，退出请使用状态栏菜单。

`go tool mygo` 会运行 `go.mod` 里锁定版本的 MyGo CLI。

只有一个环境：调试也用 `mygo build` 打出的正式包，不用 `mygo dev`。`mygo dev` 会把应用改名为「CloudShuttle Dev」、标识加上 `.dev`，数据目录和钥匙串条目都和正式包分开，所以不再使用；代价是没有改代码后自动重建，改完要重新打包。

## 发布

发布在 GitHub Actions 上打包（`.github/workflows/release.yml`），不依赖本机。改好 `mygo.json` 的 `version`，提交后推送同名标签：

```sh
git tag v1.3.1 && git push origin main v1.3.1
```

workflow 先检查标签和 `version` 一致，建立草稿 Release `v<版本>`，再依次打三个包，连同各自的更新文件上传到草稿（不能同时：每个包都要合并草稿里的 `SHA256SUMS` 再传回，两个 macOS 包还都上传 `install.sh`，同时上传会互相覆盖）：

| 任务 | 运行在 | 安装包 |
|---|---|---|
| macOS universal | macOS | `cloudshuttle-<版本>.dmg`，Intel 与 Apple 芯片通用 |
| macOS Apple silicon | macOS | `cloudshuttle-<版本>-arm64.dmg` |
| Windows x64 | Ubuntu（NSIS 来自 apt） | `cloudshuttle-setup-<版本>-amd64.exe` |

全部完成后，在 GitHub 上检查草稿、补写说明，发布并设为 Latest，已安装的应用下次检查时就会更新到它。某个任务失败时，「Re-run failed jobs」重打那一个，会上传到同一个草稿。

本机也能打同样的包（`go tool mygo build -platform darwin/universal` 等，Windows 需要 `brew install makensis`），设置下文的私钥并加上 `-upload` 就和 CI 一样上传到草稿。

### 仓库 Secrets

workflow 用到三个 Secret（Settings > Secrets and variables > Actions，或用 `gh secret set`）：

| Secret | 内容 |
|---|---|
| `MYGO_UPDATER_PRIVATE_KEY` | 更新私钥 `mygo-update.key` 的内容：`gh secret set MYGO_UPDATER_PRIVATE_KEY < mygo-update.key` |
| `MACOS_CERTIFICATE` | `macos.signingIdentity` 那张证书连同私钥导出的 `.p12`（钥匙串访问 > 我的证书 > 导出），base64：`base64 -i cert.p12 \| gh secret set MACOS_CERTIFICATE` |
| `MACOS_CERTIFICATE_PASSWORD` | 导出 `.p12` 时设的密码 |

CI 必须用和 `signingIdentity` 同一张证书签名：钥匙串按证书认应用，换一张证书，用户更新后每个密钥都会再询问一次（见「签名」）。证书过期或更换时，同时更新 `mygo.json` 和这两个 Secret。

### 自动更新

已安装的云梭从 GitHub 最新的 Release 读取更新（见 architecture.md「自动更新」），更新必须用更新私钥签名：

- 私钥由 `go tool mygo keygen` 生成一次（`mygo-update.key`，默认在 `~/Library/Application Support/mygo/update-keys/`），公钥 `mygo-update.pub` 的内容写在 `mygo.json` 的 `updates.publicKey`。私钥**不进仓库**，另存一份离线备份：丢了它，已安装的应用就再也收不到更新；别人拿到它，就能向用户推送代码。不要再运行 `keygen -force`，那会换掉这对密钥。
- 有私钥时，每个平台在安装包旁边多出更新包 `cloudshuttle-<版本>-<平台>.tar.gz` 和清单 `update-<平台>.json`。应用从最新 Release 读清单、下载清单指向的更新包，所以这两种文件必须留在 Release 里。`mygo.json` 的 `updates.deltas` 为 0，不生成增量包 `*.delta`，让 Release 的文件保持精简；更新包只有十几到二十几 MB。
- 打包还会上传命令行安装脚本 `install.sh`、`install.ps1` 和校验和 `SHA256SUMS`。云梭不提供命令行安装，workflow 最后的 `tidy-release` 会删掉它们；安装包的 SHA-256 写在 Release 说明里。
- 只有发布并设为 Latest 的 Release 会推给用户；草稿和预发布版不会。

没有 `updates` 配置或没有私钥打出的包不能自动更新，设置页会说明并链接到下载页。1.1.0 及更早的版本没有更新功能，用户需要手动装一次第一个带更新的版本。

## 签名

签名证书在 `mygo.json` 的 `macos.signingIdentity`（`security find-identity -v -p codesigning` 列出本机的证书）。钥匙串按签名认应用：用证书签名时，身份是「应用标识 + 证书」，重新打包和更新都不变，应用存的密钥读取时不会再询问；临时签名的身份是二进制的哈希，每次打包都变，每次都会询问。因此即使没有 Developer ID，也要用固定的证书签名。

目前用 Apple Development 证书签名，没有公证，用户第一次打开时需要在「系统设置 > 隐私与安全性」中点「仍要打开」（见 README 的安装说明）。取得 Developer ID Application 证书后，把 `signingIdentity` 换成它，并用 `xcrun notarytool store-credentials notary` 保存公证凭据、在 `mygo.json` 中加上 `"notarize": {"keychainProfile": "notary"}`，打出的 DMG 即经过公证，用户可直接打开。更换证书后，已安装用户的钥匙串会对每个密钥询问一次。

应用每次启动只从钥匙串读取每个密钥一次，之后从内存取；启动时还会先读出定时任务要用的密钥，这样即使需要授权，也是在打开应用时询问。后台调度进程就是同一个已签名的可执行文件，钥匙串认它是同一个应用，在这里选过「始终允许」后，无人值守的定时运行不会再被询问卡住。

## 数据位置

| 内容 | 位置 |
|---|---|
| 存储、任务、设置、运行历史和每次运行的日志 | 应用数据目录下的 `cloudshuttle.db`（SQLite）：macOS 为 `~/Library/Application Support/CloudShuttle`，Linux 为 `~/.config/CloudShuttle`，Windows 为 `%AppData%\CloudShuttle` |
| iCloud 备份（开启时） | `~/Library/Mobile Documents/com~apple~CloudDocs/CloudShuttle/cloudshuttle.backup`，加密，包含存储、任务、设置和调度去重状态 |
| 备份密钥 | 系统凭据存储，键名 `backup/key`（由密码拉伸出的密钥，不是密码本身） |
| 已装好、等应用退出后换上的更新 | 应用旁边的隐藏文件夹 `.CloudShuttle.app.update-*`，换上后删除 |
| 更新后重新打开主窗口的标记 | 数据目录下的 `show-after-update`，新版本启动时删除 |
| 应用自身的日志（不含密钥） | 同一目录下的 `cloudshuttle.log`，后台调度进程的行带 `process=scheduler` |
| 进程锁（窗口、后台调度、传输轮换、配置维护） | 同一目录下的 `app.lock`、`scheduler.lock`、`transfer.lock`、`maintenance.lock`，内容为空 |
| 定时任务的系统登记（macOS） | `~/Library/LaunchAgents/com.starriv.cloudshuttle.scheduler.plist`；没有任何定时任务、或未允许在退出后运行时删除 |
| Secret Access Key、SMB 密码 | 系统凭据存储，服务名为应用标识 `com.starriv.cloudshuttle` |

运行历史和运行日志默认保留 90 天，更早的会在应用启动时清理（正在运行的记录不动）；被同步的文件不受影响。

## 品牌素材

正式图标采用白色云朵与蓝色对向传输箭头。`design/brand/app-icon.svg` 是跨平台静态图标的矢量源稿，`design/brand/AppIcon.icon` 分别保留云朵和传输箭头；`macos.icon` 指向这个文档，由 Xcode 的 `actool` 编译。macOS 包含原生分层资产目录和给旧系统使用的 ICNS，Windows 安装程序及可执行文件从 `resources/icon.png` 生成图标。

需要重新导出素材时，使用安装了 CairoSVG 和 resvg-py 的 Python 3 执行 `python3 design/brand/generate.py`，生成 `resources/icon.png`、透明镂空箭头的 `resources/mark.svg`、macOS 的 `cmd/cloudshuttle/tray.png` 模板图和其他系统使用的 `tray-color.png`。Python、CairoSVG 和 resvg-py 仅用于维护素材，正常构建不需要它们。状态栏的单色图沿用相同云朵和对向箭头结构，由 macOS 自动适配浅色和深色菜单栏。

设计源稿放在 `design/brand/`，不会作为运行时资源整份装入应用。README 展示与应用打包使用同一份 `resources/icon.png`。
