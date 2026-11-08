package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"bqagent/internal/atomicfile"
	"bqagent/internal/privatefile"
	"bqagent/internal/session"
)

const queueAttachmentDir = "webui-queue-attachments"

type queueAttachmentInfo struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
	MIME string `json:"mime_type,omitempty"`
	Size int    `json:"size"`
}

// Contents are written once and never placed in queue snapshots or the edit
// journal. The input digest identifies the user's retry, even if a referenced
// workspace file changed after its bytes were captured.
type queueAttachmentPayload struct {
	Images []webUIImagePayload `json:"images,omitempty"`
	Files  []webUIFilePayload  `json:"files,omitempty"`
}

func queueBundleDigest(payload queueAttachmentPayload) string {
	data, _ := json.Marshal(payload)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func queueAttachmentStatus(err error) int {
	var requestErr *webUIRequestError
	if errors.As(err, &requestErr) {
		return requestErr.Status
	}
	return http.StatusBadRequest
}

func (svc *Service) snapshotQueueAttachments(images []webUIImagePayload, files []webUIFilePayload) (queueAttachmentPayload, []queueAttachmentInfo, error) {
	var payload queueAttachmentPayload
	decoded, uploads, err := decodeWebUIAttachments(images, files)
	if err != nil {
		return payload, nil, err
	}
	var summary []queueAttachmentInfo
	for _, img := range decoded {
		payload.Images = append(payload.Images, webUIImagePayload{MIMEType: img.ContentType(), DataBase64: img.Base64()})
		summary = append(summary, queueAttachmentInfo{Kind: "image", MIME: img.ContentType(), Size: len(img.Data)})
	}
	var root *os.Root
	total := 0
	for _, file := range uploads {
		name, data := file.Name, file.Data
		var source string
		if file.Path != "" {
			if root == nil {
				root, err = os.OpenRoot(svc.workspaceRoot)
				if err != nil {
					return payload, nil, err
				}
				defer root.Close()
			}
			frozen, err := svc.readWorkspaceAttachment(root, file.Path)
			if err != nil {
				return payload, nil, err
			}
			name, data, source = frozen.Name, frozen.Data, frozen.Path
		}
		name, err = sanitizeUploadName(name)
		if err != nil {
			return payload, nil, err
		}
		total += len(data)
		if total > maxWebUITotalFileBytes {
			return payload, nil, &webUIRequestError{Status: http.StatusRequestEntityTooLarge, Err: fmt.Errorf("文件总大小不能超过 6 MiB")}
		}
		base64Data := base64.StdEncoding.EncodeToString(data)
		payload.Files = append(payload.Files, webUIFilePayload{Name: name, DataBase64: &base64Data})
		summary = append(summary, queueAttachmentInfo{Kind: "file", Name: name, Path: source, Size: len(data)})
	}
	return payload, summary, nil
}

func queueBlobPath(digest string) (string, error) {
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("无效的队列附件引用")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("无效的队列附件引用")
	}
	return filepath.Join(queueAttachmentDir, digest+".json"), nil
}

func writeQueueBundle(s *session.Session, payload queueAttachmentPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	path, _ := queueBlobPath(digest)
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.MkdirAll(queueAttachmentDir, privatefile.DirMode); err != nil {
		return "", err
	}
	return digest, atomicfile.WriteRoot(root, path, data, privatefile.FileMode)
}

// An edit keeps selected immutable snapshots by index. Newly attached paths
// are frozen now, while an unchanged file keeps the bytes from its enqueue.
func (svc *Service) editQueueAttachments(ctx context.Context, s *session.Session, q *commandQueue, row *queuedCommand, req commandQueueRequest) (bool, error) {
	keep := *req.KeepAttachments
	if len(keep)+len(req.Images)+len(req.Files) > maxWebUIImages+maxWebUIFiles {
		return false, fmt.Errorf("附件数量过多")
	}
	var payload queueAttachmentPayload
	var images, files []queueAttachmentInfo
	if len(keep) > 0 {
		old, err := readQueueBundle(s, row.Blob)
		if err != nil {
			return false, &webUIRequestError{Status: 500, Err: fmt.Errorf("已排队附件无法读取：%w", err)}
		}
		if len(row.Attachments) != len(old.Images)+len(old.Files) {
			return false, fmt.Errorf("队列附件索引无效")
		}
		seen := map[int]bool{}
		for _, idx := range keep {
			if idx < 0 || idx >= len(row.Attachments) || seen[idx] {
				return false, fmt.Errorf("附件索引无效或重复")
			}
			seen[idx] = true
			if idx < len(old.Images) {
				payload.Images = append(payload.Images, old.Images[idx])
				images = append(images, row.Attachments[idx])
			} else {
				payload.Files = append(payload.Files, old.Files[idx-len(old.Images)])
				files = append(files, row.Attachments[idx])
			}
		}
	}
	added, metadata, err := svc.snapshotQueueAttachments(req.Images, req.Files)
	if err != nil {
		return false, err
	}
	payload.Images = append(payload.Images, added.Images...)
	payload.Files = append(payload.Files, added.Files...)
	images = append(images, metadata[:len(added.Images)]...)
	files = append(files, metadata[len(added.Images):]...)
	result := append(images, files...)
	if _, _, err = decodeWebUIAttachments(payload.Images, payload.Files); err != nil {
		return false, err
	}
	if len(payload.Images) > 0 && row.Agent != builtinChatAgent {
		menu, err := svc.externalBroker.ModelCapabilities(ctx, row.Agent, s.ID(), svc.workspaceRoot)
		if err != nil {
			return false, err
		}
		if !menu.SupportsImages {
			return false, fmt.Errorf("%s 的当前接口不支持图片", row.Agent)
		}
	}
	blob := ""
	if len(result) > 0 {
		blob = queueBundleDigest(payload)
		if blob != row.Blob {
			if _, err := writeQueueBundle(s, payload); err != nil {
				return false, &webUIRequestError{Status: 500, Err: err}
			}
		}
	}
	if blob == row.Blob && slices.Equal(result, row.Attachments) {
		return false, nil
	}
	if row.Blob != "" && row.Blob != blob {
		q.released = append(q.released, row.Blob)
	}
	row.Blob, row.Attachments = blob, result
	return true, nil
}

