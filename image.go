package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
)

// 展示图最长边：手机拍的照片动不动 3000~4000px，直接存、直接看都慢。
// 压到 1600px、JPEG 质量 82，一张 3MB 的照片能到 200~400KB，肉眼看不出差别。
const loveMaxImageDim = 1600
const loveJPEGQuality = 82

// 缩略图最长边：画廊里卡片也就 260~300px 宽，400px 的缩略图足够清晰，
// 一张一般只有 30~60KB，比 1600px 的原图小一个量级，画廊加载快得多。
// 灯箱点开看大图时再用原图。GIF 动图不生成缩略图（用原图）。
const loveThumbDim = 400
const loveThumbQuality = 75

// makeLoveThumbnail 给一张图做缩略图：最长边 400px、JPEG 质量 75。
// 解不出来、GIF、或压完没更小都返回 nil（调用方用原图兜底）。
func makeLoveThumbnail(data []byte) []byte {
	ct := http.DetectContentType(data)
	if ct != "image/jpeg" && ct != "image/png" {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= loveThumbDim {
		return nil // 本来就够小，不用再压。
	}
	scale := float64(loveThumbDim) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	r := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(r, r.Bounds(), img, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, r, &jpeg.Options{Quality: loveThumbQuality}); err != nil {
		return nil
	}
	if buf.Len() >= len(data) {
		return nil
	}
	return buf.Bytes()
}

// compressLoveImage 把上传的图片压成适合网页展示的大小：
// 最长边不超过 1600px，转 JPEG（质量 82）。
// GIF（可能是动图）和解不出来的原样返回。
// 返回（压完的数据，文件名，内容类型）。
func compressLoveImage(data []byte, filename string) (out []byte, outName string, outType string) {
	ct := http.DetectContentType(data)
	if ct != "image/jpeg" && ct != "image/png" {
		return data, filename, ct
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, filename, ct
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	dst := img
	if longest > loveMaxImageDim {
		scale := float64(loveMaxImageDim) / float64(longest)
		nw := int(float64(w) * scale)
		nh := int(float64(h) * scale)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		r := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(r, r.Bounds(), img, b, draw.Over, nil)
		dst = r
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: loveJPEGQuality}); err != nil {
		return data, filename, ct
	}
	// 压完反而更大（比如纯色小图）就用原图。
	if buf.Len() >= len(data) {
		return data, filename, ct
	}
	name := filename
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".jpg" && ext != ".jpeg" {
		name = strings.TrimSuffix(name, ext) + ".jpg"
	}
	return buf.Bytes(), name, "image/jpeg"
}
