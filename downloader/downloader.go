package downloader

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	MaxRetries    = 5
	RetryInterval = 5 * time.Second
)

// HTTPStatusError HTTP 状态错误
type HTTPStatusError struct {
	StatusCode int
	Status     string
	URL        string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "http status error"
	}
	if e.URL == "" {
		return fmt.Sprintf("server returned status %s", e.Status)
	}
	return fmt.Sprintf("server returned status %s for %s", e.Status, e.URL)
}

// IsHTTPStatusError 判断错误是否为 HTTP 状态码错误，并返回状态码。
// 上层（web_server）用此函数区分可重试与不可重试错误，
// 决定是否需要刷新下载 URL 后重试。
func IsHTTPStatusError(err error) (int, bool) {
	var e *HTTPStatusError
	if errors.As(err, &e) {
		return e.StatusCode, true
	}
	return 0, false
}

// isRetriableStatus 判断 HTTP 状态码是否值得重试。
// 403/404/410 等永久性或需要刷新 URL 的错误不重试（立即返回，由上层处理）；
// 408/429/5xx 等临时性错误重试。
func isRetriableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout,        // 408
		http.StatusTooManyRequests,        // 429
		http.StatusInternalServerError,   // 500
		http.StatusBadGateway,            // 502
		http.StatusServiceUnavailable,     // 503
		http.StatusGatewayTimeout:         // 504
		return true
	}
	return false
}

// ProgressCallback 下载进度回调函数
type ProgressCallback func(downloaded, total int64)

// ProgressWriter 进度追踪器
type ProgressWriter struct {
	Total      int64
	Downloaded int64
	FileName   string
	Callback   ProgressCallback
	lastCB     time.Time
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.Downloaded += int64(n)
	// 限流回调，避免频繁加锁（最多每 500ms 一次）
	if pw.Callback != nil {
		now := time.Now()
		if now.Sub(pw.lastCB) >= 500*time.Millisecond {
			pw.lastCB = now
			pw.Callback(pw.Downloaded, pw.Total)
		}
	}
	return n, nil
}

// Downloader 文件下载器
type Downloader struct {
	proxyURL           string
	directDownloadFirst bool
}

// NewDownloader 创建新的下载器
func NewDownloader(proxyURL string, directFirst bool) *Downloader {
	return &Downloader{
		proxyURL:            proxyURL,
		directDownloadFirst: directFirst,
	}
}

// DownloadFile 下载文件（支持断点续传）
func (d *Downloader) DownloadFile(urlStr, filePath string) error {
	return d.downloadFile(urlStr, filePath, nil)
}

// DownloadFileCB 下载文件并回调进度（支持断点续传）
func (d *Downloader) DownloadFileCB(urlStr, filePath string, cb ProgressCallback) error {
	return d.downloadFile(urlStr, filePath, cb)
}