func readQueueBundle(s *session.Session, digest string) (queueAttachmentPayload, error) {
	var payload queueAttachmentPayload
	path, err := queueBlobPath(digest)
	if err != nil {
		return payload, err
	}
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return payload, err
	}
	defer root.Close()
	f, err := root.Open(path)
	if err != nil {
		return payload, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return payload, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxWebUIRequestBodyBytes {
		return payload, fmt.Errorf("队列附件格式或大小无效")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWebUIRequestBodyBytes+1))
	if err != nil {
		return payload, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
		return payload, fmt.Errorf("队列附件内容校验失败")
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func (svc *Service) loadQueueTurnAttachments(req *TurnRequest) error {
	s, err := svc.store.Open(req.SessionID)
	if err != nil {
		return err
	}
	payload, err := readQueueBundle(s, req.queuedBlob)
	if err != nil {
		return err
	}
	for _, file := range payload.Files {
		if file.Path != "" {
			return fmt.Errorf("队列附件缺少入队时的文件快照")
		}
	}
	if len(payload.Files)+len(payload.Images) == 0 {
		return fmt.Errorf("队列附件为空")
	}
	req.Images, req.Files, err = decodeWebUIAttachments(payload.Images, payload.Files)
	return err
}

func (q *commandQueue) referencesBlob(digest string) bool {
	for _, h := range q.History {
		if h.Blob == digest {
			return true
		}
	}
	for _, row := range q.Entries {
		if row.Blob == digest {
			return true
		}
	}
	return false
}

func (q *commandQueue) removeReleasedAttachments(root *os.Root) {
	for _, digest := range q.released {
		path, err := queueBlobPath(digest)
		if err == nil && !q.referencesBlob(digest) {
			if err = root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("remove settled queue attachment: %v", err)
			}
		}
	}
	q.released = nil
}

// Recover writes interrupted between the blob and journal commits. Only the
// process holding the session's queue lease may collect unreferenced blobs.
func (q *commandQueue) pruneAttachmentDirectory(s *session.Session) {
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return
	}
	defer root.Close()
	dir, err := root.Open(queueAttachmentDir)
	if err != nil {
		return
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return
	}
	for _, name := range names {
		if digest, ok := strings.CutSuffix(name, ".json"); ok {
			if path, err := queueBlobPath(digest); err == nil && !q.referencesBlob(digest) {
				_ = root.Remove(path)
			}
		} else if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") {
			// atomicfile writes .<digest>.json.<random>.tmp before renaming.
			parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "."), ".tmp"), ".")
			if len(parts) == 3 && parts[1] == "json" && len(parts[2]) == 32 {
				_, digestErr := queueBlobPath(parts[0])
				_, suffixErr := hex.DecodeString(parts[2])
				if digestErr == nil && suffixErr == nil {
					_ = root.Remove(filepath.Join(queueAttachmentDir, name))
				}
			}
		}
	}
}

func (ch *WebUIChannel) serveQueueAttachments(w http.ResponseWriter, svc *Service, req commandQueueRequest, query url.Values) {
	if !validTurnID(req.ID) {
		writeError(w, 400, chatResponse{Error: "无效的条目 ID"})
		return
	}
	// Pin the bytes before a concurrent withdrawal drops their last reference.
	payload, err := func() (queueAttachmentPayload, error) {
		svc.webTurns.queueMu.Lock()
		defer svc.webTurns.queueMu.Unlock()
		var empty queueAttachmentPayload
		s, err := svc.store.Open(req.Session)
		if err != nil {
			return empty, err
		}
		if !s.Meta().Chat {
			return empty, os.ErrNotExist
		}
		q, err := readCommandQueue(s, "")
		if err != nil {
			return empty, err
		}
		if query.Get("source") == "handoff" {
			h, ok := q.History[req.ID]
			if !ok || h.Blob == "" {
				return empty, os.ErrNotExist
			}
			return readQueueBundle(s, h.Blob)
		}
		row := q.find(req.ID)
		if row == nil || row.Blob == "" {
			return empty, os.ErrNotExist
		}
		return readQueueBundle(s, row.Blob)
	}()
	if err != nil {
		status := 500
		if errors.Is(err, os.ErrNotExist) {
			status = 404
		}
		writeError(w, status, chatResponse{Error: "队列附件无法读取"})
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if query.Get("view") == "attachments" {
		writeJSON(w, 200, payload)
		return
	}
	index, err := strconv.Atoi(query.Get("index"))
	if err != nil || index < 0 || index >= len(payload.Images)+len(payload.Files) {
		writeError(w, 404, chatResponse{Error: "附件不存在"})
		return
	}
	var data []byte
	if index < len(payload.Images) {
		img, err := decodeWebUIImage(payload.Images[index])
		if err != nil {
			writeError(w, 500, chatResponse{Error: "图片无法读取"})
			return
		}
		w.Header().Set("Content-Type", img.ContentType())
		data = img.Data
	} else {
		file, err := decodeWebUIFile(payload.Files[index-len(payload.Images)])
		if err != nil || file.Path != "" {
			writeError(w, 500, chatResponse{Error: "文件无法读取"})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
		data = file.Data
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
