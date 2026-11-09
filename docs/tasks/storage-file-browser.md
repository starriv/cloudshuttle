# Add a Finder-style storage browser / 为已添加的存储增加文件管理器

<!-- template-type: task; repository: starriv/cloudshuttle; label: type:task -->

## English

### Background and goal

Open a saved storage card and manage its files without setting up a sync task. The task editor already has a folder picker backed by `Service.Folders` and `engine.Folders`. Reuse the connection setup and navigation patterns for a dedicated file browser.

### Scope

Start with configured S3 buckets, including R2 and MinIO. Browse prefixes, filter by name in the current directory, inspect file details, upload and download files, create folders, rename or move individual objects within the bucket, and delete individual objects or empty folders after confirmation. Follow with the same browser for SMB shares. Track recursive search, non-empty directory mutations, batch mutations, and bucket administration separately.

### Steps and acceptance

- [ ] **1. Read-only browser.** Add Browse to storage cards and display breadcrumbs, a file list with name, size and modification time, refresh, and current-directory filtering. Handle empty, loading, denied and missing locations separately. Keep requests cancellable, bound the work for large lists, and ignore stale results after navigation.
- [ ] **2. File changes.** Use saved credentials and enforce the configured bucket boundary. Respect permission failures per operation. Download via a temporary file. Ask before replacing an existing file or permanently deleting an item. Handle S3 moves as copy followed by delete, preserving the source on copy failure and reporting both paths when the copy succeeds and deletion fails.
- [ ] **3. Backup coordination.** Serialize browser mutations with app and scheduler runs via the existing cross-process transfer lock. Under the lock, recheck the storage configuration and affected paths. Block mutations of the configured roots and reserved trash paths. Require explicit confirmation for changes under saved task destinations and explain that a later sync can overwrite or remove manual changes.
- [ ] **4. SMB support.** Open authorized shares from the server card, with direct entry by share name for accounts that cannot enumerate shares. Enforce a single-share boundary and handle case-insensitive collisions and case-only renames according to server behavior.
- [ ] **5. Verify.** Exercise least-privilege credentials, partial failures, cancellation, name collisions, stale requests and scheduler contention. Cover S3 with the existing fake service and verify provider behavior on R2 and MinIO. Use the existing opt-in SMB integration path and verify the production app in Chinese and English.

### Dependencies and risks

S3 empty folders require directory markers. Use browser-specific backend options to prevent implicit bucket creation. External clients can race local operations, so use backend preconditions where supported and surface ambiguous results. See [rclone S3 documentation](https://rclone.org/s3/) and [rclone move semantics](https://rclone.org/commands/rclone_move/).

## 中文

### 背景与目标

在「存储」卡片上点击「浏览」，进入 Finder 式文件管理页，直接管理已配置存储里的文件，无需先创建同步任务。现有任务表单的「浏览」通过 `Service.Folders` → `engine.Folders` 选择目录，可复用连接配置、凭据和路径导航模式。

### 范围

- **首版 S3、Cloudflare R2、MinIO。** 打开卡片所配置的桶，按前缀浏览。支持当前目录按名称筛选、查看文件名/大小/修改时间、上传、下载、新建文件夹、桶内单文件改名或移动、删除单个文件或空文件夹。
- **下一阶段 SMB。** 复用页面，从服务器卡片进入有权限的共享；支持手动输入共享名，用于无法列出共享但能访问指定共享的账号。
- **后续独立任务。** 递归搜索、批量写操作、非空文件夹递归移动或删除、跨桶或共享迁移，以及桶创建/删除、权限策略和历史版本管理。

### 子任务与验收标准

- [ ] **T1 · 浏览与查找。** 在存储卡片增加「浏览」入口，提供面包屑、列表、刷新、按名称筛选、双击或回车进入目录。清楚区分空目录、加载、权限不足和位置不存在。请求可取消，大目录有分批展示或明确的数量限制，切换目录或存储后旧请求不得覆盖当前视图。对桶内前缀受限的账号，允许直接打开已知前缀，并保留服务端的权限判定。
- [ ] **T2 · 上传、下载与新建。** 复用已保存凭据，禁止隐式创建桶。覆盖已有对象前单独确认；后端支持条件写入时用于防止并发误覆盖，无法确认同名状态时拒绝覆盖。下载使用临时文件，成功后再交付到选定位置，取消或失败后清理残留。S3 空文件夹以目录标记实现，刷新后仍能找到。权限拒绝只影响对应操作，禁止用试写或试删探测权限。
- [ ] **T3 · 改名、移动、删除与任务保护。** S3 先复制，确认目标成功后再删源；复制失败保留源，复制成功但删除失败时显示两条路径和实际状态，禁止盲目重试。删除前列出完整路径并明确说明文件管理页的删除语义。S3 版本保留仍由桶策略决定。写操作复用窗口和调度进程共用的传输锁，锁内重新校验存储配置和源/目标状态。禁止改动桶根、共享根及任何 `.cloudshuttle-trash` 路径；触及已保存任务的目标范围时显示任务名，确认后方可继续，并提示下次同步可能覆盖或删除手动变更。
- [ ] **T4 · SMB 接入。** 浏览可访问共享，将每次操作限制在当前共享内。覆盖共享拒绝访问、共享改名、大小写冲突及仅大小写改名。校验路径段和已有条目身份，拒绝会越界或经规范化指向其他对象的输入。相同的路径保护也适用于 S3。
- [ ] **T5 · 验证与文档。** 用现有 S3 测试服务覆盖只读凭据、仅前缀访问、同名冲突、复制后删源失败、取消和保护路径；用 R2、MinIO 复核真实权限与目录标记。SMB 沿用可选实机集成测试。验证浏览时切换目录及写入与后台调度并发的结果。更新实现后的架构文档，补齐中英文文案，执行 i18n 生成和检查，以及 `CGO_ENABLED=0` 下的 Go vet、MyGo vet、Go test 和签名生产构建验证。

### 实现约束

维持 `view → service → engine` 的调用方向。界面通过 goroutine 发起 I/O，并用 `Host.Update` 回填。服务从存储 ID 重新解析目标并经 `service.Secrets` 读取凭据。rclone 配置仅在引擎内存中构建。错误分为权限、同名、找不到、任务占用、取消和部分成功，界面用本地化文案显示。同步锁只协调本应用的两个进程，外部客户端并发仍须按后端能力处理。[rclone S3 文档](https://rclone.org/s3/)、[rclone 移动语义](https://rclone.org/commands/rclone_move/)
