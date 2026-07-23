package verifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFile 创建临时测试文件并写入指定内容，返回文件路径
func makeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("WriteFile(%s) failed: %v", name, err)
	}
	return path
}

// makeFileWithSize 创建以指定前缀开头、总大小为 size 字节的临时文件（用于测试大小阈值）
func makeFileWithSize(t *testing.T, name string, prefix []byte, size int) string {
	t.Helper()
	content := make([]byte, size)
	copy(content, prefix) // 填充前缀字节，剩余部分为 0x00
	return makeFile(t, name, content)
}

// validMP4Header 构造合法 MP4 文件头：4字节size + "ftyp" + 4字节brand
func validMP4Header() []byte {
	return []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
}

// validJPGHeader 构造合法 JPG 文件头：FF D8 + 后续填充
func validJPGHeader() []byte {
	return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
}

// --- JPG 校验测试 ---

// TestVerifyJPG_Valid 合法 JPG（>100B + FF D8 头）应通过
func TestVerifyJPG_Valid(t *testing.T) {
	// 构造 200B 的合法 JPG（>100B 阈值）
	content := makeFileWithSize(t, "test.jpg", validJPGHeader(), 200)
	if err := Verify(content); err != nil {
		t.Errorf("valid jpg (>100B + FF D8) should pass, got: %v", err)
	}
}

func TestVerifyJPG_ValidJPEGExt(t *testing.T) {
	content := makeFileWithSize(t, "test.jpeg", validJPGHeader(), 200)
	if err := Verify(content); err != nil {
		t.Errorf("valid .jpeg should pass, got: %v", err)
	}
}

// TestVerifyJPG_EmptyFile 空文件应失败
func TestVerifyJPG_EmptyFile(t *testing.T) {
	path := makeFile(t, "empty.jpg", []byte{})
	err := Verify(path)
	if err == nil {
		t.Fatal("empty jpg should fail")
	}
	if !IsCorrupt(err) {
		t.Errorf("empty file should be corrupt, got: %v", err)
	}
}

// TestVerifyJPG_TooSmall 文件 ≤100B 应失败（即使魔数正确）
func TestVerifyJPG_TooSmall(t *testing.T) {
	// 100B，魔数正确但大小刚好等于阈值（需 >100B），应失败
	content := makeFileWithSize(t, "small.jpg", validJPGHeader(), 100)
	err := Verify(content)
	if err == nil {
		t.Fatal("jpg with size == 100 should fail (need > 100)")
	}
	if !IsCorrupt(err) {
		t.Errorf("too small file should be corrupt, got: %v", err)
	}
}

// TestVerifyJPG_JustAboveThreshold 刚好超过阈值（101B）应通过
func TestVerifyJPG_JustAboveThreshold(t *testing.T) {
	content := makeFileWithSize(t, "ok.jpg", validJPGHeader(), 101)
	if err := Verify(content); err != nil {
		t.Errorf("jpg with size == 101 (>100) and valid magic should pass, got: %v", err)
	}
}

// TestVerifyJPG_WrongMagic 魔数错误应失败（即使文件够大）
func TestVerifyJPG_WrongMagic(t *testing.T) {
	// HTML 内容被错误保存为 .jpg，文件够大但魔数不对
	html := []byte("<html><body>403 Forbidden</body></html>")
	content := makeFileWithSize(t, "fake.jpg", html, 200)
	err := Verify(content)
	if err == nil {
		t.Fatal("fake jpg (html content, >100B) should fail")
	}
	if !IsCorrupt(err) {
		t.Errorf("wrong magic should be corrupt, got: %v", err)
	}
}

// TestVerifyJPG_FF_D8_Only 前2字节是 FF D8（第3字节非FF）也应通过
// 因为新规则只校验 FF D8 (SOI 标记)，更宽容
func TestVerifyJPG_FF_D8_Only(t *testing.T) {
	// FF D8 00 ... (第3字节不是FF，但符合新规则只校验前2字节)
	header := []byte{0xFF, 0xD8, 0x00, 0x00}
	content := makeFileWithSize(t, "soi_only.jpg", header, 200)
	if err := Verify(content); err != nil {
		t.Errorf("jpg with FF D8 (SOI only) should pass under new rule, got: %v", err)
	}
}

// --- MP4 校验测试 ---

// TestVerifyMP4_Valid 合法 MP4（>10KB + ftyp 头）应通过
func TestVerifyMP4_Valid(t *testing.T) {
	// 构造 11KB 的合法 MP4（>10KB 阈值）
	content := makeFileWithSize(t, "test.mp4", validMP4Header(), 11*1024)
	if err := Verify(content); err != nil {
		t.Errorf("valid mp4 (>10KB + ftyp) should pass, got: %v", err)
	}
}

