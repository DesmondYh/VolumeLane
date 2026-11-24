package main

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxTextBytes = 2 << 20
const maxReplaceBytes = 64 << 20

type fileDetails struct {
	Version  string  `json:"version,omitempty"`
	Text     *string `json:"text,omitempty"`
	Image    bool    `json:"image"`
	Writable bool    `json:"writable"`
}

func (a *Agent) fileParent(id, relative string) (*os.Root, string, error) {
	if !validPath(id, relative) {
		return nil, "", os.ErrPermission
	}
	location, err := a.location(id)
	if err != nil {
		return nil, "", err
	}
	root, err := location.openRoot()
	if err != nil {
		return nil, "", err
	}
	if relative == "" {
		return root, ".", nil
	}
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		info, err := root.Lstat(part)
		if err != nil || !info.IsDir() {
			root.Close()
			if err == nil {
				err = os.ErrPermission
			}
			return nil, "", err
		}
		next, err := root.OpenRoot(part)
		root.Close()
		if err != nil {
			return nil, "", err
		}
		if err := verifyVolume(next, location); err != nil {
			next.Close()
			return nil, "", err
		}
		root = next
	}
	return root, parts[len(parts)-1], nil
}

func openSelected(root *os.Root, leaf string, flags int) (*os.File, error) {
	info, err := root.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	return root.OpenFile(leaf, flags|openSelectedFlags, 0)
}

func fileError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrPermission) {
		apiError(w, http.StatusForbidden, "没有访问此文件的权限，或所在磁盘为只读。")
	} else if errors.Is(err, os.ErrNotExist) {
		apiError(w, http.StatusNotFound, "此文件或目录目前不可用。")
	} else {
		apiError(w, http.StatusConflict, "无法完成文件操作，请检查磁盘空间和权限。")
	}
}

func editableMode(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0222 != 0 && info.Mode()&(os.ModeSetuid|os.ModeSetgid) == 0 && info.Size() <= maxReplaceBytes
}

func imageContentType(data []byte) string {
	switch kind := http.DetectContentType(data); kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return kind
	default:
		return ""
	}
}

func fileRevision(reader io.Reader) (string, []byte, error) {
	digest := sha256.New()
	limited := &io.LimitedReader{R: reader, N: maxReplaceBytes + 1}
	stream := io.TeeReader(limited, digest)
	prefix, err := io.ReadAll(io.LimitReader(stream, maxTextBytes+1))
	if err == nil {
		_, err = io.Copy(io.Discard, stream)
	}
	if err != nil {
		return "", nil, err
	}
	if limited.N == 0 {
		return "", nil, errors.New("file exceeds the replacement size limit")
	}
	return fmt.Sprintf(`"%x"`, digest.Sum(nil)), prefix, nil
}

func isText(data []byte) bool {
	if len(data) > maxTextBytes || !utf8.Valid(data) {
		return false
	}
	for _, c := range string(data) {
		if unicode.IsControl(c) && c != '\t' && c != '\n' && c != '\r' && c != '\f' {
			return false
		}
	}
	return true
}

func (a *Agent) readFileDetails(w http.ResponseWriter, r *http.Request) {
	file, _ := a.open(w, r)
	if file == nil {
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		apiError(w, http.StatusBadRequest, "请选择一个文件。")
		return
	}
	if info.Size() > maxReplaceBytes {
		prefix, _ := io.ReadAll(io.LimitReader(file, 512))
		writeJSON(w, fileDetails{Image: imageContentType(prefix) != ""})
		return
	}
	version, prefix, err := fileRevision(file)
	if err != nil {
		fileError(w, err)
		return
	}
	details := fileDetails{Version: version, Writable: editableMode(info), Image: imageContentType(prefix) != ""}
	if isText(prefix) {
		text := string(prefix)
		details.Text = &text
	}
	writeJSON(w, details)
}

