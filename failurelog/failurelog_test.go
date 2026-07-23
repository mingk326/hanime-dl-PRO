package failurelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearLog 清空两个日志文件的内容（不删目录，避免 Windows 文件锁残留）
func clearLog(t *testing.T) {
	t.Helper()
	os.MkdirAll("./log", 0755)
	os.WriteFile("./log/Download-log.txt", []byte{}, 0644)
	os.WriteFile("./log/Completed-log.txt", []byte{}, 0644)
}

// removeLogDir 测试结束后删除整个日志目录
func removeLogDir() {
	os.RemoveAll("./log")
}

// TestLog 验证 Log 函数能正确写入 ./log/Download-log.txt 文件
func TestLog(t *testing.T) {
	clearLog(t)
	defer removeLogDir()

	cases := []struct {
		videoID string
		reason  string
	}{
		{"test-video-001", "视频下载失败: HTTP 403 Forbidden"},
		{"test-video-002", "解析视频信息失败: page returned HTTP 404"},
		{"test-video-003", "视频下载失败: connection reset"},
	}

	for _, c := range cases {
		if err := Log(c.videoID, c.reason); err != nil {
			t.Fatalf("Log(%s) failed: %v", c.videoID, err)
		}
	}

	data, err := os.ReadFile("./log/Download-log.txt")
	if err != nil {
		t.Fatalf("ReadFile(Download-log.txt) failed: %v", err)
	}

	content := string(data)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("expected %d lines, got %d (content: %q)", len(cases), len(lines), content)
	}

	for i, c := range cases {
		if !strings.Contains(lines[i], "videoID="+c.videoID) {
			t.Errorf("line %d missing videoID=%s: %s", i, c.videoID, lines[i])
		}
		if !strings.Contains(lines[i], "reason="+c.reason) {
			t.Errorf("line %d missing reason=%s: %s", i, c.reason, lines[i])
		}
	}
}

// TestLogSanitize 验证包含换行符的 reason 不会破坏单行格式
func TestLogSanitize(t *testing.T) {
	clearLog(t)
	defer removeLogDir()

	malicious := "reason\nwith\nnewlines"
	if err := Log("vid", malicious); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	data, err := os.ReadFile("./log/Download-log.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	content := strings.TrimRight(string(data), "\n")
	if strings.Contains(content, "\n") {
		t.Errorf("Download-log.txt contains unexpected newlines: %q", content)
	}
}

// TestLogReject 验证 LogReject 写入 ./log/Completed-log.txt
func TestLogReject(t *testing.T) {
	clearLog(t)
	defer removeLogDir()

	cases := []struct {
		videoID string
		reason  string
	}{
		{"vid-001", "MP4 文件不存在，拒绝写入记录"},
		{"vid-002", "JPG 校验失败: magic mismatch"},
		{"vid-003", "MP4 文件大小为 0，拒绝写入记录"},
	}

	for _, c := range cases {
		if err := LogReject(c.videoID, c.reason); err != nil {
			t.Fatalf("LogReject(%s) failed: %v", c.videoID, err)
		}
	}

	data, err := os.ReadFile("./log/Completed-log.txt")
	if err != nil {
		t.Fatalf("ReadFile(Completed-log.txt) failed: %v", err)
	}

	content := string(data)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("expected %d lines, got %d (content: %q)", len(cases), len(lines), content)
	}

	for i, c := range cases {
		if !strings.Contains(lines[i], "videoID="+c.videoID) {
			t.Errorf("line %d missing videoID=%s: %s", i, c.videoID, lines[i])
		}
		if !strings.Contains(lines[i], "reason="+c.reason) {
			t.Errorf("line %d missing reason=%s: %s", i, c.reason, lines[i])
		}
	}
}

// TestLogAndLogReject_SeparateFiles 验证两个日志写入不同的文件
func TestLogAndLogReject_SeparateFiles(t *testing.T) {
	clearLog(t)
	defer removeLogDir()

	Log("dl-vid", "下载失败")
	LogReject("rej-vid", "拒绝写入记录")

	// 下载日志只包含 dl-vid
	dlData, err := os.ReadFile("./log/Download-log.txt")
	if err != nil {
		t.Fatalf("ReadFile(Download-log.txt) failed: %v", err)
	}
	if !strings.Contains(string(dlData), "dl-vid") {
		t.Errorf("Download-log.txt should contain dl-vid")
	}
	if strings.Contains(string(dlData), "rej-vid") {
		t.Errorf("Download-log.txt should NOT contain rej-vid")
	}

	// 拒绝日志只包含 rej-vid
	rejData, err := os.ReadFile("./log/Completed-log.txt")
	if err != nil {
		t.Fatalf("ReadFile(Completed-log.txt) failed: %v", err)
	}
	if !strings.Contains(string(rejData), "rej-vid") {
		t.Errorf("Completed-log.txt should contain rej-vid")
	}
	if strings.Contains(string(rejData), "dl-vid") {
		t.Errorf("Completed-log.txt should NOT contain dl-vid")
	}
}

// TestLogRejectSanitize 验证 LogReject 的换行清理
func TestLogRejectSanitize(t *testing.T) {
	clearLog(t)
	defer removeLogDir()

	if err := LogReject("vid", "reason\nwith\nnewlines"); err != nil {
		t.Fatalf("LogReject failed: %v", err)
	}

	data, err := os.ReadFile("./log/Completed-log.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	content := strings.TrimRight(string(data), "\n")
	if strings.Contains(content, "\n") {
		t.Errorf("Completed-log.txt contains unexpected newlines: %q", content)
	}
}

// TestLogPaths 验证日志目录被正确创建
func TestLogPaths(t *testing.T) {
	removeLogDir()
	defer removeLogDir()

	dl, rej := LogPaths()

	// 目录应该被创建
	if _, err := os.Stat("./log"); err != nil {
		t.Errorf("expected ./log directory to exist: %v", err)
	}

	// 路径应该包含正确的文件名
	if !strings.HasSuffix(dl, "Download-log.txt") {
		t.Errorf("download log path should end with Download-log.txt, got %s", dl)
	}
	if !strings.HasSuffix(rej, "Completed-log.txt") {
		t.Errorf("completed log path should end with Completed-log.txt, got %s", rej)
	}
}

// TestLog_CreatesLogDir 验证日志目录不存在时自动创建
func TestLog_CreatesLogDir(t *testing.T) {
	removeLogDir()
	defer removeLogDir()

	// 目录不存在时调用 Log，应自动创建
	if err := Log("vid", "test"); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join("./log", "Download-log.txt")); err != nil {
		t.Errorf("expected Download-log.txt to exist: %v", err)
	}
}
