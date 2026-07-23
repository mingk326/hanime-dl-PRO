// Package failurelog 负责将视频下载失败和记录拒绝信息分别写入两个独立日志文件。
//
// 下载失败日志：./log/Download-log.txt
//   记录下载失败、解析失败、校验失败等场景。
//
// 记录拒绝日志：./log/Completed-log.txt
//   记录 registry 拒绝写入完成记录的场景（MP4/JPG 缺失或空文件）。
//
// 两个日志格式相同：[时间] videoID=<视频ID> reason=<原因>
// 文件不存在时自动创建，已存在则追加，多协程并发写入安全。
package failurelog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	logDir        = "./log"
	downloadPath  = "./log/Download-log.txt"
	completedPath = "./log/Completed-log.txt"
)

var (
	downloadMu sync.Mutex
	rejectMu   sync.Mutex
)

// ensureLogDir 确保日志目录存在
func ensureLogDir() {
	os.MkdirAll(logDir, 0755)
}

// Log 记录一条下载失败日志：时间 + 视频ID + 失败原因。
// 写入 ./log/Download-log.txt。
// 并发安全（内部加锁）。写入失败不影响主流程，仅通过返回值告知调用方。
func Log(videoID, reason string) error {
	ensureLogDir()

	downloadMu.Lock()
	defer downloadMu.Unlock()

	return writeLog(downloadPath, videoID, reason)
}

// LogReject 记录一条记录拒绝日志：时间 + 视频ID + 拒绝原因。
// 写入 ./log/Completed-log.txt。
// 用于 registry 因 MP4/JPG 缺失或校验失败而拒绝写入完成记录的场景。
// 并发安全（内部加锁）。
func LogReject(videoID, reason string) error {
	ensureLogDir()

	rejectMu.Lock()
	defer rejectMu.Unlock()

	return writeLog(completedPath, videoID, reason)
}

// writeLog 将一条日志追加写入指定文件。
// 调用者必须持有对应的 mutex。
func writeLog(filePath, videoID, reason string) error {
	// 视频ID 和原因中若包含换行会破坏单行格式，统一替换为空格
	videoID = sanitize(videoID)
	reason = sanitize(reason)
	line := fmt.Sprintf("[%s] videoID=%s reason=%s\n",
		time.Now().Format("2006-01-02 15:04:05"), videoID, reason)

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", filePath, err)
	}
	defer f.Close()

	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write log %s: %w", filePath, err)
	}
	return nil
}

// LogPaths 返回两个日志文件的绝对路径，方便调用方打印信息。
func LogPaths() (downloadLog, completedLog string) {
	ensureLogDir()
	abs1, _ := filepath.Abs(downloadPath)
	abs2, _ := filepath.Abs(completedPath)
	return abs1, abs2
}

// sanitize 移除字符串中的换行符与制表符，避免破坏日志单行格式
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\r' || c == '\t' {
			out = append(out, ' ')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