// TestVerifyMP4_EmptyFile 空文件应失败
func TestVerifyMP4_EmptyFile(t *testing.T) {
	path := makeFile(t, "empty.mp4", []byte{})
	err := Verify(path)
	if err == nil {
		t.Fatal("empty mp4 should fail")
	}
	if !IsCorrupt(err) {
		t.Errorf("empty file should be corrupt, got: %v", err)
	}
}

// TestVerifyMP4_TooSmall 文件 ≤10KB 应失败（即使 ftyp 正确）
func TestVerifyMP4_TooSmall(t *testing.T) {
	// 10KB，ftyp 正确但大小刚好等于阈值（需 >10KB），应失败
	content := makeFileWithSize(t, "small.mp4", validMP4Header(), 10*1024)
	err := Verify(content)
	if err == nil {
		t.Fatal("mp4 with size == 10KB should fail (need > 10KB)")
	}
	if !IsCorrupt(err) {
		t.Errorf("too small file should be corrupt, got: %v", err)
	}
}

// TestVerifyMP4_JustAboveThreshold 刚好超过阈值（10KB+1B）应通过
func TestVerifyMP4_JustAboveThreshold(t *testing.T) {
	content := makeFileWithSize(t, "ok.mp4", validMP4Header(), 10*1024+1)
	if err := Verify(content); err != nil {
		t.Errorf("mp4 with size == 10KB+1 (>10KB) and valid ftyp should pass, got: %v", err)
	}
}

// TestVerifyMP4_WrongFtyp ftyp 错误应失败（即使文件够大）
func TestVerifyMP4_WrongFtyp(t *testing.T) {
	// HTML 内容被错误保存为 .mp4，文件够大但 ftyp 不对
	html := []byte("<html>404 Not Found</html>")
	content := makeFileWithSize(t, "fake.mp4", html, 11*1024)
	err := Verify(content)
	if err == nil {
		t.Fatal("fake mp4 (html content, >10KB) should fail")
	}
	if !IsCorrupt(err) {
		t.Errorf("wrong ftyp should be corrupt, got: %v", err)
	}
}

// TestVerifyMP4_DifferentBrand 不同的 ftyp brand（如 mp42）也应通过
func TestVerifyMP4_DifferentBrand(t *testing.T) {
	mp4 := []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	content := makeFileWithSize(t, "mp42.mp4", mp4, 11*1024)
	if err := Verify(content); err != nil {
		t.Errorf("mp4 with mp42 brand should pass, got: %v", err)
	}
}

// TestVerifyMP4_HTMLErrorPage HTML 错误页（即使 >10KB）应失败
// 这是最常见的错误场景：403/404 的 HTML 响应被保存为 .mp4
func TestVerifyMP4_HTMLErrorPage(t *testing.T) {
	// 39字节/段 × 300段 ≈ 11.7KB > 10KB
	html := strings.Repeat("<html><body>403 Forbidden</body></html>", 300) // >10KB
	content := makeFile(t, "error.mp4", []byte(html))
	if len(html) <= 10240 {
		t.Fatalf("test html content must be >10KB, got %d", len(html))
	}
	err := Verify(content)
	if err == nil {
		t.Fatal("html error page saved as .mp4 should fail")
	}
	if !IsCorrupt(err) {
		t.Errorf("html error page should be corrupt, got: %v", err)
	}
}

// --- 其他扩展名测试 ---

func TestVerify_UnsupportedExt(t *testing.T) {
	path := makeFile(t, "file.txt", []byte("hello"))
	err := Verify(path)
	if err != ErrUnknownFormat {
		t.Errorf("unsupported ext should return ErrUnknownFormat, got: %v", err)
	}
}

// TestVerify_NonExistentFile 文件不存在应返回错误，但不算 corrupt
func TestVerify_NonExistentFile(t *testing.T) {
	err := Verify("/nonexistent/path/video.mp4")
	if err == nil {
		t.Fatal("non-existent file should fail")
	}
	// 文件不存在是系统错误，不应被判为 corrupt（不应删除）
	if IsCorrupt(err) {
		t.Errorf("non-existent file should not be corrupt, got: %v", err)
	}
}

// TestVerify_ExtensionCaseInsensitive 验证扩展名大小写不敏感
func TestVerify_ExtensionCaseInsensitive(t *testing.T) {
	mp4Content := makeFileWithSize(t, "TEST.MP4", validMP4Header(), 11*1024)
	if err := Verify(mp4Content); err != nil {
		t.Errorf("uppercase .MP4 should pass, got: %v", err)
	}

	jpgContent := makeFileWithSize(t, "TEST.JPG", validJPGHeader(), 200)
	if err := Verify(jpgContent); err != nil {
		t.Errorf("uppercase .JPG should pass, got: %v", err)
	}
}
