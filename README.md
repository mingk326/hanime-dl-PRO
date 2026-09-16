# Hanime Downloader PRO

> 基于 [mingjiezxc/hanime-dl](https://github.com/mingjiezxc/hanime-dl) 的增强版本，新增完成记录、文件校验、失败日志、自动重试、降级下载等可靠性功能。

Hanime 视频下载工具，使用 Chrome DevTools Protocol 进行网页抓取，支持 CLI 和 Web 两种模式。

## 配套项目

- [hanime-config-generator](https://github.com/mingk326/hanime-config-generator) — 在线可视化生成 `config.yaml`，与本项目配套使用

---

## ✨ 功能特性

| 维度 | 说明 |
|------|------|
| 🎬 单视频 / 播放列表下载 | `SingleCode` / `ListCode` 灵活配置 |
| ⚡ 双模式 | CLI 命令行 + Web 管理界面 |
| 🔄 断点续传 | HTTP Range，网络中断从 `.tmp` 断点继续 |
| 📡 HTTP 代理 | 支持 HTTP/SOCKS5 代理，适配受限网络 |
| 🎚 多分辨率 | 1080p / 720p / 480p / 360p / 240p |
| 🧵 并发下载 | 可配置 `MaxDownloadWorkers` 多线程 |
| ✅ 完成记录 | Registry 记录，已下载视频**零网络请求**直接跳过 |
| 🛡 文件完整性校验 | MP4 / JPG 双重 `verifier` 校验，损坏自动重下 |
| 📝 双日志系统 | `Download-log.txt`（下载失败）+ `Completed-log.txt`（记录拒绝） |
| 🔁 自动重试 | 线性退避，重试前重新解析获取新 URL |
| 📉 降级下载 | 分辨率逐级降级 + watch 页面提取 |
| 🖼 封面防盗链 | Referer 头绕过 `vdownload.hembed.com` 403 |
| 🌐 强制 IPv4 | 避免部分 CDN 的 IPv6 连接被强制断开 |

---

## 🆕 v3.2.1 变更（最新）

- **修复 CDN IPv6 连接被强制断开的问题**：下载器新增仅 IPv4 拨号器。当本机同时存在 IPv6/IPv4 地址、且部分 CDN（如 `vdownload.hembed.com`）会强制断开 IPv6 连接时，强制走 IPv4 可避免下载失败。
- 配置文件新增可选字段 `HttpProxy`（支持 `http://` / `socks5://`），用于直连受限的场景。

## 📦 版本历史

| 版本 | 主要内容 |
|------|---------|
| V1 | 基础下载功能 |
| V2 | Registry、Verifier、双日志、自动重试 |
| V3 (PRO) | 降级下载、封面图防盗链修复、正常/降级流程分离 |
| V3.1 (PRO) | 降级下载视频正常记录（Demotion 标记）、JPG 未下载写入 Download-log |
| V3.2 (PRO) | 封面图下载失败不再记录到 Download-log.txt，仅保留控制台输出 |
| **V3.2.1 (PRO)** | **强制 IPv4 拨号，修复 CDN IPv6 连接断开导致的下载失败** |

## 可靠性设计详解

### 1. 完成记录系统（registry 包）

下载完成后记录视频信息到 `./Completed/`，每个视频一个独立的 `{videoID}.json`。重新运行程序时，已下载的视频**零网络请求**直接跳过。

**三层校验确保记录准确性：**
1. 记录存在且分辨率匹配
2. `os.Stat` 确认 MP4 文件存在（正常记录还需 JPG 存在）
3. 实际文件大小与记录一致

**写入规则：**
- **正常下载**：MP4 + JPG 都下载完成且通过校验后写入，标记 `demotion: false`
- **降级下载**：MP4 下载完成且通过校验即可写入，JPG 缺失/损坏时标记 `demotion: true`
- 拒绝写入的原因记录到 `./log/Completed-log.txt`

### 2. 文件完整性校验（verifier 包）

| 格式 | 校验规则 | 检测问题 |
|------|---------|---------|
| MP4 | 大小 > 10KB + 偏移 4-7 字节为 `ftyp` | HTML 错误页、空文件、损坏文件 |
| JPG | 大小 > 100B + 前 2 字节为 `FF D8` | HTML 错误页、空文件、损坏文件 |

校验失败且判定为损坏时自动删除文件，触发重新下载。

### 3. 双日志系统（failurelog 包）

```
./log/
  ├── Download-log.txt    ← 下载失败日志（解析失败、下载失败、校验失败、重试耗尽）
  └── Completed-log.txt   ← 记录拒绝日志（MP4/JPG 缺失或校验失败导致拒绝写入完成记录）
```

两个日志各有独立 mutex，互不干扰，格式统一为 `[时间] videoID=<ID> reason=<原因>`。

**封面图失败不记录日志：** 封面图（JPG）下载失败或校验失败时不写入 `Download-log.txt`，仅在控制台输出，避免大量封面失败信息污染下载失败日志。

### 4. 自动重试机制

- 配置项 `MaxRetryAttempts`（默认 3，0=不重试）
- 线性退避：第 1 次 10s，第 2 次 20s，第 3 次 30s
- 三种失败都触发重试：解析失败、下载失败、校验失败
- 每次重试前重新解析视频信息获取新的下载 URL（旧 URL 可能已过期）
- 中间失败不写日志，只有最终失败才记录到 `Download-log.txt`

### 5. Windows 文件名安全化

- 替换 9 个 Windows 非法字符（`< > : " / \ | ? *`）及控制字符为 `_`
- UTF-8 安全截断到 200 字节（不会在多字节字符中间截断）
- 去除末尾空格和点号
- 文件名添加 `[视频ID]` 前缀，如 `[407238][CEO NEET (ニート社長)] 标题.mp4`

### 6. 降级下载机制（fallback 机制）

当正常下载重试全部失败后，**自动触发降级下载**，通过 watch 页面点击下载按钮获取下载链接，按分辨率逐级降级尝试。

**降级流程：**
1. 导航到 watch 页面 (`https://hanime1.me/watch?v=XXX`) 建立会话
2. 点击下载按钮（自动处理直接可见 / 隐藏在 `more_horiz` 下拉菜单两种情况）
3. 提取所有分辨率的下载链接和封面图 URL
4. 按优先级降级：配置分辨率 → 720p → 480p → 360p → 240p

### 7. 六层中断恢复机制

| 层级 | 触发场景 | 恢复方式 |
|------|---------|---------|
| HTTP 断点续传 | 网络中断 | 从 `.tmp` 文件断点继续 |
| 下载器重试 | 408/429/5xx | 间隔 5s/15s 重试 |
| URL 刷新 | 410 URL 过期 | 刷新 URL 后重试 |
| 应用层重试 | 解析/下载/校验失败 | 重新解析 + 下载，退避 10-30s |
| 降级下载 | 重试全部失败 | watch 页面提取链接，分辨率逐级降级 |
| 程序重启恢复 | 程序崩溃 | 扫描缓存文件恢复任务 |
| 文件校验修复 | 文件损坏 | 删除损坏文件后重下 |

### 8. 强制 IPv4 拨号（v3.2.1 新增）

双栈主机（同时有 IPv6 与 IPv4 地址）下，部分 CDN 会强制断开 IPv6 连接导致下载失败。下载器为 HTTP Transport 配置了仅 IPv4 的拨号器（`tcp4`），避免该类连接中断。

---

## 📥 安装

### 前置要求

- **Go 1.24+**
- **Google Chrome 或 Chromium 浏览器**（用于 CDP 抓取与解析下载链接）

### 编译

```bash
git clone https://github.com/mingk326/hanime-dl-PRO.git
cd hanime-dl-PRO
go build -o hanime-dl .
```

### 多平台交叉编译参考（也可直接使用 Releases 产物）

```bash
GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o hanime-dl-linux-amd64 .
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o hanime-dl-windows-amd64.exe .
GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o hanime-dl-macos-amd64 .
```

> 各平台（Windows ×2 / Linux ×2 / macOS ×2）的可执行文件已打包在 [GitHub Releases](https://github.com/mingk326/hanime-dl-PRO/releases) 中，可直接下载使用。

---

## ⚙️ 配置

编辑 `config.yaml`：

```yaml
# Chrome 远程调试 URL
chromeRemoteURL: http://localhost:9222/json/version

# 缓存目录
CacheDir: ./cache

# 下载目录
DownDir: ./downloads

# 已完成视频记录目录（每个视频一个 {videoID}.json）
RegistryDir: ./Completed

# HTTP 代理（可选，支持 http:// 或 socks5://，直连受限网络时配置）
HttpProxy:

# 是否优先尝试直接下载
DirectDownloadFirst: true

# 最大并发下载线程数
MaxDownloadWorkers: 3

# 单个视频 ID 列表
SingleCode:
  - 408264

# 播放列表 ID 列表
ListCode: []

# 视频分辨率（如：1080p, 720p, 480p, 360p, 240p）
VideoResolution: 1080p

# 下载后清除缓存
ClearCache: true

# 单个视频失败后最大重试次数（0=不重试，3=最多重试3次）
MaxRetryAttempts: 3
```

### 配置项说明

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `chromeRemoteURL` | string | - | Chrome DevTools WebSocket URL |
| `CacheDir` | string | `./cache` | 解析缓存目录 |
| `DownDir` | string | `./downloads` | 视频下载目录 |
| `RegistryDir` | string | `./Completed` | 已完成视频记录目录 |
| `HttpProxy` | string | - | HTTP/SOCKS5 代理（可选） |
| `DirectDownloadFirst` | bool | `true` | 是否优先直接下载 |
| `MaxDownloadWorkers` | int | `3` | 并发下载线程数 |
| `SingleCode` | []string | - | 单个视频 ID 列表 |
| `ListCode` | []string | - | 播放列表 ID 列表 |
| `VideoResolution` | string | `1080p` | 目标分辨率 |
| `ClearCache` | bool | `true` | 下载后清除缓存 |
| `MaxRetryAttempts` | int | `3` | 失败最大重试次数 |

---

## 🚀 使用方法

### CLI 模式

```bash
# 使用默认配置
./hanime-dl

# 指定配置文件
./hanime-dl -config /path/to/config.yaml
```

### Web 模式（推荐）

```bash
# 启动 Web 服务器
./hanime-dl -web

# 指定端口
./hanime-dl -web -web-addr :3000

# 访问 http://localhost:8080
```

Web 界面功能：查看下载队列与实时进度、配置下载参数、管理下载任务，响应式设计支持移动端。

### Chrome 浏览器设置

```bash
# Linux
google-chrome --remote-debugging-port=9222

# macOS
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --remote-debugging-port=9222

# Windows
"C:\Program Files\Google\Chrome\Application\chrome.exe" --remote-debugging-port=9222
```

或使用 Docker 桌面环境：

```bash
cd ubuntu-desktop
docker compose up -d
```

### ⚠️ 网络说明

- 本工具抓取与解析依赖 Chrome（需能访问 hanime1.me）。
- **下载直连受限时**，在 `config.yaml` 中配置 `HttpProxy`（如本地代理 `socks5://127.0.0.1:10808`）即可经代理下载。
- 部分 CDN 会强制断开 IPv6 连接，v3.2.1 起下载器已默认强制走 IPv4。

---

## 📁 目录结构

```
hanime-dl-PRO/
├── main.go                    # 主程序入口（CLI + Web 模式）
├── config/                    # 配置管理
├── chrome/                    # Chrome 浏览器管理（跨平台）
├── scraper/                   # 网页抓取（视频信息解析 + 降级下载）
├── downloader/                # 文件下载（断点续传、重试、防盗链、IPv4 拨号）
├── verifier/                  # 文件完整性校验
├── registry/                  # 已完成视频记录
├── failurelog/                # 双日志系统
├── web/                       # Web 服务器与界面
├── ubuntu-desktop/            # Docker 桌面环境
├── config.yaml                # 配置文件
├── go.mod
└── README.md
```

运行后产生的目录：

```
./
├── cache/                    # 解析缓存（下载完成后自动清除）
│   ├── list_<playlistID>.json
│   └── info_<videoID>.json
├── downloads/                # 下载的视频与封面
│   └── [videoID]标题.mp4 / .jpg
├── Completed/                # 已完成视频记录（永久保留）
│   └── <videoID>.json
└── log/                      # 日志目录
    ├── Download-log.txt
    └── Completed-log.txt
```

---

## 🧪 单元测试

```bash
go test ./... -v
```

覆盖 registry、failurelog、scraper、verifier 四个包。

---

## 📚 相关链接

- [hanime-config-generator](https://github.com/mingk326/hanime-config-generator) — 配套的配置生成工具
- [mingjiezxc/hanime-dl](https://github.com/mingjiezxc/hanime-dl) — 原项目

## 许可证

MIT License

## 致谢

- [chromedp](https://github.com/chromedp/chromedp) — Chrome DevTools Protocol 库
- [yaml.v3](https://github.com/go-yaml/yaml) — YAML 解析库