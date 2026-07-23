// Package verifier 负责对下载完成的文件进行完整性校验。
// 通过文件大小阈值 + 文件头魔数（magic bytes）双重校验判断文件是否为声称的格式，
// 能有效检测出以下问题：
//   - 下载不完整（文件头都不完整或文件过小）
//   - 下载了错误内容（如 HTML 错误页、重定向页面被保存为 .mp4/.jpg）
//   - 空文件
//
// 支持的格式及校验规则：
//   - JPG/JPEG: 文件大小 > 100 字节 + 前 2 字节为 FF D8 (JPEG SOI 标记)
//   - MP4: 文件大小 > 10KB + 偏移 4-7 字节为 "ftyp" (ISO Base Media File Format)
//
// 文件大小阈值的意义：真实封面图至少几 KB，真实视频至少几百 KB，
// 远大于这些阈值；而错误响应（HTML 错误页、重定向片段）通常很小，
// 加上大小阈值可作为魔数校验之外的双重保险。
package verifier

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrEmptyFile 文件为空（0 字节）
var ErrEmptyFile = errors.New("file is empty")

// ErrUnknownFormat 不支持的文件扩展名
var ErrUnknownFormat = errors.New("unsupported file extension for verification")

// jpgMagic 是 JPEG 文件的 SOI (Start of Image) 标记：FF D8。
// 这是 JPEG 标准规定的文件起始 2 字节，是最权威的 JPEG 标识。
// 参考: https://www.w3.org/Graphics/JPEG/itu-t81.pdf
var jpgMagic = []byte{0xFF, 0xD8}

// minJPGSize 是 JPG 文件的最小合法大小阈值（字节）。
// 真实封面图至少几 KB，小于此值的"JPG"几乎肯定是错误响应片段。
const minJPGSize = 100

// mp4Ftyp 是 MP4 (ISO Base Media File Format) 文件偏移 4-7 字节的标识。
// MP4 文件结构：[4字节box大小][ftyp][...], 因此偏移 4-7 是 "ftyp"。
// 参考: https://developer.apple.com/library/archive/documentation/QuickTime/QTFF/QTFFChap1/QTFFChap1.html
var mp4Ftyp = []byte("ftyp")

// minMP4Size 是 MP4 文件的最小合法大小阈值（字节）。
// 真实视频至少几百 KB，小于此值的"MP4"几乎肯定是 HTML 错误页或重定向页面。
const minMP4Size = 10 * 1024 // 10KB

// maxHeaderBytes 是校验时需要读取的最大文件头字节数。
// JPG 需要前 2 字节，MP4 需要前 8 字节，取较大值并留余量。
const maxHeaderBytes = 12

// Verify 根据文件扩展名自动选择校验方式，校验文件完整性。
// 支持的扩展名：.mp4, .jpg, .jpeg。
// 其他扩展名返回 ErrUnknownFormat（调用方可决定是否忽略）。
//
// 校验规则：
//   - JPG: 文件大小 > 100 字节 + 前 2 字节为 FF D8
//   - MP4: 文件大小 > 10KB + 偏移 4-7 字节为 "ftyp"
//
// 返回 nil 表示校验通过，非 nil 表示校验失败及原因。
func Verify(filePath string) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp4":
		return verifyMP4(filePath)
	case ".jpg", ".jpeg":
		return verifyJPG(filePath)
	default:
		return ErrUnknownFormat
	}
}

// verifyJPG 校验 JPEG 文件：文件大小 > 100 字节且前 2 字节为 FF D8。
func verifyJPG(filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat jpg file failed: %w", err)
	}
	if info.Size() == 0 {
		return ErrEmptyFile
	}
	if info.Size() <= int64(minJPGSize) {
		return fmt.Errorf("jpg file too small: got %d bytes, need more than %d bytes (real cover image should be at least a few KB)", info.Size(), minJPGSize)
	}

	header, err := readHeader(filePath, len(jpgMagic))
	if err != nil {
		return fmt.Errorf("read jpg header failed: %w", err)
	}

	if len(header) < len(jpgMagic) {
		return fmt.Errorf("jpg header too short: got %d bytes, need at least %d", len(header), len(jpgMagic))
	}

	for i, b := range jpgMagic {
		if header[i] != b {
			return fmt.Errorf("jpg magic mismatch: byte %d got 0x%02X, want 0x%02X (file may be corrupt or not a real JPEG)", i, header[i], b)
		}
	}
	return nil
}

// verifyMP4 校验 MP4 文件：文件大小 > 10KB 且偏移 4-7 字节为 "ftyp"。
func verifyMP4(filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat mp4 file failed: %w", err)
	}
	if info.Size() == 0 {
		return ErrEmptyFile
	}
	if info.Size() <= int64(minMP4Size) {
		return fmt.Errorf("mp4 file too small: got %d bytes, need more than %d bytes (real video should be at least a few hundred KB)", info.Size(), minMP4Size)
	}

	// MP4 需要至少 8 字节：4字节size + 4字节"ftyp"
	const minFtypHeader = 8
	header, err := readHeader(filePath, maxHeaderBytes)
	if err != nil {
		return fmt.Errorf("read mp4 header failed: %w", err)
	}

	if len(header) < minFtypHeader {
		return fmt.Errorf("mp4 header too short: got %d bytes, need at least %d", len(header), minFtypHeader)
	}

	// 检查偏移 4-7 是否为 "ftyp"
	ftypStart := 4
	for i, b := range mp4Ftyp {
		if header[ftypStart+i] != b {
			return fmt.Errorf("mp4 ftyp box mismatch: byte %d got 0x%02X, want 0x%02X (file may be corrupt or not a real MP4)", ftypStart+i, header[ftypStart+i], b)
		}
	}
	return nil
}

// readHeader 读取文件前 n 字节。若文件不足 n 字节，返回实际读取的字节。
func readHeader(filePath string, n int) ([]byte, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, n)
	read, err := f.Read(buf)
	if err != nil && read == 0 {
		return nil, err
	}
	return buf[:read], nil
}

// IsCorrupt 判断校验错误是否表示文件损坏（而非文件不存在等系统错误）。
// 用于决定是否应该删除损坏的文件。空文件、魔数不匹配、文件过小都算损坏。
func IsCorrupt(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrEmptyFile) {
		return true
	}
	// 魔数不匹配或文件过小的错误都包含 "mismatch" 或 "too small" / "too short" 字样
	msg := err.Error()
	return strings.Contains(msg, "mismatch") ||
		strings.Contains(msg, "too small") ||
		strings.Contains(msg, "too short")
}
