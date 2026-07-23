package scraper

import (
	"strings"
	"testing"
)

// TestSanitizeFilename_IllegalChars 验证 Windows 非法字符全部被替换为下划线
func TestSanitizeFilename_IllegalChars(t *testing.T) {
	// Windows 保留字符: < > : " / \ | ? * 以及控制字符
	input := `a<b>c:d"e/f\g|h?i*j`
	got := sanitizeFilename(input, 200, "unnamed")
	want := "a_b_c_d_e_f_g_h_i_j"
	if got != want {
		t.Errorf("illegal chars: got %q, want %q", got, want)
	}
}

// TestSanitizeFilename_ControlChars 验证控制字符 (0x00-0x1F) 被替换
func TestSanitizeFilename_ControlChars(t *testing.T) {
	input := "file\x00\x01\x1Fname"
	got := sanitizeFilename(input, 200, "unnamed")
	want := "file___name"
	if got != want {
		t.Errorf("control chars: got %q, want %q", got, want)
	}
}

// TestSanitizeFilename_TrailingSpaceAndDot 验证末尾空格和点号被去除
func TestSanitizeFilename_TrailingSpaceAndDot(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"filename   ", "filename"},    // 末尾空格
		{"filename...", "filename"},    // 末尾点号
		{"filename . . ", "filename"},  // 混合末尾
		{"   filename", "   filename"}, // 开头空格保留(Windows只限制末尾)
	}
	for _, c := range cases {
		got := sanitizeFilename(c.input, 200, "unnamed")
		if got != c.want {
			t.Errorf("trailing cleanup for %q: got %q, want %q", c.input, got, c.want)
		}
	}
}

// TestSanitizeFilename_UTF8Truncate 验证 UTF-8 安全截断到 maxBytes 字节
// 中文每个字符 3 字节，截断时不能在多字节字符中间截断
func TestSanitizeFilename_UTF8Truncate(t *testing.T) {
	// 4 个中文字符 = 12 字节，限制 10 字节应截断到 9 字节(3 个完整字符)
	input := "你好世界测试"
	got := sanitizeFilename(input, 10, "unnamed")
	want := "你好世" // 9 字节，第 4 个字符(3字节)会使总长变 12 > 10，故回退
	if got != want {
		t.Errorf("utf8 truncate: got %q (len=%d), want %q (len=%d)",
			got, len(got), want, len(want))
	}
	if len(got) > 10 {
		t.Errorf("result length %d exceeds maxBytes 10", len(got))
	}
}

// TestSanitizeFilename_TruncateAfterTrailingCleanup 验证截断后产生的末尾点号/空格被清理
// 这是处理顺序的关键：必须先截断再清理末尾
func TestSanitizeFilename_TruncateProducesTrailingDot(t *testing.T) {
	// 构造一个截断后正好以点号结尾的场景
	// "abcd." 共 5 字节，限制 4 字节截断后为 "abcd"，正常
	// 用 "ab.cd" 限制 3 字节 -> "ab." -> 清理末尾点号 -> "ab"
	input := "ab.cdef"
	got := sanitizeFilename(input, 3, "unnamed")
	want := "ab"
	if got != want {
		t.Errorf("trailing cleanup after truncate: got %q, want %q", got, want)
	}
}

// TestSanitizeFilename_EmptyFallback 验证清理后为空时返回 fallback
func TestSanitizeFilename_EmptyFallback(t *testing.T) {
	cases := []struct {
		input    string
		fallback string
	}{
		{"...", "unnamed"},   // 全点号，清理后为空
		{"   ", "unnamed"},   // 全空格，清理后为空
		{"", "default"},      // 空字符串
		{". . .", "unnamed"}, // 全点号空格，清理后为空
	}
	for _, c := range cases {
		got := sanitizeFilename(c.input, 200, c.fallback)
		if got != c.fallback {
			t.Errorf("fallback for %q: got %q, want %q", c.input, got, c.fallback)
		}
	}
}

// TestSanitizeFilename_AllIllegalCharsReplaced 验证全非法字符替换后为下划线(不触发fallback)
func TestSanitizeFilename_AllIllegalCharsReplaced(t *testing.T) {
	input := "<>:?*"
	got := sanitizeFilename(input, 200, "unnamed")
	want := "_____" // 5 个非法字符替换为 5 个下划线，不为空，不返回 fallback
	if got != want {
		t.Errorf("all illegal chars: got %q, want %q", got, want)
	}
}

// TestSanitizeFilename_200BytesLimit 验证截断到 200 字节的边界
func TestSanitizeFilename_200BytesLimit(t *testing.T) {
	// 构造 300 字节的 ASCII 字符串
	input := strings.Repeat("a", 300)
	got := sanitizeFilename(input, 200, "unnamed")
	if len(got) != 200 {
		t.Errorf("ascii truncate: got length %d, want 200", len(got))
	}
}

// TestSanitizeFilename_MixedChineseTruncate 验证混合中英文截断
func TestSanitizeFilename_MixedChineseTruncate(t *testing.T) {
	// "视频video文件file" = 4*3 + 5 + 4*3 + 4 = 29 字节
	// 限制 15 字节: "视频video文件" = 4*3+5+2*3 = 23 字节, 超过
	//                "视频video文" = 4*3+5+1*3 = 20 字节, 超过
	//                "视频video" = 4*3+5 = 17 字节, 超过
	//                "视频vide" = 4*3+4 = 16 字节, 超过
	//                "视频vid" = 4*3+3 = 15 字节, 正好
	input := "视频video文件file"
	got := sanitizeFilename(input, 15, "unnamed")
	if len(got) > 15 {
		t.Errorf("mixed truncate: got length %d (%q), must be <= 15", len(got), got)
	}
	// 验证没有产生 UTF-8 乱码(能正确解码所有 rune)
	for _, r := range got {
		if r == 0xFFFD {
			t.Errorf("mixed truncate produced invalid utf8: %q", got)
			break
		}
	}
}

// TestSanitizeFilename_NormalCase 验证正常文件名不被破坏
func TestSanitizeFilename_NormalCase(t *testing.T) {
	input := "正常的文件名 123"
	got := sanitizeFilename(input, 200, "unnamed")
	if got != input {
		t.Errorf("normal case: got %q, want %q", got, input)
	}
}

// TestSanitizeFilename_RealWorldExamples 验证真实场景中的标题
func TestSanitizeFilename_RealWorldExamples(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "带冒号的标题",
			input: "Video: Episode 1",
			want:  "Video_ Episode 1",
		},
		{
			name:  "带斜杠的标题",
			input: "Anime/Subtitle: Test",
			want:  "Anime_Subtitle_ Test",
		},
		{
			name:  "带引号的标题",
			input: `"Quoted" Title`,
			want:  "_Quoted_ Title",
		},
		{
			name:  "末尾带点号",
			input: "Title.",
			want:  "Title",
		},
		{
			name:  "带竖线和星号",
			input: "A | B * C",
			want:  "A _ B _ C",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeFilename(c.input, 200, "unnamed")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