func (d *Downloader) downloadFile(urlStr, filePath string, cb ProgressCallback) error {
	tempFilePath := filePath + ".tmp"

	// 确保目标文件的父目录存在，否则创建临时文件会失败
	if dir := filepath.Dir(tempFilePath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// 检查是否已存在临时文件
	var startOffset int64 = 0
	fileInfo, err := os.Stat(tempFilePath)
	var file *os.File
	if err == nil {
		startOffset = fileInfo.Size()
		file, err = os.OpenFile(tempFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		file, err = os.Create(tempFilePath)
	}

	if err != nil {
		return fmt.Errorf("failed to open/create temp file %s: %w", tempFilePath, err)
	}
	defer file.Close()

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if startOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startOffset))
		log.Printf("Resuming download for %s from byte %d", filePath, startOffset)
	}

	// 配置代理
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if d.proxyURL != "" {
		pUrl, err := url.Parse(d.proxyURL)
		if err == nil {
			tr.Proxy = http.ProxyURL(pUrl)
			log.Printf("Using proxy: %s", d.proxyURL)
		} else {
			log.Printf("Invalid proxy URL: %s, ignoring.", d.proxyURL)
		}
	}

	client := http.Client{
		Transport: tr,
		Timeout:   360000 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: urlStr}
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: urlStr}
	}

	if startOffset > 0 && resp.StatusCode == http.StatusOK {
		log.Printf("Server does not support resume (got 200 OK), restarting download.")
		file.Truncate(0)
		file.Seek(0, 0)
		startOffset = 0
	}

	// 进度追踪
	contentLength := resp.ContentLength
	totalSize := contentLength + startOffset
	if contentLength == -1 {
		totalSize = -1
	}

	pw := &ProgressWriter{
		Total:      totalSize,
		Downloaded: startOffset,
		FileName:   filepath.Base(filePath),
		Callback:   cb,
	}

	// 进度打印协程
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	done := make(chan bool)

	go func() {
		for {
			select {
			case <-ticker.C:
				var progressStr string
				if pw.Total > 0 {
					percent := float64(pw.Downloaded) / float64(pw.Total) * 100
					progressStr = fmt.Sprintf("%.2f%% (%d/%d bytes)", percent, pw.Downloaded, pw.Total)
				} else {
					progressStr = fmt.Sprintf("%d bytes (Total unknown)", pw.Downloaded)
				}
				log.Printf("[%s] Download Progress: %s", pw.FileName, progressStr)
			case <-done:
				return
			}
		}
	}()

	// 下载
	mw := io.MultiWriter(file, pw)
	n, err := io.Copy(mw, resp.Body)
	done <- true

	if err != nil {
		return fmt.Errorf("failed to write to file: %w", err)
	}

	file.Close()
	// 下载完成，回调最终进度
	if cb != nil {
		cb(pw.Downloaded, pw.Total)
	}
	log.Printf("[%s] Downloaded %d bytes (Total: %d)", pw.FileName, n, pw.Downloaded)

	if err := os.Rename(tempFilePath, filePath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// DownloadWithRetry 带重试的下载
func (d *Downloader) DownloadWithRetry(urlStr, filePath string) error {
	return d.downloadWithRetry(urlStr, filePath, nil)
}

// DownloadWithRetryCB 带重试的下载并回调进度
func (d *Downloader) DownloadWithRetryCB(urlStr, filePath string, cb ProgressCallback) error {
	return d.downloadWithRetry(urlStr, filePath, cb)
}

func (d *Downloader) downloadWithRetry(urlStr, filePath string, cb ProgressCallback) error {
	var lastErr error
	for i := 0; i < MaxRetries; i++ {
		err := d.downloadFile(urlStr, filePath, cb)
		if err == nil {
			log.Printf("Successfully downloaded %s to %s", urlStr, filePath)
			return nil
		}

		lastErr = err
		log.Printf("Failed to download %s (attempt %d/%d): %v", urlStr, i+1, MaxRetries, err)

		// 区分可重试与不可重试的 HTTP 错误（用类型断言替代脆弱的字符串匹配）
		if code, ok := IsHTTPStatusError(err); ok {
			// 416 Range Not Satisfiable：临时文件状态与服务器不一致，
			// 清除临时文件后重试（可能服务器侧资源已变更或断点信息过期）
			if code == http.StatusRequestedRangeNotSatisfiable {
				tempFilePath := filePath + ".tmp"
				if rmErr := os.Remove(tempFilePath); rmErr == nil {
					log.Printf("Deleted temp file after HTTP 416, restarting download")
				}
				// 416 可重试，继续下一轮
			} else if !isRetriableStatus(code) {
				// 403/404/410 等永久性或需要刷新 URL 的错误：立即返回，不再重试。
				// 上层（web_server）可根据状态码决定是否刷新 URL 后重试：
				//   - 410 Gone：下载 URL 已过期，刷新后通常可恢复
				//   - 403/404：视频不存在或被封禁，刷新也无效，直接标记失败
				log.Printf("HTTP %d is not retriable, giving up download", code)
				return err
			}
			// 408/429/5xx 等临时性错误：继续重试
		}

		if i < MaxRetries-1 {
			sleep := RetryInterval
			// 429 Too Many Requests：退避更久，避免触发更严格的限流
			if code, ok := IsHTTPStatusError(err); ok && code == http.StatusTooManyRequests {
				sleep = RetryInterval * 3
				log.Printf("HTTP 429 received, backing off for %s", sleep)
			}
			time.Sleep(sleep)
		}
	}

	return fmt.Errorf("failed to download %s after %d attempts, last error: %w", urlStr, MaxRetries, lastErr)
}
