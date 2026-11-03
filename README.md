# 云梭 CloudShuttle

在本机文件夹、NAS 与云存储之间复制和同步文件的桌面应用，支持手动运行和定时运行。

![总览](docs/screenshots/dashboard.png)

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/task.png" alt="任务"></td>
    <td width="50%"><img src="docs/screenshots/storage.png" alt="存储"></td>
  </tr>
  <tr>
    <td align="center">任务</td>
    <td align="center">存储</td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/history.png" alt="历史"></td>
    <td width="50%"><img src="docs/screenshots/setting.png" alt="设置"></td>
  </tr>
  <tr>
    <td align="center">历史</td>
    <td align="center">设置</td>
  </tr>
</table>

## 产品亮点

- **多种存储**：支持 NAS（SMB）、兼容 S3 的对象存储（Amazon S3、Cloudflare R2、阿里云 OSS、腾讯云 COS、华为云 OBS、MinIO 等）及本机文件夹，可自动发现局域网中的 NAS。
- **定时任务**：按小时、天、周、月设置计划；在 macOS 上经允许后，云梭退出后仍按时运行，错过的运行在开机或唤醒后补运行。
- **删除保护**：同步前先行预演；来源为空或将删除目标半数以上文件时暂停运行，确认后方可继续。
- **运行记录**：完整保存每次运行的日志与传输统计，失败时发送系统通知；在 macOS 上，屏幕顶部实时显示进度。
- **凭据安全**：访问密钥与 NAS 密码保存在系统钥匙串，不写入应用数据。
- **加密备份**：配置可加密备份至 iCloud 云盘，更换电脑后凭备份密码恢复（macOS）。
- **开箱即用**：内置传输引擎，无需安装其他工具；原生界面，支持浅色、深色或跟随系统外观，界面语言随系统切换中英文。

## 安装

从 [Releases](https://github.com/starriv/cloudshuttle/releases/latest) 下载最新版本：

| 文件 | 适用系统 |
|---|---|
| `cloudshuttle-<版本>.dmg` | macOS 通用版，适用于所有 Mac（Intel 与 Apple 芯片） |
| `cloudshuttle-<版本>-arm64.dmg` | macOS Apple 芯片（M 系列）专用版，体积更小 |
| `cloudshuttle-setup-<版本>-amd64.exe` | Windows 10 / 11（64 位） |

### macOS

系统要求：macOS 12 或更高版本。不确定机型时，请下载通用版。

1. 打开 DMG，将「CloudShuttle」拖入「应用程序」文件夹。
2. 在「应用程序」中双击打开云梭。

当前版本尚未经过 Apple 公证，首次打开时系统会提示「无法验证开发者」。请按以下步骤放行：

1. 在提示框中点「完成」。
2. 打开「系统设置 > 隐私与安全性」，在页面下方点「仍要打开」。
3. 输入登录密码确认。

放行只需进行一次；安装新版本后需重新放行。也可在终端执行以下命令代替上述步骤：

```sh
xattr -dr com.apple.quarantine /Applications/CloudShuttle.app
```

首次使用时的系统授权：

- **本地网络**：首次连接 NAS 时，请允许云梭访问本地网络。
- **后台运行**：首次保存定时任务时，云梭会询问是否允许在退出后继续运行定时任务。允许后，「系统设置 > 通用 > 登录项与扩展」中将出现云梭的后台项目。

### Windows

运行安装程序，按提示完成安装。

当前版本尚未进行代码签名，运行安装程序时可能出现「Windows 已保护你的电脑」提示。请点「更多信息」，再点「仍要运行」。

Windows 版与 macOS 版的差异：

- 定时任务在云梭运行期间执行；关闭窗口后云梭在通知区域继续运行，退出后定时任务暂停。
- 不提供 iCloud 加密备份。
- 访问密钥与 NAS 密码保存在 Windows 凭据管理器中。

---

参与开发请参阅 [docs/development.md](docs/development.md)。
