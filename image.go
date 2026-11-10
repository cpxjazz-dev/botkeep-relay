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
