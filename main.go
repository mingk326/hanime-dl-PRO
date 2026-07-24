package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hanime-dl/chrome"
	"hanime-dl/config"
	"hanime-dl/downloader"
	"hanime-dl/failurelog"
	"hanime-dl/registry"
	"hanime-dl/scraper"
	"hanime-dl/verifier"
	"hanime-dl/web"
)

//go:embed web/*
var embeddedWebFS embed.FS

var globalConfig *config.Config

// VideoTask 下载任务
type VideoTask struct {
	VideoID string
	ListID  string
}

// ListStatus 播放列表进度
type ListStatus struct {
	Total   int
	Pending int
	Mutex   sync.Mutex
}

var (
	listProgress = make(map[string]*ListStatus)
	progressMu   sync.Mutex
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	webMode := flag.Bool("web", false, "Start web server instead of CLI mode")
	webAddr := flag.String("web-addr", ":8080", "Web server address")
	flag.Parse()

	// Web 模式
	if *webMode {
		cfg, created := config.MustLoadOrCreate(*configPath)
		if created {
			log.Printf("Config not found at %s, generated default config. Please review and edit it as needed.", *configPath)
		}
		cfg.ClearCache = true
		wsURL, launchedLocalChrome, err := chrome.EnsureChromeConnectionDetailed(cfg.ChromeRemoteURL, chrome.StartupCDPTimeout)
		if err != nil {
			log.Printf("Failed to prepare Chrome CDP for web mode: %v", err)
			log.Println("\n=== Chrome Connection Options ===")
			log.Println("Option 1: Start Chrome manually with remote debugging:")
			log.Println("  google-chrome --remote-debugging-port=9222")
			log.Println("Option 2: Use Docker desktop environment:")
			log.Println("  cd ubuntu-desktop && docker compose up -d")
			log.Println("Option 3: Configure remote Chrome in config.yaml:")
			log.Println("  chromeRemoteURL: http://your-chrome-host:9222/json/version")
			log.Fatal("\nUnable to connect to Chrome. Please set up Chrome and try again.")
		}

		if launchedLocalChrome {
			webURL := webInterfaceURL(*webAddr)
			go func() {
				waitForHTTPReady(webURL, 10*time.Second)
				if err := chrome.OpenURL(wsURL, webURL, chrome.OpenPageTimeout); err != nil {
					log.Printf("Failed to open web interface in local Chrome: %v", err)
					return
				}
				log.Printf("Opened web interface in local Chrome: %s", webURL)
			}()
		}

		web.RunWebServer(web.Options{
			Addr:                *webAddr,
			CacheDir:            cfg.CacheDir,
			DownDir:             cfg.DownDir,
			RegistryDir:         cfg.RegistryDir,
			MaxWorkers:          cfg.MaxDownloadWorkers,
			ChromeRemoteURL:     cfg.ChromeRemoteURL,
			WSURL:               wsURL,
			HTTPProxy:           cfg.HttpProxy,
			DirectDownloadFirst: cfg.DirectDownloadFirst,
			VideoResolution:     cfg.VideoResolution,
			ClearCache:          cfg.ClearCache,
			MaxRetryAttempts:    cfg.MaxRetryAttempts,
			SingleCode:          cfg.SingleCode,
			ListCode:            cfg.ListCode,
			EmbedFS:             embeddedWebFS,
		})
		return
	}

	// 加载配置
	globalConfig, created := config.MustLoadOrCreate(*configPath)
	if created {
		log.Printf("Config not found at %s, generated default config. Please review and edit it as needed.", *configPath)
	}
	globalConfig.ClearCache = true

	// 确保目录存在
	os.MkdirAll(globalConfig.CacheDir, 0755)
	os.MkdirAll(globalConfig.DownDir, 0755)

	// 创建已完成视频记录器
	registryDir := globalConfig.RegistryDir
	if registryDir == "" {
		registryDir = "./Completed"
	}
	reg := registry.NewRegistry(registryDir)

	// 设置默认值
	if globalConfig.MaxDownloadWorkers <= 0 {
		globalConfig.MaxDownloadWorkers = 3
	}

	log.Printf("Configuration loaded: RemoteURL=%s, DownDir=%s, CacheDir=%s, Workers=%d",
		globalConfig.ChromeRemoteURL, globalConfig.DownDir, globalConfig.CacheDir, globalConfig.MaxDownloadWorkers)

	// 连接 Chrome
	wsURL, err := chrome.EnsureChromeConnection(globalConfig.ChromeRemoteURL, chrome.StartupCDPTimeout)
	if err != nil {
		log.Printf("Failed to prepare Chrome CDP: %v", err)
		log.Println("\n=== Chrome Connection Options ===")
		log.Println("Option 1: Start Chrome manually with remote debugging:")
		log.Println("  google-chrome --remote-debugging-port=9222")
		log.Println("Option 2: Use Docker desktop environment:")
		log.Println("  cd ubuntu-desktop && docker compose up -d")
		log.Println("Option 3: Configure remote Chrome in config.yaml:")
		log.Println("  chromeRemoteURL: http://your-chrome-host:9222/json/version")
		log.Fatal("\nUnable to connect to Chrome. Please set up Chrome and try again.")
	}

	// 注册清理
	defer func() {
		log.Println("Cleaning up...")
	}()

	// 创建抓取器和下载器
	s := scraper.NewScraper(globalConfig.CacheDir, globalConfig.DownDir, globalConfig.VideoResolution)
	d := downloader.NewDownloader(globalConfig.HttpProxy, globalConfig.DirectDownloadFirst)

	// 下载队列
	downloadQueue := make(chan scraper.VideoMetadata, 100)
	var downloadWg sync.WaitGroup

	// 启动下载 Worker
	for i := 0; i < globalConfig.MaxDownloadWorkers; i++ {
		downloadWg.Add(1)
		go func(workerId int) {
			defer downloadWg.Done()
			for meta := range downloadQueue {
				log.Printf("[Worker %d] Starting download for: %s", workerId, meta.Title)

				currentMeta := meta
				videoSuccess := false

				// 最大重试次数（0=不重试，直接尝试一次）
				maxRetries := globalConfig.MaxRetryAttempts
				if maxRetries < 0 {
					maxRetries = 0
				}

				// 重试循环：每次重试前重新解析获取新的下载 URL
				for attempt := 0; attempt <= maxRetries; attempt++ {
					if attempt > 0 {
						// 退避等待：第1次10s，第2次20s，第3次30s（线性递增）
						backoff := time.Duration(attempt*10) * time.Second
						log.Printf("[Worker %d] Retrying %s (attempt %d/%d) after %s backoff",
							workerId, currentMeta.VideoID, attempt, maxRetries, backoff)
						time.Sleep(backoff)

						// 重新解析视频信息，获取最新的下载 URL（旧 URL 可能已过期）
						newMeta, rerr := s.ResolveVideoInfo(wsURL, currentMeta.VideoID, currentMeta.ListID)
						if rerr != nil {
							log.Printf("[Worker %d] Re-resolve failed for %s: %v", workerId, currentMeta.VideoID, rerr)
							if attempt == maxRetries {
								if logErr := failurelog.Log(currentMeta.VideoID, "重试后仍解析失败: "+rerr.Error()); logErr != nil {
									log.Printf("[Worker %d] Failed to write failure log: %v", workerId, logErr)
								}
							}
							continue
						}
						currentMeta = newMeta
					}

					// === 下载图片 ===
					if currentMeta.ImageURL != "" && currentMeta.ImageFilePath != "" {
						if _, err := os.Stat(currentMeta.ImageFilePath); os.IsNotExist(err) {
							if imgErr := d.DownloadWithRetry(currentMeta.ImageURL, currentMeta.ImageFilePath); imgErr != nil {
								log.Printf("[Worker %d] Image download failed for %s: %v", workerId, currentMeta.VideoID, imgErr)
							}
						}

						// 校验 jpg 图片完整性
						if _, err := os.Stat(currentMeta.ImageFilePath); err == nil {
							if vErr := verifier.Verify(currentMeta.ImageFilePath); vErr != nil {
								log.Printf("[Worker %d] Image verify failed for %s: %v", workerId, currentMeta.VideoID, vErr)
								if verifier.IsCorrupt(vErr) {
									os.Remove(currentMeta.ImageFilePath)
									log.Printf("[Worker %d] Removed corrupt image: %s", workerId, currentMeta.ImageFilePath)
								}
							}
						}
					}

					time.Sleep(3 * time.Second)

					// === 下载视频 ===
					if currentMeta.DataURL != "" && currentMeta.VideoFilePath != "" {
						if _, err := os.Stat(currentMeta.VideoFilePath); os.IsNotExist(err) {
							if dlErr := d.DownloadWithRetry(currentMeta.DataURL, currentMeta.VideoFilePath); dlErr != nil {
								log.Printf("[Worker %d] Video download failed for %s (attempt %d): %v",
									workerId, currentMeta.VideoID, attempt+1, dlErr)
							}
						}

						// 校验 mp4 视频完整性
						if _, err := os.Stat(currentMeta.VideoFilePath); err == nil {
							if vErr := verifier.Verify(currentMeta.VideoFilePath); vErr != nil {
								log.Printf("[Worker %d] Video verify failed for %s (attempt %d): %v",
									workerId, currentMeta.VideoID, attempt+1, vErr)
								if verifier.IsCorrupt(vErr) {
									os.Remove(currentMeta.VideoFilePath)
									log.Printf("[Worker %d] Removed corrupt video: %s", workerId, currentMeta.VideoFilePath)
								}
							}
						}
					}

					// === 检查本次尝试是否成功（MP4 和 JPG 都必须存在）===
				if currentMeta.DataURL != "" && currentMeta.VideoFilePath != "" {
					if _, err := os.Stat(currentMeta.VideoFilePath); err == nil {
						// MP4 存在，再检查 JPG
						if currentMeta.ImageFilePath != "" {
							if _, imgErr := os.Stat(currentMeta.ImageFilePath); imgErr == nil {
								videoSuccess = true
							} else {
								log.Printf("[Worker %d] MP4 OK but JPG missing for %s, will retry",
									workerId, currentMeta.VideoID)
							}
						} else {
							// ImageFilePath 为空（视频无封面），仅检查 MP4
							videoSuccess = true
						}
					}
				}

					if videoSuccess {
						log.Printf("[Worker %d] Video downloaded and verified OK: %s (attempt %d)",
							workerId, currentMeta.VideoID, attempt+1)
						break
					}

					log.Printf("[Worker %d] Attempt %d/%d failed for %s",
						workerId, attempt+1, maxRetries+1, currentMeta.VideoID)
				}

				// 所有重试结束后仍未成功，尝试降级下载
			if !videoSuccess {
				log.Printf("[Worker %d] All retries exhausted, attempting fallback download for %s", workerId, currentMeta.VideoID)

				fallbackLinks, ferr := s.FallbackResolveDownloadURLs(wsURL, currentMeta.VideoID)
				if ferr != nil {
					log.Printf("[Worker %d] Fallback resolve failed for %s: %v", workerId, currentMeta.VideoID, ferr)
					if logErr := failurelog.Log(currentMeta.VideoID,
						fmt.Sprintf("降级下载失败: %v", ferr)); logErr != nil {
						log.Printf("[Worker %d] Failed to write failure log: %v", workerId, logErr)
					}
				} else {
					// 按优先级逐个尝试每个分辨率的下载链接
					for _, link := range fallbackLinks {
						// 记录降级日志：视频ID + 触发降级 + 降级到哪个分辨率
						if logErr := failurelog.Log(currentMeta.VideoID,
							fmt.Sprintf("触发降级下载: 尝试分辨率 %s", link.Resolution)); logErr != nil {
							log.Printf("[Worker %d] Failed to write fallback log: %v", workerId, logErr)
						}

						log.Printf("[Worker %d] Fallback: trying %s for %s", workerId, link.Resolution, currentMeta.VideoID)

						// 删除可能存在的残留文件
						os.Remove(currentMeta.VideoFilePath)

						// 下载视频
						if dlErr := d.DownloadWithRetry(link.URL, currentMeta.VideoFilePath); dlErr != nil {
							log.Printf("[Worker %d] Fallback download failed at %s for %s: %v",
								workerId, link.Resolution, currentMeta.VideoID, dlErr)
							os.Remove(currentMeta.VideoFilePath)
							continue
						}

						// 校验 MP4
						if vErr := verifier.Verify(currentMeta.VideoFilePath); vErr != nil {
							log.Printf("[Worker %d] Fallback verify failed at %s for %s: %v",
								workerId, link.Resolution, currentMeta.VideoID, vErr)
							if verifier.IsCorrupt(vErr) {
								os.Remove(currentMeta.VideoFilePath)
							}
							continue
						}

						// 下载成功，记录降级成功日志
						log.Printf("[Worker %d] Fallback download succeeded at %s for %s",
							workerId, link.Resolution, currentMeta.VideoID)
						failurelog.Log(currentMeta.VideoID,
							fmt.Sprintf("降级下载成功: 分辨率 %s", link.Resolution))

						// 更新 metadata 中的下载 URL
						currentMeta.DataURL = link.URL

						// 下载封面图（如果还没有）
						if currentMeta.ImageURL != "" && currentMeta.ImageFilePath != "" {
							if _, err := os.Stat(currentMeta.ImageFilePath); os.IsNotExist(err) {
								if imgErr := d.DownloadWithRetry(currentMeta.ImageURL, currentMeta.ImageFilePath); imgErr != nil {
									log.Printf("[Worker %d] Fallback image download failed for %s: %v", workerId, currentMeta.VideoID, imgErr)
								}
							}
							if _, err := os.Stat(currentMeta.ImageFilePath); err == nil {
								if vErr := verifier.Verify(currentMeta.ImageFilePath); vErr != nil {
									log.Printf("[Worker %d] Fallback image verify failed: %v", workerId, vErr)
									if verifier.IsCorrupt(vErr) {
										os.Remove(currentMeta.ImageFilePath)
									}
								}
							}
						}

						videoSuccess = true
						break
					}
				}

				// 如果降级也失败
				if !videoSuccess {
					if logErr := failurelog.Log(currentMeta.VideoID,
						fmt.Sprintf("重试 %d 次后仍下载失败（含降级下载）", maxRetries)); logErr != nil {
						log.Printf("[Worker %d] Failed to write failure log: %v", workerId, logErr)
					}
				}
			}

				if videoSuccess && globalConfig.ClearCache {
				cacheFile := filepath.Join(globalConfig.CacheDir, fmt.Sprintf("info_%s.json", currentMeta.VideoID))
				os.Remove(cacheFile)
				log.Printf("[Worker %d] Cleared cache for %s", workerId, currentMeta.VideoID)
			}

			// 下载成功后写入 registry 记录（严格校验 MP4 + JPG 都存在且通过验证）
			if videoSuccess {
				canRecord := true

				// 最终确认 MP4 文件存在且通过校验
				if _, err := os.Stat(currentMeta.VideoFilePath); err != nil {
					canRecord = false
					reason := fmt.Sprintf("拒绝写入完成记录: MP4 文件不存在 %s", currentMeta.VideoFilePath)
					log.Printf("[Worker %d] [Registry] %s", workerId, reason)
					failurelog.LogReject(currentMeta.VideoID, reason)
				} else if vErr := verifier.Verify(currentMeta.VideoFilePath); vErr != nil {
					canRecord = false
					reason := fmt.Sprintf("拒绝写入完成记录: MP4 校验失败 %v", vErr)
					log.Printf("[Worker %d] [Registry] %s", workerId, reason)
					failurelog.LogReject(currentMeta.VideoID, reason)
				}

				// 最终确认 JPG 文件存在且通过校验
				if canRecord && currentMeta.ImageFilePath != "" {
					if _, err := os.Stat(currentMeta.ImageFilePath); err != nil {
						canRecord = false
						reason := fmt.Sprintf("拒绝写入完成记录: JPG 文件不存在 %s", currentMeta.ImageFilePath)
						log.Printf("[Worker %d] [Registry] %s", workerId, reason)
						failurelog.LogReject(currentMeta.VideoID, reason)
					} else if vErr := verifier.Verify(currentMeta.ImageFilePath); vErr != nil {
						canRecord = false
						reason := fmt.Sprintf("拒绝写入完成记录: JPG 校验失败 %v", vErr)
						log.Printf("[Worker %d] [Registry] %s", workerId, reason)
						failurelog.LogReject(currentMeta.VideoID, reason)
					}
				}

				if canRecord {
					if regErr := reg.Record(currentMeta.VideoID, currentMeta.Title,
						currentMeta.VideoFilePath, currentMeta.ImageFilePath,
						globalConfig.VideoResolution); regErr != nil {
						reason := fmt.Sprintf("拒绝写入完成记录: %v", regErr)
						log.Printf("[Worker %d] [Registry] %s", workerId, reason)
						failurelog.LogReject(currentMeta.VideoID, reason)
					} else {
						log.Printf("[Worker %d] Recorded to registry: %s", workerId, currentMeta.VideoID)
					}
				}
			}

				// 更新播放列表进度
				if videoSuccess && currentMeta.ListID != "" {
					progressMu.Lock()
					if status, ok := listProgress[currentMeta.ListID]; ok {
						status.Mutex.Lock()
						status.Pending--
						pending := status.Pending
						status.Mutex.Unlock()

						if pending == 0 && globalConfig.ClearCache {
							cacheFile := filepath.Join(globalConfig.CacheDir, fmt.Sprintf("list_%s.json", currentMeta.ListID))
							os.Remove(cacheFile)
							log.Printf("[Worker %d] Cleared list cache for %s", workerId, currentMeta.ListID)
						}
					}
					progressMu.Unlock()
				}
			}
		}(i)
	}

	// 任务队列
	taskQueue := make(chan VideoTask, 1000)

	// 启动发现协程
	go func() {
		defer close(taskQueue)

		taskMap := make(map[string]bool)
		addTask := func(task VideoTask) {
			if !taskMap[task.VideoID] {
				taskQueue <- task
				taskMap[task.VideoID] = true
			}
		}

		// 1. 单个视频
		for _, id := range globalConfig.SingleCode {
			addTask(VideoTask{VideoID: id, ListID: ""})
		}

		// 2. 区分缓存和网络的播放列表
		var cachedLists, networkLists []string
		for _, listID := range globalConfig.ListCode {
			cacheFile := filepath.Join(globalConfig.CacheDir, fmt.Sprintf("list_%s.json", listID))
			if _, err := os.Stat(cacheFile); err == nil {
				cachedLists = append(cachedLists, listID)
			} else {
				networkLists = append(networkLists, listID)
			}
		}

		processList := func(listID string) {
			log.Printf("Fetching playlist for ID: %s", listID)
			list, err := s.GetPlaylist(wsURL, listID)
			if err != nil {
				log.Printf("Failed to get playlist %s: %v", listID, err)
				return
			}
			log.Printf("Playlist %s acquired: %d videos found", listID, len(list))

			progressMu.Lock()
			if _, ok := listProgress[listID]; !ok {
				listProgress[listID] = &ListStatus{
					Total:   len(list),
					Pending: len(list),
				}
			}
			progressMu.Unlock()

			for _, vid := range list {
				taskQueue <- VideoTask{VideoID: vid, ListID: listID}
				taskMap[vid] = true
			}
		}

		// 3. 处理缓存的播放列表
		for _, listID := range cachedLists {
			processList(listID)
		}

		// 4. 扫描缓存中待处理的播放列表
		listFiles, _ := filepath.Glob(filepath.Join(globalConfig.CacheDir, "list_*.json"))
		for _, f := range listFiles {
			base := filepath.Base(f)
			if filepath.Ext(base) == ".json" && len(base) > 5 {
				listID := base[5 : len(base)-5]
				alreadyProcessed := false
				for _, cfgID := range globalConfig.ListCode {
					if cfgID == listID {
						alreadyProcessed = true
						break
					}
				}
				if !alreadyProcessed {
					log.Printf("Found cached playlist %s, resuming...", listID)
					processList(listID)
				}
			}
		}

		// 5. 扫描缓存中待处理的视频
		infoFiles, _ := filepath.Glob(filepath.Join(globalConfig.CacheDir, "info_*.json"))
		for _, f := range infoFiles {
			base := filepath.Base(f)
			if filepath.Ext(base) == ".json" && len(base) > 5 {
				vid := base[5 : len(base)-5]
				if !taskMap[vid] {
					log.Printf("Found cached video info for %s, resuming...", vid)
					addTask(VideoTask{VideoID: vid, ListID: ""})
				}
			}
		}

		// 6. 处理网络的播放列表
		for _, listID := range networkLists {
			processList(listID)
		}

		log.Println("Discovery completed, closing task queue.")
	}()

	// 主线程：解析视频信息
	for task := range taskQueue {
		// === Registry 快速检查：已下载的视频直接跳过，零网络请求 ===
		if _, _, ok, needsRemove := reg.IsCompleted(task.VideoID, globalConfig.VideoResolution); ok {
			log.Printf("[Skip] %s already completed (registry hit), skipping", task.VideoID)
			if task.ListID != "" {
				progressMu.Lock()
				if status, ok := listProgress[task.ListID]; ok {
					status.Mutex.Lock()
					status.Pending--
					status.Mutex.Unlock()
				}
				progressMu.Unlock()
			}
			continue
		} else if needsRemove {
			reg.Remove(task.VideoID)
			log.Printf("[Registry] Removed stale record for %s", task.VideoID)
		}

		meta, err := s.ResolveVideoInfo(wsURL, task.VideoID, task.ListID)
		if err != nil {
			log.Printf("Skipping %s due to resolution failure: %v", task.VideoID, err)
			// 记录到 ./log 失败日志文件
			if logErr := failurelog.Log(task.VideoID, "解析视频信息失败: "+err.Error()); logErr != nil {
				log.Printf("Failed to write failure log: %v", logErr)
			}

			if task.ListID != "" {
				progressMu.Lock()
				if status, ok := listProgress[task.ListID]; ok {
					status.Mutex.Lock()
					status.Pending--
					status.Mutex.Unlock()
				}
				progressMu.Unlock()
			}
			continue
		}

		downloadQueue <- meta
	}

	close(downloadQueue)
	downloadWg.Wait()

	log.Println("All tasks completed.")
}

// jsonMarshal 辅助函数
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func probeHTTPReady(target string) bool {
	u := strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://")
	hostPort := u
	if slash := strings.Index(hostPort, "/"); slash >= 0 {
		hostPort = hostPort[:slash]
	}
	conn, err := net.DialTimeout("tcp", hostPort, 500*time.Millisecond)
	if err == nil {
		conn.Close()
	}
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	resp, err := client.Get(target)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return true
}

func waitForHTTPReady(target string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if probeHTTPReady(target) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func webInterfaceURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if len(addr) > 0 && addr[0] == ':' {
			return "http://localhost" + addr
		}
		return "http://" + addr
	}

	switch host {
	case "", "0.0.0.0", "::":
		host = "localhost"
	}

	return fmt.Sprintf("http://%s:%s", host, port)
}
