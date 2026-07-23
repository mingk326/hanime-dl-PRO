# Hanime Downloader V2

> 基于 [mingjiezxc/hanime-dl](https://github.com/mingjiezxc/hanime-dl) 的增强版本，新增完成记录、文件校验、失败日志、自动重试等可靠性功能。

Hanime 视频下载工具，使用 Chrome DevTools Protocol 进行网页抓取，支持 CLI 和 Web 两种模式。

## V2 新增特性

相对原项目，V2 做了以下改进：

### 1. 修复 403/404 卡死问题

原项目遇到 403/404 时会卡住等待 50 分钟超时。V2 通过 HTTP 状态码监听实现 20 秒内快速失败。

| 对比项 | 原项目 | V2 |
|--------|--------|-----|
| 403/404 超时时间 | 50 分钟 | 20 秒 |
| 403/404 重试 | 5 次无效重试 | 立即失败，不重试 |

### 2. 完成记录系统（registry 包）

下载完成后记录视频信息到 `./Completed/` 目录，每个视频一个独立的 `{videoID}.json` 文件。重新运行程序时，已下载的视频**零网络请求**直接跳过。

**三层校验确保记录准确性：**
1. 记录存在且分辨率匹配
2. `os.Stat` 确认 MP4 和 JPG 文件都存在
3. 实际文件大小与记录一致

**严格写入条件：** 只有 MP4 + JPG 都下载完成且通过 `verifier` 校验后才写入记录。任一检查失败则拒绝写入，并记录原因到 `./log/Completed-log.txt`。

### 3. 文件完整性校验（verifier 包）

下载完成后自动校验文件完整性，防止保存 HTML 错误页或损坏文件。

| 格式 | 校验规则 | 检测问题 |
|------|---------|---------|
| MP4 | 大小 > 10KB + 偏移 4-7 字节为 `ftyp` | HTML 错误页、空文件、损坏文件 |
| JPG | 大小 > 100B + 前 2 字节为 `FF D8` | HTML 错误页、空文件、损坏文件 |

校验失败且判定为损坏时自动删除文件，触发重新下载。

### 4. 双日志系统（failurelog 包）

```
./log/
  ├── Download-log.txt    ← 下载失败日志（解析失败、下载失败、校验失败、重试耗尽）
  └── Completed-log.txt   ← 记录拒绝日志（MP4/JPG 缺失或校验失败导致拒绝写入完成记录）
```

两个日志各有独立 mutex，互不干扰，格式统一为 `[时间] videoID=<ID> reason=<原因>`。

### 5. 自动重试机制

下载失败后自动重试，每次重试前重新解析视频信息获取新的下载 URL（旧 URL 可能已过期）。

- 配置项 `MaxRetryAttempts`（默认 3）
- 线性退避：第 1 次 10s，第 2 次 20s，第 3 次 30s
- 三种失败都触发重试：解析失败、下载失败、校验失败
- 中间失败不写日志，只有最终失败才记录到 `Download-log.txt`

### 6. Windows 文件名安全化

- 替换 9 个 Windows 非法字符（`< > : " / \ | ? *`）及控制字符为 `_`
- UTF-8 安全截断到 200 字节（不会在多字节字符中间截断）
- 去除末尾空格和点号
- 文件名添加 `[视频ID]` 前缀，如 `[407238][CEO NEET (ニート社長)] 标题.mp4`

### 7. 六层中断恢复机制

| 层级 | 触发场景 | 恢复方式 |
|------|---------|---------|
| HTTP 断点续传 | 网络中断 | 从 `.tmp` 文件断点继续 |
| 下载器重试 | 408/429/5xx | 间隔 5s/15s 重试 |
| URL 刷新 | 410 URL 过期 | 刷新 URL 后重试 |
| 应用层重试 | 解析/下载/校验失败 | 重新解析 + 下载，退避 10-30s |
| 程序重启恢复 | 程序崩溃 | 扫描缓存文件恢复任务 |
| 文件校验修复 | 文件损坏 | 删除损坏文件后重下 |

## 功能特性

- 单个视频下载 / 播放列表批量下载
- CLI 模式 / Web 管理界面
- 断点续传（HTTP Range）
- HTTP 代理配置
- 多分辨率选择
- 多线程并发下载
- 下载进度缓存与中断恢复
- 已完成视频记录与快速跳过
- 文件完整性校验
- 双日志系统（下载失败 + 记录拒绝）
- 自动重试与 URL 刷新

## 项目结构

```
hanime-dl-v2/
├── main.go                    # 主程序入口（CLI 模式）
├── config/
│   └── config.go              # 配置管理
├── chrome/
│   ├── chrome.go              # Chrome 浏览器管理（跨平台接口）
│   ├── chrome_unix.go         # Unix 平台实现
│   └── chrome_windows.go      # Windows 平台实现
├── scraper/
│   └── scraper.go             # 网页抓取（视频信息解析）
├── downloader/
│   └── downloader.go          # 文件下载（断点续传、重试）
├── verifier/                  # [新增] 文件完整性校验
│   ├── verifier.go
│   └── verifier_test.go
├── registry/                  # [新增] 已完成视频记录
│   ├── registry.go
│   └── registry_test.go
├── failurelog/                # [增强] 双日志系统
│   ├── failurelog.go
│   └── failurelog_test.go
├── web/
│   ├── web_server.go          # Web 服务器
│   └── index.html             # Web 界面
├── ubuntu-desktop/            # Docker 桌面环境
│   ├── Dockerfile
│   └── docker-compose.yaml
├── config.yaml                # 配置文件
├── go.mod
└── README.md
```

## 安装

### 前置要求

- Go 1.19+
- Google Chrome 或 Chromium 浏览器

### 编译

```bash
git clone https://github.com/mingk326/hanime-dl-v2.git
cd hanime-dl-v2
go mod download
go build -o hanime-dl .
```

Windows 交叉编译：

```bash
$env:GOOS='windows'; $env:GOARCH='amd64'
go build -ldflags="-s -w" -o hanime-dl-windows-amd64.exe .
```

## 配置

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

# 视频分辨率
VideoResolution: 1080p

# 最大并发下载线程数
MaxDownloadWorkers: 3

# 单个视频失败后最大重试次数（0=不重试）
MaxRetryAttempts: 3

# 是否优先尝试直接下载
DirectDownloadFirst: true

# 下载后清除缓存
ClearCache: true

# 播放列表 ID 列表
ListCode:
  - playlist-id-1

# 单个视频 ID 列表
SingleCode:
  - video-id-1
```

### 配置项说明

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `chromeRemoteURL` | string | - | Chrome DevTools WebSocket URL |
| `CacheDir` | string | `./cache` | 缓存目录路径 |
| `DownDir` | string | `./downloads` | 视频下载目录路径 |
| `RegistryDir` | string | `./Completed` | 已完成视频记录目录 |
| `VideoResolution` | string | `1080p` | 目标视频分辨率 |
| `MaxDownloadWorkers` | int | `3` | 并发下载线程数 |
| `MaxRetryAttempts` | int | `3` | 单个视频失败后重试次数 |
| `DirectDownloadFirst` | bool | `true` | 是否优先尝试直接下载 |
| `ClearCache` | bool | `true` | 下载后是否清除缓存 |
| `ListCode` | []string | - | 播放列表 ID 列表 |
| `SingleCode` | []string | - | 单个视频 ID 列表 |

## 使用方法

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

Web 界面功能：
- 查看下载队列和实时进度
- 配置下载参数
- 管理视频下载任务
- 响应式设计，支持移动端访问

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

## 目录结构说明

运行后会产生以下目录：

```
./
├── cache/                    # 解析缓存（下载完成后自动清除）
│   ├── list_<playlistID>.json
│   └── info_<videoID>.json
├── downloads/                # 下载的视频和封面
│   └── [videoID]标题.mp4 / .jpg
├── Completed/                # 已完成视频记录（永久保留）
│   └── <videoID>.json
└── log/                      # 日志目录
    ├── Download-log.txt      # 下载失败日志
    └── Completed-log.txt     # 记录拒绝日志
```

## 单元测试

```bash
go test ./... -v
```

共 62 个测试用例，覆盖 registry、failurelog、scraper、verifier 四个包。

## 与原项目的对比

| 维度 | 原项目 | V2 |
|------|--------|-----|
| 403/404 卡住时间 | 50 分钟 | 20 秒内 |
| 下载失败追踪 | 仅控制台 | `Download-log.txt` 持久化 |
| 文件完整性 | 不校验 | MP4/JPG 双重校验 |
| 文件名安全性 | 仅替换 `/` `\` | 9 个非法字符 + UTF-8 截断 |
| 文件名可识别性 | 仅标题 | `[视频ID]标题` |
| 已下载跳过 | 需重新解析网页 | 零网络请求，瞬间跳过 |
| 失败恢复 | 不重试 | 自动重试 3 次 + URL 刷新 |
| 单元测试 | 0 个 | 62 个 |

## 许可证

MIT License

## 致谢

- [mingjiezxc/hanime-dl](https://github.com/mingjiezxc/hanime-dl) - 原项目
- [chromedp](https://github.com/chromedp/chromedp) - Chrome DevTools Protocol 库
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) - YAML 解析库
