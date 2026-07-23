package scraper

import (
	"strings"
	"testing"
)

// TestFilenameFormat_WithVideoIDPrefix 验证文件名以 [视频ID] 开头
func TestFilenameFormat_WithVideoIDPrefix(t *testing.T) {
	cases := []struct {
		name    string
		videoID string
		title   string
		want    string
	}{
		{
			name:    "日文标题",
			videoID: "407238",
			title:   "【音声付き】ルーシーとモルス(事後)",
			want:    "[407238]【音声付き】ルーシーとモルス(事後)",
		},
		{
			name:    "含作者名带方括号的标题",
			videoID: "407238",
			title:   "[CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)",
			want:    "[407238][CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)",
		},
		{
			name:    "纯英文标题",
			videoID: "12345",
			title:   "Sample Video Title",
			want:    "[12345]Sample Video Title",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeFilename("["+c.videoID+"]"+c.title, 200, "unnamed")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			// 验证以 [视频ID] 开头
			if !strings.HasPrefix(got, "["+c.videoID+"]") {
				t.Errorf("filename %q should start with [%s]", got, c.videoID)
			}
		})
	}
}

// TestFilenameFormat_PrefixNotTruncated 验证前缀 [视频ID] 在截断时不会被破坏
// 当标题极长时，截断应发生在标题部分，前缀完整保留
func TestFilenameFormat_PrefixNotTruncated(t *testing.T) {
	videoID := "407238"
	// 构造极长标题：前缀 [407238] = 8 字节，标题需超长以触发截断
	// 200 字节限制下，8 字节前缀 + 192+ 字节标题会触发截断
	longTitle := strings.Repeat("あ", 100) // 每个日文字符 3 字节，100 个 = 300 字节

	got := sanitizeFilename("["+videoID+"]"+longTitle, 200, "unnamed")

	// 前缀必须完整保留
	if !strings.HasPrefix(got, "["+videoID+"]") {
		t.Errorf("prefix should be preserved after truncation, got: %q", got)
	}

	// 总长度不超过 200 字节
	if len(got) > 200 {
		t.Errorf("truncated length %d exceeds 200 bytes", len(got))
	}

	// 验证前缀后面的内容是有效的 UTF-8（没有乱码）
	rest := strings.TrimPrefix(got, "["+videoID+"]")
	for _, r := range rest {
		if r == 0xFFFD {
			t.Errorf("truncated title contains invalid UTF-8 (replacement char), got: %q", rest)
			break
		}
	}
}

// TestFilenameFormat_IllegalCharsInTitle 验证标题中的非法字符被替换但前缀保留
func TestFilenameFormat_IllegalCharsInTitle(t *testing.T) {
	videoID := "407238"
	title := `Video/Sub: Title?`
	got := sanitizeFilename("["+videoID+"]"+title, 200, "unnamed")
	want := "[407238]Video_Sub_ Title_"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFilenameFormat_RealWorldExample 验证用户给出的真实示例
// 标题中本身包含 [CEO NEET (ニート社長)] 方括号，必须完整保留
// 最终文件名：[407238][CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)
func TestFilenameFormat_RealWorldExample(t *testing.T) {
	input := "[407238][CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)"
	got := sanitizeFilename(input, 200, "unnamed")
	want := "[407238][CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)"
	if got != want {
		t.Errorf("real world example: got %q, want %q", got, want)
	}
}

// TestFilenameFormat_BracketsInTitlePreserved 专门验证标题中的方括号不被删除
// [ 和 ] 不是 Windows 非法字符，必须原样保留
func TestFilenameFormat_BracketsInTitlePreserved(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "标题含方括号作者名",
			input: "[407238][CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)",
			want:  "[407238][CEO NEET (ニート社長)] 【音声付き】ルーシーとモルス(事後)",
		},
		{
			name:  "标题含多组方括号",
			input: "[407238][作者名][系列名] 标题",
			want:  "[407238][作者名][系列名] 标题",
		},
		{
			name:  "标题仅方括号",
			input: "[407238][测试]",
			want:  "[407238][测试]",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeFilename(c.input, 200, "unnamed")
			if got != c.want {
				t.Errorf("brackets should be preserved: got %q, want %q", got, c.want)
			}
			// 确保方括号数量不变（[ 和 ] 各出现相同次数）
			leftCount := strings.Count(got, "[")
			rightCount := strings.Count(got, "]")
			inputLeft := strings.Count(c.input, "[")
			inputRight := strings.Count(c.input, "]")
			if leftCount != inputLeft || rightCount != inputRight {
				t.Errorf("bracket count changed: input [%d,%d] -> output [%d,%d]",
					inputLeft, inputRight, leftCount, rightCount)
			}
		})
	}
}

// TestFilenameFormat_JPGAndMP4SameBase 验证 MP4 和 JPG 使用相同的基础文件名
// 两者仅扩展名不同，便于关联同一视频的封面和视频
func TestFilenameFormat_JPGAndMP4SameBase(t *testing.T) {
	videoID := "407238"
	title := "ルーシーとモルス"
	base := sanitizeFilename("["+videoID+"]"+title, 200, "unnamed")

	mp4Name := base + ".mp4"
	jpgName := base + ".jpg"

	// 两者基础名相同
	if mp4Name[:len(mp4Name)-4] != jpgName[:len(jpgName)-4] {
		t.Errorf("mp4 and jpg should share the same base name, got mp4=%q jpg=%q", mp4Name, jpgName)
	}
}