func (a *Agent) previewImage(w http.ResponseWriter, r *http.Request) {
	file, relative := a.open(w, r)
	if file == nil {
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		apiError(w, http.StatusBadRequest, "请选择一个图片文件。")
		return
	}
	prefix, err := io.ReadAll(io.LimitReader(file, 512))
	kind := imageContentType(prefix)
	if err != nil || kind == "" {
		apiError(w, http.StatusUnsupportedMediaType, "这种图片格式请下载查看。")
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		fileError(w, err)
		return
	}
	w.Header().Set("Content-Type", kind)
	http.ServeContent(w, r, path.Base(relative), info.ModTime(), file)
}

func (a *Agent) saveFile(w http.ResponseWriter, r *http.Request) {
	ifMatch := r.Header.Get("If-Match")
	if len(ifMatch) != 66 || ifMatch[0] != '"' || ifMatch[65] != '"' {
		apiError(w, http.StatusPreconditionRequired, "请重新打开文件后再保存。")
		return
	}
	if r.ContentLength > maxReplaceBytes {
		apiError(w, http.StatusRequestEntityTooLarge, "文件最大支持 64 MiB。")
		return
	}
	location, relative := r.URL.Query().Get("location"), r.URL.Query().Get("path")
	if relative == "" {
		apiError(w, http.StatusBadRequest, "请选择一个文件。")
		return
	}
	root, leaf, err := a.fileParent(location, relative)
	if err != nil {
		fileError(w, err)
		return
	}
	defer root.Close()
	// Stage the upload alongside its destination so a failed write never truncates the original.
	temporary := ".device-files-" + rand.Text()
	output, err := root.OpenFile(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fileError(w, err)
		return
	}
	defer root.Remove(temporary)
	defer output.Close()
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, digest), http.MaxBytesReader(w, r.Body, maxReplaceBytes)); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apiError(w, http.StatusRequestEntityTooLarge, "文件最大支持 64 MiB。")
		} else {
			fileError(w, err)
		}
		return
	}

	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	original, err := openSelected(root, leaf, os.O_RDWR)
	if err != nil {
		fileError(w, err)
		return
	}
	defer original.Close()
	info, err := original.Stat()
	if err != nil || !editableMode(info) {
		apiError(w, http.StatusForbidden, "这个文件无法在网页中保存。")
		return
	}
	current, _, err := fileRevision(original)
	if err != nil {
		fileError(w, err)
		return
	}
	if current != ifMatch {
		apiError(w, http.StatusPreconditionFailed, "文件已在别处修改。请先复制你的编辑内容，再重新打开文件。")
		return
	}
	if err := copyFileMetadata(original, output, info); err != nil {
		apiError(w, http.StatusConflict, "无法保留原文件的权限或属性，文件没有保存。")
		return
	}
	now := time.Now()
	if err := root.Chtimes(temporary, now, now); err != nil {
		fileError(w, err)
		return
	}
	if err := output.Sync(); err != nil {
		fileError(w, err)
		return
	}
	if err := output.Close(); err != nil {
		fileError(w, err)
		return
	}
	latest, err := root.Lstat(leaf)
	if err != nil || !os.SameFile(info, latest) || latest.Size() != info.Size() || !latest.ModTime().Equal(info.ModTime()) {
		apiError(w, http.StatusPreconditionFailed, "文件已在别处修改。请先复制你的编辑内容，再重新打开文件。")
		return
	}
	if err := root.Rename(temporary, leaf); err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, map[string]string{"version": fmt.Sprintf(`"%x"`, digest.Sum(nil))})
}

func (a *Agent) deleteFile(w http.ResponseWriter, r *http.Request) {
	location, relative := r.URL.Query().Get("location"), r.URL.Query().Get("path")
	if relative == "" {
		apiError(w, http.StatusBadRequest, "请选择一个文件。")
		return
	}
	root, leaf, err := a.fileParent(location, relative)
	if err != nil {
		fileError(w, err)
		return
	}
	defer root.Close()
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	info, err := root.Lstat(leaf)
	if err != nil {
		fileError(w, err)
		return
	}
	if info.IsDir() {
		if location != "workspace" || r.URL.Query().Get("kind") != "folder" {
			apiError(w, http.StatusBadRequest, "只能从共享空间删除空文件夹。")
			return
		}
		if err := root.Remove(leaf); err != nil {
			apiError(w, http.StatusConflict, "无法删除文件夹，请确认它是空的且有删除权限。")
			return
		}
		writeJSON(w, map[string]bool{"deleted": true})
		return
	}
	if !info.Mode().IsRegular() || r.URL.Query().Get("kind") == "folder" {
		apiError(w, http.StatusForbidden, "只能删除普通文件。")
		return
	}
	if err := root.Remove(leaf); err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}

