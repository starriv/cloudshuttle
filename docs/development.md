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

`go tool mygo` 会运行 `go.mod` 里锁定版本的 MyGo CLI。

只有一个环境：调试也用 `mygo build` 打出的正式包，不用 `mygo dev`。`mygo dev` 会把应用改名为「CloudShuttle Dev」、标识加上 `.dev`，数据目录和钥匙串条目都和正式包分开，所以不再使用；代价是没有改代码后自动重建，改完要重新打包。

## 发布

```sh
go tool mygo build -platform darwin/universal   # build/cloudshuttle-<版本>.dmg，Intel 与 Apple 芯片通用
go tool mygo build -platform darwin/arm64       # build/cloudshuttle-<版本>-arm64.dmg，仅 Apple 芯片
go tool mygo build -platform windows/amd64      # build/windows-amd64/cloudshuttle-setup-<版本>-amd64.exe，需要 NSIS（brew install makensis）
```

版本号在 `mygo.json` 的 `version`。两个 DMG 和 Windows 安装程序上传到 GitHub Release，标签为 `v<版本>`。

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
| 应用自身的日志（不含密钥） | 同一目录下的 `cloudshuttle.log`，后台调度进程的行带 `process=scheduler` |
| 进程锁（窗口、后台调度、传输轮换、配置维护） | 同一目录下的 `app.lock`、`scheduler.lock`、`transfer.lock`、`maintenance.lock`，内容为空 |
| 定时任务的系统登记（macOS） | `~/Library/LaunchAgents/com.starriv.cloudshuttle.scheduler.plist`；没有任何定时任务、或未允许在退出后运行时删除 |
| Secret Access Key、SMB 密码 | 系统凭据存储，服务名为应用标识 `com.starriv.cloudshuttle` |

运行历史和运行日志默认保留 90 天，更早的会在应用启动时清理（正在运行的记录不动）；被同步的文件不受影响。
