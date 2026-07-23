package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// makeTestFiles 在临时目录创建指定大小的 mp4 和 jpg 文件
func makeTestFiles(t *testing.T, dir string, mp4Name string, jpgName string, mp4Size int, jpgSize int) (string, string) {
	t.Helper()
	mp4Path := filepath.Join(dir, mp4Name)
	jpgPath := filepath.Join(dir, jpgName)

	if err := os.WriteFile(mp4Path, make([]byte, mp4Size), 0644); err != nil {
		t.Fatalf("failed to create mp4: %v", err)
	}
	if err := os.WriteFile(jpgPath, make([]byte, jpgSize), 0644); err != nil {
		t.Fatalf("failed to create jpg: %v", err)
	}
	return mp4Path, jpgPath
}

func TestNewRegistry_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))
	if r.Count() != 0 {
		t.Errorf("expected 0 records, got %d", r.Count())
	}
}

func TestRecord_AndIsCompleted(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")

	r := NewRegistry(regDir)
	if r.Count() != 0 {
		t.Fatalf("expected 0 records, got %d", r.Count())
	}

	// 创建测试文件
	mp4Path, jpgPath := makeTestFiles(t, dir, "video.mp4", "video.jpg", 10000, 200)

	// 写入记录
	if err := r.Record("vid1", "Test Video", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	if r.Count() != 1 {
		t.Errorf("expected 1 record, got %d", r.Count())
	}

	// 确认磁盘上生成了独立文件
	recordFile := filepath.Join(regDir, "vid1.json")
	if _, err := os.Stat(recordFile); err != nil {
		t.Errorf("expected record file %s to exist: %v", recordFile, err)
	}

	// 三层校验全部通过
	gotMp4, gotJpg, ok, needsRemove := r.IsCompleted("vid1", "1080p")
	if !ok {
		t.Errorf("expected IsCompleted=true, got false (needsRemove=%v)", needsRemove)
	}
	if needsRemove {
		t.Errorf("expected needsRemove=false")
	}
	if gotMp4 != mp4Path {
		t.Errorf("mp4Path mismatch: got %s, want %s", gotMp4, mp4Path)
	}
	if gotJpg != jpgPath {
		t.Errorf("jpgPath mismatch: got %s, want %s", gotJpg, jpgPath)
	}
}

func TestIsCompleted_NotInRegistry(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))

	_, _, ok, needsRemove := r.IsCompleted("nonexistent", "1080p")
	if ok {
		t.Errorf("expected ok=false for nonexistent video")
	}
	if needsRemove {
		t.Errorf("expected needsRemove=false for nonexistent video")
	}
}

func TestIsCompleted_ResolutionMismatch(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 用不同分辨率查询
	_, _, ok, needsRemove := r.IsCompleted("vid1", "720p")
	if ok {
		t.Errorf("expected ok=false for resolution mismatch")
	}
	if !needsRemove {
		t.Errorf("expected needsRemove=true for resolution mismatch")
	}
}

func TestIsCompleted_Mp4Deleted(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 删除 mp4 文件
	os.Remove(mp4Path)

	_, _, ok, needsRemove := r.IsCompleted("vid1", "1080p")
	if ok {
		t.Errorf("expected ok=false when mp4 deleted")
	}
	if !needsRemove {
		t.Errorf("expected needsRemove=true when mp4 deleted")
	}
}

func TestIsCompleted_JpgDeleted(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 删除 jpg 文件
	os.Remove(jpgPath)

	_, _, ok, needsRemove := r.IsCompleted("vid1", "1080p")
	if ok {
		t.Errorf("expected ok=false when jpg deleted")
	}
	if !needsRemove {
		t.Errorf("expected needsRemove=true when jpg deleted")
	}
}

