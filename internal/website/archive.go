package website

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
)

const maxZIPSize = 512 << 10
const maxExpandedSize = 3 << 20

type Files map[string][]byte

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".txt": "text/plain; charset=utf-8",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp",
	".gif": "image/gif", ".ico": "image/x-icon", ".avif": "image/avif", ".svg": "image/svg+xml",
	".woff": "font/woff", ".woff2": "font/woff2",
}

const ArchivePolicy = "sandbox; default-src 'none'; style-src 'self' 'unsafe-inline' https:; img-src 'self' data: https:; font-src 'self' data: https:; base-uri 'none'; form-action 'none'"

func ReadArchive(encoded string) (Files, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxZIPSize) {
		return nil, errors.New("ZIP должен быть не больше 512 КиБ")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) > maxZIPSize || base64.StdEncoding.EncodeToString(raw) != encoded {
		return nil, errors.New("Неверный ZIP или его размер превышает 512 КиБ")
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(z.File) > 128 {
		return nil, errors.New("Нужен ZIP с не более чем 128 записями")
	}
	out := make(Files)
	seen := make(map[string]bool)
	remaining := maxExpandedSize
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !validArchivePath(name) || seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("Недопустимый или повторяющийся путь в ZIP: %q", name)
		}
		seen[strings.ToLower(name)] = true
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() || contentTypes[strings.ToLower(path.Ext(name))] == "" {
			return nil, fmt.Errorf("ZIP поддерживает только статические файлы: %q", name)
		}
		if f.UncompressedSize64 > uint64(remaining) {
			return nil, errors.New("Размер распакованного сайта превышает 3 МиБ")
		}
		r, err := f.Open()
		if err != nil {
			return nil, errors.New("Не удалось открыть файл из ZIP")
		}
		data, err := io.ReadAll(io.LimitReader(r, int64(remaining)+1))
		r.Close()
		if err != nil || len(data) > remaining {
			return nil, errors.New("Не удалось распаковать файл или сайт превышает 3 МиБ")
		}
		out[name] = data
		remaining -= len(data)
	}
	if len(out["index.html"]) == 0 {
		return nil, errors.New("В корне ZIP нужен непустой index.html")
	}
	for name := range out {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := out[parent]; exists {
				return nil, fmt.Errorf("В ZIP один путь используется и как файл, и как каталог: %q", parent)
			}
		}
	}
	return out, nil
}

func validArchivePath(name string) bool {
	if name == "" || len(name) > 240 || strings.ContainsAny(name, "\\:?#%") || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func (files Files) Lookup(name string) ([]byte, string, bool) {
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	data, ok := files[name]
	return data, contentTypes[strings.ToLower(path.Ext(name))], ok
}