func (a *Agent) makeFolder(w http.ResponseWriter, r *http.Request) {
	location, relative := r.URL.Query().Get("location"), r.URL.Query().Get("path")
	if location != "workspace" || relative == "" {
		apiError(w, http.StatusBadRequest, "请选择共享空间内的新文件夹位置。")
		return
	}
	root, leaf, err := a.fileParent(location, relative)
	if err != nil {
		fileError(w, err)
		return
	}
	defer root.Close()
	if err := root.Mkdir(leaf, 0700); err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"created": true})
}

func (a *Agent) uploadWorkspace(w http.ResponseWriter, r *http.Request, root *os.Root, leaf string) {
	const uploadLimit = 1 << 30
	if r.ContentLength > uploadLimit {
		apiError(w, http.StatusRequestEntityTooLarge, "共享空间单个文件最大支持 1 GiB。")
		return
	}
	name := ".device-files-" + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|openSelectedFlags, 0600)
	if err != nil {
		fileError(w, err)
		return
	}
	defer root.Remove(name)
	digest := sha256.New()
	_, err = io.Copy(io.MultiWriter(file, digest), http.MaxBytesReader(w, r.Body, uploadLimit))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apiError(w, http.StatusRequestEntityTooLarge, "共享空间单个文件最大支持 1 GiB。")
			return
		}
		fileError(w, err)
		return
	}
	if closeErr != nil {
		fileError(w, closeErr)
		return
	}
	// A same-volume link publishes the completed file without overwriting a concurrent upload.
	if err := root.Link(name, leaf); err != nil {
		if errors.Is(err, os.ErrExist) {
			apiError(w, http.StatusConflict, "同名文件已存在。")
			return
		}
		fileError(w, err)
		return
	}
	writeJSON(w, map[string]string{"version": fmt.Sprintf(`"%x"`, digest.Sum(nil))})
}

func (a *Agent) uploadFile(w http.ResponseWriter, r *http.Request) {
	location, relative := r.URL.Query().Get("location"), r.URL.Query().Get("path")
	if location != "workspace" && r.ContentLength > maxReplaceBytes {
		apiError(w, http.StatusRequestEntityTooLarge, "文件最大支持 64 MiB。")
		return
	}
	if relative == "" {
		apiError(w, http.StatusBadRequest, "请选择上传位置。")
		return
	}
	root, leaf, err := a.fileParent(location, relative)
	if err != nil {
		fileError(w, err)
		return
	}
	defer root.Close()
	if location == "workspace" {
		a.uploadWorkspace(w, r, root, leaf)
		return
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if _, err := root.Lstat(leaf); err == nil {
		apiError(w, http.StatusConflict, "同名文件已存在，请先删除或改名后再上传。")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		fileError(w, err)
		return
	}
	file, err := root.OpenFile(leaf, os.O_WRONLY|os.O_CREATE|os.O_EXCL|openSelectedFlags, 0644)
	if err != nil {
		fileError(w, err)
		return
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, digest), http.MaxBytesReader(w, r.Body, maxReplaceBytes)); err != nil {
		root.Remove(leaf)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apiError(w, http.StatusRequestEntityTooLarge, "文件最大支持 64 MiB。")
		} else {
			fileError(w, err)
		}
		return
	}
	if err := file.Sync(); err != nil {
		root.Remove(leaf)
		fileError(w, err)
		return
	}
	writeJSON(w, map[string]string{"version": fmt.Sprintf(`"%x"`, digest.Sum(nil))})
}