func TestIsCompleted_SizeMismatch(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 覆盖 mp4 文件为不同大小
	os.WriteFile(mp4Path, make([]byte, 5000), 0644)

	_, _, ok, needsRemove := r.IsCompleted("vid1", "1080p")
	if ok {
		t.Errorf("expected ok=false when mp4 size mismatch")
	}
	if !needsRemove {
		t.Errorf("expected needsRemove=true when mp4 size mismatch")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if r.Count() != 1 {
		t.Fatalf("expected 1 record, got %d", r.Count())
	}

	// 确认文件存在
	recordFile := filepath.Join(regDir, "vid1.json")
	if _, err := os.Stat(recordFile); err != nil {
		t.Fatalf("record file should exist before Remove: %v", err)
	}

	r.Remove("vid1")

	if r.Count() != 0 {
		t.Errorf("expected 0 records after Remove, got %d", r.Count())
	}

	// 确认磁盘文件也删除了
	if _, err := os.Stat(recordFile); !os.IsNotExist(err) {
		t.Errorf("expected record file to be deleted after Remove")
	}

	// 重新加载确认
	r2 := NewRegistry(regDir)
	if r2.Count() != 0 {
		t.Errorf("expected 0 records after reload, got %d", r2.Count())
	}
}

func TestLoad_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")

	r1 := NewRegistry(regDir)
	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r1.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 新实例加载同一目录
	r2 := NewRegistry(regDir)
	if r2.Count() != 1 {
		t.Fatalf("expected 1 record after reload, got %d", r2.Count())
	}

	_, _, ok, _ := r2.IsCompleted("vid1", "1080p")
	if !ok {
		t.Errorf("expected IsCompleted=true after reload")
	}
}

func TestLoad_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	os.MkdirAll(regDir, 0755)

	// 写入损坏的 JSON 文件
	os.WriteFile(filepath.Join(regDir, "bad.json"), []byte("{invalid json}}}"), 0644)
	// 写入一个正常的文件
	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	goodRecord := Record{
		VideoID:    "good",
		Title:      "Good Video",
		Mp4Path:    mp4Path,
		JpgPath:    jpgPath,
		Mp4Size:    10000,
		JpgSize:    200,
		Resolution: "1080p",
	}
	goodData, _ := json.MarshalIndent(goodRecord, "", "  ")
	os.WriteFile(filepath.Join(regDir, "good.json"), goodData, 0644)

	r := NewRegistry(regDir)
	// 损坏文件被跳过，正常文件被加载
	if r.Count() != 1 {
		t.Errorf("expected 1 record (corrupt skipped), got %d", r.Count())
	}
	_, _, ok, _ := r.IsCompleted("good", "1080p")
	if !ok {
		t.Errorf("expected good record to be loaded")
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 确认 .tmp 文件已被 rename 掉（不应存在）
	tmpPath := filepath.Join(regDir, "vid1.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("expected .tmp file to not exist after atomic write")
	}

	// 确认正式文件存在且是有效 JSON
	recordFile := filepath.Join(regDir, "vid1.json")
	data, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatalf("failed to read record file: %v", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record file is not valid JSON: %v", err)
	}
	if rec.VideoID != "vid1" {
		t.Errorf("expected video_id=vid1, got %s", rec.VideoID)
	}
}

func TestSave_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "subdir", "deep", "Completed")
	r := NewRegistry(regDir)

	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	if err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p"); err != nil {
		t.Fatalf("Record failed with nested dir: %v", err)
	}

	if _, err := os.Stat(filepath.Join(regDir, "vid1.json")); err != nil {
		t.Errorf("expected record file to exist: %v", err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			vid := "vid" + string(rune('a'+idx))
			mp4 := filepath.Join(dir, vid+".mp4")
			jpg := filepath.Join(dir, vid+".jpg")
			os.WriteFile(mp4, make([]byte, 1000+idx), 0644)
			os.WriteFile(jpg, make([]byte, 100+idx), 0644)
			if err := r.Record(vid, "Title"+vid, mp4, jpg, "1080p"); err != nil {
				t.Errorf("concurrent Record failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if r.Count() != 20 {
		t.Errorf("expected 20 records, got %d", r.Count())
	}

	// 重新加载，确认数据完整
	r2 := NewRegistry(regDir)
	if r2.Count() != 20 {
		t.Errorf("expected 20 records after reload, got %d", r2.Count())
	}

	// 确认磁盘上有 20 个独立文件
	files, _ := filepath.Glob(filepath.Join(regDir, "*.json"))
	if len(files) != 20 {
		t.Errorf("expected 20 json files on disk, got %d", len(files))
	}
}

func TestMultipleRecords_IndependentFiles(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "Completed")
	r := NewRegistry(regDir)

	// 写入 3 个不同的视频记录
	for i, vid := range []string{"aaa", "bbb", "ccc"} {
		mp4, jpg := makeTestFiles(t, dir, vid+".mp4", vid+".jpg", 10000+i, 200+i)
		if err := r.Record(vid, "Title "+vid, mp4, jpg, "1080p"); err != nil {
			t.Fatalf("Record %s failed: %v", vid, err)
		}
	}

	// 确认 3 个独立文件存在
	for _, vid := range []string{"aaa", "bbb", "ccc"} {
		path := filepath.Join(regDir, vid+".json")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}

	// 删除其中一个文件，不影响其他
	r.Remove("bbb")
	if r.Count() != 2 {
		t.Errorf("expected 2 records after remove, got %d", r.Count())
	}
	_, _, ok, _ := r.IsCompleted("aaa", "1080p")
	if !ok {
		t.Errorf("aaa should still be completed")
	}
	_, _, ok, _ = r.IsCompleted("ccc", "1080p")
	if !ok {
		t.Errorf("ccc should still be completed")
	}
}

// === 严格校验测试：MP4 或 JPG 缺失/空文件时拒绝写入记录 ===

func TestRecord_RejectMissingMp4(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))

	// 只有 JPG，没有 MP4
	_, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	err := r.Record("vid1", "Title", "/nonexistent/mp4.mp4", jpgPath, "1080p")
	if err == nil {
		t.Errorf("expected error when mp4 missing")
	}
	if r.Count() != 0 {
		t.Errorf("expected 0 records when mp4 missing, got %d", r.Count())
	}
	// 确认磁盘上没有生成 json 文件
	if _, err := os.Stat(filepath.Join(dir, "Completed", "vid1.json")); !os.IsNotExist(err) {
		t.Errorf("expected no json file when mp4 missing")
	}
}

