# Hanime Downloader PRO

> 基于 [mingjiezxc/hanime-dl](https://github.com/mingjiezxc/hanime-dl) 的增强版本，新增完成记录、文件校验、失败日志、自动重试、降级下载等可靠性功能。

Hanime 视频下载工具，使用 Chrome DevTools Protocol 进行网页抓取，支持 CLI 和 Web 两种模式。

## 相关项目（配套使用）

本项目需要配合配置文件生成工具一起使用：

- [hanime-config-generator](https://github.com/mingk326/hanime-config-generator) - 在线配置文件生成工具，可视化生成 `config.yaml`，与 `hanime-dl-PRO` 配套使用

## 版本特性

相对原项目，做了以下改进：

### 1. 修复 403/404 卡死问题

原项目遇到 403/404 时会卡住等待 50 分钟超时。通过 HTTP 状态码监听实现 20 秒内快速失败。

| 对比项 | 原项目 | PRO |
|--------|--------|-----|
| 403/404 超时时间 | 50 分钟 | 20 秒 |
| 403/404 重试 | 5 次无效重试 | 立即失败，不重试 |

### 2. 完成记录系统（registry 包）

下载完成后记录视频信息到 `./Completed/` 目录，每个视频一个独立的 `{videoID}.json` 文件。重新运行程序时，已下载的视频**零网络请求**直接跳过。

**三层校验确保记录准确性：**
1. 记录存在且分辨率匹配
2. `os.Stat` 确认 MP4 文件存在（正常记录还需 JPG 存在）
3. 实际文件大小与记录一致

**写入规则：**
- **正常下载**：MP4 + JPG 都下载完成且通过 `verifier` 校验后写入，标记 `demotion: false`
- **降级下载**：只要 MP4 下载完成且通过校验即可写入，JPG 缺失/损坏时标记 `demotion: true`（降级记录）
- 拒绝写入原因记录到 `./log/Completed-log.txt`

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
  ├── Download-log.txt    ← 下载失败日志（解析失败、下载失败、校验失败、重试耗尽、封面图失败）
  └── Completed-log.txt   ← 记录拒绝日志（MP4/JPG 缺失或校验失败导致拒绝写入完成记录）
```

两个日志各有独立 mutex，互不干扰，格式统一为 `[时间] videoID=<ID> reason=<原因>`。

**封面图失败记录：** 只要 JPG 封面图未成功下载/校验，就会写入 `Download-log.txt`，覆盖正常下载和降级下载两种场景。

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

### 7. 降级下载机制（fallback 机制）

当正常下载重试全部失败后，自动触发降级下载，通过 watch 页面点击下载按钮获取下载链接，按分辨率逐级降级尝试。

**降级流程：**
1. 导航到 watch 页面 (`https://hanime1.me/watch?v=XXX`) 建立会话
2. 点击下载按钮（自动处理两种情况：直接可见的按钮 / 隐藏在 `more_horiz` 下拉菜单中）
3. 提取所有分辨率的下载链接和封面图 URL
4. 按优先级逐级降级下载：配置分辨率 → 720p → 480p → 360p → 240p

| 支持的分辨率 | 降级顺序示例（配置 1080p） |
|-------------|------------------------|
| 1080p / 720p / 480p / 360p / 240p | 1080p → 720p → 480p → 360p → 240p |

**降级日志记录：**
- 触发降级时记录：`videoID=XXX reason=触发降级下载: 尝试分辨率 720p`
- 降级成功时记录：`videoID=XXX reason=降级下载成功: 分辨率 720p`
- 降级下载的视频同样走 `verifier` 校验 + `registry` 记录（JPG 缺失时标记为 `demotion: true`）
- 降级时封面图未下载/校验失败同样记录到 `Download-log.txt`

**封面图保障：**
- 降级时优先从下载页面 `img.download-image` 的 `src` 提取新鲜封面图 URL
- watch 页面的 `og:image` meta 标签作为备选
- 用 Go HTTP 客户端直接下载封面图 URL（添加 `Referer` 头绕过防盗链）
- 降级下载的封面图同样经过 `verifier` 校验

### 8. 正常下载与降级下载分离

**正常流程**：视频和封面图通过下载页面的 URL 直接下载，只有视频本身下载失败才触发降级。

| 场景 | 行为 |
|------|------|
| 视频 + 封面都成功 | 正常完成，写入 registry |
| 视频成功，封面失败 | 单独重试封面下载（重新解析 URL），不触发降级 |
| 视频失败 | 触发降级下载，降级成功后下载视频和封面 |

**封面图防盗链处理：**
- 下载器添加 `Referer: https://hanime1.me/` 和 `User-Agent` 头
- 解决 `vdownload.hembed.com` 图片资源 403 防盗链问题

### 9. 六层中断恢复机制

| 层级 | 触发场景 | 恢复方式 |
|------|---------|---------|
| HTTP 断点续传 | 网络中断 | 从 `.tmp` 文件断点继续 |
| 下载器重试 | 408/429/5xx | 间隔 5s/15s 重试 |
| URL 刷新 | 410 URL 过期 | 刷新 URL 后重试 |
| 应用层重试 | 解析/下载/校验失败 | 重新解析 + 下载，退避 10-30s |
| 降级下载 | 重试全部失败 | watch 页面提取链接，分辨率逐级降级 |
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
- 降级下载机制（分辨率降级 + watch 页面提取）
- 封面图防盗链处理（Referer 头）

## 项目结构

```
hanime-dl-PRO/
├── main.go                    # 主程序入口（CLI 模式 + Web 模式）
├── config/
│   └── config.go              # 配置管理
├── chrome/
│   ├── chrome.go              # Chrome 浏览器管理（跨平台接口）
│   ├── chrome_unix.go         # Unix 平台实现
│   └── chrome_windows.go      # Windows 平台实现
├── scraper/
│   └── scraper.go             # 网页抓取（视频信息解析 + 降级下载）
├── downloader/
│   └── downloader.go          # 文件下载（断点续传、重试、防盗链头）
├── verifier/                  # 文件完整性校验
│   ├── verifier.go
│   └── verifier_test.go
├── registry/                  # 已完成视频记录
│   ├── registry.go
│   └── registry_test.go
├── failurelog/                # 双日志系统
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
git clone https://github.com/mingk326/hanime-dl-PRO.git
cd hanime-dl-PRO
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

| 维度 | 原项目 | PRO |
|------|--------|-----|
| 403/404 卡住时间 | 50 分钟 | 20 秒内 |
| 下载失败追踪 | 仅控制台 | `Download-log.txt` 持久化 |
| 文件完整性 | 不校验 | MP4/JPG 双重校验 |
| 文件名安全性 | 仅替换 `/` `\` | 9 个非法字符 + UTF-8 截断 |
| 文件名可识别性 | 仅标题 | `[视频ID]标题` |
| 已下载跳过 | 需重新解析网页 | 零网络请求，瞬间跳过 |
| 失败恢复 | 不重试 | 自动重试 3 次 + URL 刷新 + 降级下载 |
| 分辨率降级 | 不支持 | 1080p → 720p → 480p → 360p → 240p |
| 封面图下载 | 无防盗链处理 | Referer 头绕过防盗链 |
| 正常/降级分离 | 不适用 | 封面失败不误触发降级 |
| 单元测试 | 0 个 | 62 个 |

## 版本历史

| 版本 | 主要内容 |
|------|---------|
| V1 | 基础下载功能 |
| V2 | Registry、Verifier、双日志、自动重试 |
| V3 (PRO) | 降级下载、封面图防盗链修复、正常/降级流程分离 |
| V3.1 (PRO) | 降级下载视频正常记录（Demotion 标记）、JPG 未下载写入 Download-log |

## 相关链接

- [hanime-config-generator](https://github.com/mingk326/hanime-config-generator) - 配套使用的配置生成工具

## 许可证

MIT License

## 致谢

- [mingjiezxc/hanime-dl](https://github.com/mingjiezxc/hanime-dl) - 原项目
- [chromedp](https://github.com/chromedp/chromedp) - Chrome DevTools Protocol 库
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) - YAML 解析库
