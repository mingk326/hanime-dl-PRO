package registry

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record 单个视频的完成记录。
// 仅在 MP4 + JPG 都下载完成且通过 verifier 校验后才写入。
type Record struct {
	VideoID     string `json:"video_id"`
	Title       string `json:"title"`
	Mp4Path     string `json:"mp4_path"`
	JpgPath     string `json:"jpg_path"`
	Mp4Size     int64  `json:"mp4_size"`
	JpgSize     int64  `json:"jpg_size"`
	Resolution  string `json:"resolution"`
	CompletedAt string `json:"completed_at"`
}

// Registry 已完成视频的持久化记录。
// 使用目录存储：每个视频对应一个独立的 {videoID}.json 文件。
// 启动时扫描目录一次性加载到内存，运行时通过 mutex 保护并发读写。
// 单个文件写入采用「写临时文件 → rename」原子模式。
type Registry struct {
	mu      sync.RWMutex
	records map[string]Record
	dir     string
}

// NewRegistry 创建记录器，dir 为存放 .json 文件的目录。
// 目录不存在时自动创建；已有记录文件会被加载到内存。
func NewRegistry(dir string) *Registry {
	r := &Registry{
		records: make(map[string]Record),
		dir:     dir,
	}
	os.MkdirAll(dir, 0755)
	r.load()
	return r
}

// recordPath 返回指定视频ID对应的记录文件路径。
func (r *Registry) recordPath(videoID string) string {
	// 对 videoID 做基本清洗，防止路径穿越
	safe := videoID
	for _, c := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"} {
		safe = strings.ReplaceAll(safe, c, "_")
	}
	return filepath.Join(r.dir, safe+".json")
}

// load 扫描目录下所有 .json 文件，逐个加载到内存。
// 单个文件损坏不影响其他记录。
func (r *Registry) load() {
	files, err := filepath.Glob(filepath.Join(r.dir, "*.json"))
	if err != nil {
		log.Printf("[Registry] Failed to scan %s: %v", r.dir, err)
		return
	}

	count := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			log.Printf("[Registry] Failed to read %s: %v", f, err)
			continue
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			log.Printf("[Registry] Failed to parse %s (corrupt?), skipping: %v", f, err)
			continue
		}
		if rec.VideoID == "" {
			continue
		}
		r.mu.Lock()
		r.records[rec.VideoID] = rec
		r.mu.Unlock()
		count++
	}

	if count > 0 {
		log.Printf("[Registry] Loaded %d completed records from %s", count, r.dir)
	}
}

// IsCompleted 三层校验判断视频是否已完成且文件真实存在。
//
// 第1层: 记录中存在该 videoID 且分辨率匹配
// 第2层: os.Stat(mp4Path) 和 os.Stat(jpgPath) 都成功
// 第3层: 实际文件大小 == 记录的大小
//
// 任一层失败都会从记录中移除该条目（返回 needsRemove=true）。
// 全部通过返回 (mp4Path, jpgPath, true, false)。
func (r *Registry) IsCompleted(videoID, resolution string) (mp4Path, jpgPath string, ok, needsRemove bool) {
	r.mu.RLock()
	rec, exists := r.records[videoID]
	r.mu.RUnlock()

	// 第1层：记录不存在
	if !exists {
		return "", "", false, false
	}

	// 分辨率不匹配 → 需要重新下载（用户可能改了 VideoResolution）
	if rec.Resolution != resolution {
		return "", "", false, true
	}

	// 第2层：文件存在性检查
	mp4Info, err := os.Stat(rec.Mp4Path)
	if err != nil {
		return "", "", false, true
	}
	jpgInfo, err := os.Stat(rec.JpgPath)
	if err != nil {
		return "", "", false, true
	}

	// 第3层：文件大小匹配检查
	if mp4Info.Size() != rec.Mp4Size {
		return "", "", false, true
	}
	if jpgInfo.Size() != rec.JpgSize {
		return "", "", false, true
	}

	return rec.Mp4Path, rec.JpgPath, true, false
}

// Remove 从内存和磁盘记录中移除指定视频。
// 用于文件校验失败、文件被删除等场景。
func (r *Registry) Remove(videoID string) {
	r.mu.Lock()
	delete(r.records, videoID)
	os.Remove(r.recordPath(videoID))
	r.mu.Unlock()
}

// Record 写入一条完成记录。
// 调用前必须确保 MP4 + JPG 都已下载完成且通过 verifier 校验。
// 本方法会再次严格检查两个文件：必须存在且大小 > 0。
// 任一检查失败则返回错误，不写入任何记录文件。
// 每个视频写入独立的 {videoID}.json 文件（原子写入）。
func (r *Registry) Record(videoID, title, mp4Path, jpgPath, resolution string) error {
	// === 严格检查：MP4 和 JPG 都必须存在且大小 > 0 ===
	mp4Size, err := fileSize(mp4Path)
	if err != nil {
		return fmt.Errorf("mp4 文件不存在，拒绝写入记录: %w", err)
	}
	if mp4Size <= 0 {
		return fmt.Errorf("mp4 文件大小为 0，拒绝写入记录: %s", mp4Path)
	}

	jpgSize, err := fileSize(jpgPath)
	if err != nil {
		return fmt.Errorf("jpg 文件不存在，拒绝写入记录: %w", err)
	}
	if jpgSize <= 0 {
		return fmt.Errorf("jpg 文件大小为 0，拒绝写入记录: %s", jpgPath)
	}

	rec := Record{
		VideoID:     videoID,
		Title:       title,
		Mp4Path:     mp4Path,
		JpgPath:     jpgPath,
		Mp4Size:     mp4Size,
		JpgSize:     jpgSize,
		Resolution:  resolution,
		CompletedAt: time.Now().Format("2006-01-02 15:04:05"),
	}

	r.mu.Lock()
	r.records[videoID] = rec
	err = r.saveRecordLocked(videoID, rec)
	r.mu.Unlock()

	return err
}

// saveRecordLocked 将单条记录原子写入磁盘。
// 调用者必须持有写锁（r.mu）。
// 采用「写临时文件 → rename」模式，避免写入中途断电导致文件损坏。
func (r *Registry) saveRecordLocked(videoID string, rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	if err := os.MkdirAll(r.dir, 0755); err != nil {
		return fmt.Errorf("failed to create registry dir: %w", err)
	}

	finalPath := r.recordPath(videoID)
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write record tmp file: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("failed to rename record tmp file: %w", err)
	}

	return nil
}

// Count 返回已记录的视频数量。
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.records)
}

// fileSize 获取文件大小，文件不存在时返回错误。
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