func TestRecord_RejectMissingJpg(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))

	// 只有 MP4，没有 JPG
	mp4Path, _ := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	err := r.Record("vid1", "Title", mp4Path, "/nonexistent/jpg.jpg", "1080p")
	if err == nil {
		t.Errorf("expected error when jpg missing")
	}
	if r.Count() != 0 {
		t.Errorf("expected 0 records when jpg missing, got %d", r.Count())
	}
	if _, err := os.Stat(filepath.Join(dir, "Completed", "vid1.json")); !os.IsNotExist(err) {
		t.Errorf("expected no json file when jpg missing")
	}
}

func TestRecord_RejectEmptyMp4(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))

	// MP4 是空文件（大小为 0），JPG 正常
	emptyMp4 := filepath.Join(dir, "empty.mp4")
	os.WriteFile(emptyMp4, []byte{}, 0644)
	_, jpgPath := makeTestFiles(t, dir, "v.jpg", "v2.jpg", 0, 200)
	// 修正：jpgPath 需要非空
	jpgPath = filepath.Join(dir, "v2.jpg")
	os.WriteFile(jpgPath, make([]byte, 200), 0644)

	err := r.Record("vid1", "Title", emptyMp4, jpgPath, "1080p")
	if err == nil {
		t.Errorf("expected error when mp4 size is 0")
	}
	if r.Count() != 0 {
		t.Errorf("expected 0 records when mp4 empty, got %d", r.Count())
	}
}

func TestRecord_RejectEmptyJpg(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))

	// MP4 正常，JPG 是空文件
	mp4Path := filepath.Join(dir, "v.mp4")
	os.WriteFile(mp4Path, make([]byte, 10000), 0644)
	emptyJpg := filepath.Join(dir, "empty.jpg")
	os.WriteFile(emptyJpg, []byte{}, 0644)

	err := r.Record("vid1", "Title", mp4Path, emptyJpg, "1080p")
	if err == nil {
		t.Errorf("expected error when jpg size is 0")
	}
	if r.Count() != 0 {
		t.Errorf("expected 0 records when jpg empty, got %d", r.Count())
	}
}

func TestRecord_AcceptWhenBothFilesValid(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "Completed"))

	// MP4 和 JPG 都存在且非空 → 应该成功写入
	mp4Path, jpgPath := makeTestFiles(t, dir, "v.mp4", "v.jpg", 10000, 200)
	err := r.Record("vid1", "Title", mp4Path, jpgPath, "1080p")
	if err != nil {
		t.Errorf("expected success when both files valid, got error: %v", err)
	}
	if r.Count() != 1 {
		t.Errorf("expected 1 record, got %d", r.Count())
	}
	// 确认磁盘文件存在
	if _, err := os.Stat(filepath.Join(dir, "Completed", "vid1.json")); err != nil {
		t.Errorf("expected json file to exist: %v", err)
	}
}
