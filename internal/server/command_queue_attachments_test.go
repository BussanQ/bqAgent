package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"bqagent/internal/session"
)

func queueTestImage(t *testing.T) webUIImagePayload {
	t.Helper()
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 1))); err != nil {
		t.Fatal(err)
	}
	return webUIImagePayload{MIMEType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(img.Bytes())}
}

func queueMediaGET(ch *WebUIChannel, id, sessionID, view string, index string) *httptest.ResponseRecorder {
	q := url.Values{"id": {id}, "session_id": {sessionID}, "view": {view}, "index": {index}}
	w := httptest.NewRecorder()
	ch.handleCommandQueue(w, httptest.NewRequest("GET", "/api/v1/webui/command-queue?"+q.Encode(), nil))
	return w
}

func TestWebUIQueuedAttachmentsFreezeWorkspaceFilesAndReachTheSelectedAgent(t *testing.T) {
	client := &imagePromptClient{groupFakeACP: groupFakeACP{name: "opencode"}}
	f := newQueueFixtureWithImages(t, client)
	workspaceFile := filepath.Join(f.s.workspaceRoot, "notes.txt")
	if err := os.WriteFile(workspaceFile, []byte("original workspace notes"), 0600); err != nil {
		t.Fatal(err)
	}
	img := queueTestImage(t)
	uploaded := base64.StdEncoding.EncodeToString([]byte("uploaded content"))
	req := queuedText(f, "media", "/opencode 检查附件")
	req.Images = []webUIImagePayload{img}
	req.Files = []webUIFilePayload{{Path: "notes.txt"}, {Name: "upload.md", DataBase64: &uploaded}}
	v, code, raw := f.req("POST", req)
	if code != 200 || v.Receipt == nil || len(v.Receipt.Attachments) != 3 || v.Receipt.Attachments[1].Path != "notes.txt" ||
		strings.Contains(raw, img.DataBase64) || strings.Contains(raw, "input_attachment_digest") {
		t.Fatal(code, raw)
	}
	q, err := readCommandQueue(f.saved, "")
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := queueBlobPath(q.find(req.ID).Blob)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(f.saved.Dir(), sidecar))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		t.Fatal(info, err)
	}
	if err = os.WriteFile(workspaceFile, []byte("later contents"), 0600); err != nil {
		t.Fatal(err)
	}
	postQueueChange(t, f, req, 200)
	conflict := req
	conflict.Files = []webUIFilePayload{{Path: "another.txt"}}
	postQueueChange(t, f, conflict, 409)
	preview := queueMediaGET(f.ch, req.ID, f.saved.ID(), "attachment", "0")
	if preview.Code != 200 || preview.Header().Get("Content-Type") != "image/png" || base64.StdEncoding.EncodeToString(preview.Body.Bytes()) != img.DataBase64 {
		t.Fatal(preview.Code)
	}
	file := queueMediaGET(f.ch, req.ID, f.saved.ID(), "attachment", "1")
	if file.Code != 200 || file.Body.String() != "original workspace notes" || !strings.Contains(file.Header().Get("Content-Disposition"), "notes.txt") {
		t.Fatal(file.Code, file.Body.String())
	}
	other, err := f.s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := queueMediaGET(f.ch, req.ID, other.ID(), "attachments", ""); got.Code != 404 {
		t.Fatal("attachment crossed sessions", got.Code)
	}
	close(f.provider.release)
	next := f.end(t, "parent", "done").NextTurnID
	done := f.end(t, next, "done")
	if done.Reply != "image received" || len(client.received.Images) != 1 || client.received.Images[0].Base64() != img.DataBase64 ||
		client.received.Model != "chosen" || client.received.Effort != "high" ||
		!strings.Contains(client.prompt, "original workspace notes") || !strings.Contains(client.prompt, "uploaded content") || strings.Contains(client.prompt, "later contents") {
		t.Fatal(done, client.prompt, client.received)
	}
	postQueueChange(t, f, req, 200)
	if _, err = os.Stat(filepath.Join(f.saved.Dir(), sidecar)); !os.IsNotExist(err) {
		t.Fatal("settled blob retained", err)
	}
	if got := queueMediaGET(f.ch, req.ID, f.saved.ID(), "attachments", ""); got.Code != 404 {
		t.Fatal(got.Code)
	}
	history, err := f.s.conversationHistory(f.saved.ID(), 0, true)
	if err != nil || len(history.Messages) != 4 || len(history.Messages[2].Images) != 1 || len(webHistoryPayload(history)["messages"].([]conversationMessage)[2].Files) != 2 {
		t.Fatal(history, err)
	}
}

func TestWebUIQueuedImageOnlySurvivesEditPauseAndRestart(t *testing.T) {
	client := &imagePromptClient{groupFakeACP: groupFakeACP{name: "opencode"}}
	f := newQueueFixtureWithImages(t, client)
	req := queuedText(f, "image", "/opencode")
	req.Images = []webUIImagePayload{queueTestImage(t)}
	postQueueChange(t, f, req, 200)
	edit := queueHold(f, req.ID, "tab", 0)
	postQueueChange(t, f, edit, 200)
	edit.Action, edit.Operation, edit.Text = "edit", "blank", ""
	v := postQueueChange(t, f, edit, 200)
	if len(v.Receipt.Attachments) != 1 || v.Receipt.BodyVersion != 1 {
		t.Fatal(v)
	}
	f.s.StopTurn("parent")
	f.end(t, "parent", "stopped")
	f.shutdown()
	peerService := NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot, ExternalBroker: f.s.externalBroker})
	peer := &queueFixture{ch: NewWebUIChannel(peerService, true)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ch.Start(ctx)
	// Wait for the previous lifetime's lease to drop as in a real restart.
	f.s.webTurns.queueMu.Lock()
	f.s.webTurns.releaseQueue(f.saved.ID())
	f.s.webTurns.queueMu.Unlock()
	view, code, raw := peer.req("GET", req)
	if code != 200 || !view.Paused || len(view.Items[0].Attachments) != 1 {
		t.Fatal(code, raw)
	}
	accepted := postQueueChange(t, peer, commandQueueRequest{Action: "resume", Session: f.saved.ID()}, 200)
	done := peer.end(t, accepted.TurnID, "done")
	if done.Reply != "image received" || client.prompt != "" || len(client.received.Images) != 1 {
		t.Fatal(done, client.prompt)
	}
}

func TestWebUIQueueWithdrawnAttachmentsRemainRecoverableUntilAcknowledged(t *testing.T) {
	f := newQueueFixture(t)
	data := base64.StdEncoding.EncodeToString([]byte("snapshot"))
	req := queuedText(f, "file", "")
	req.Files = []webUIFilePayload{{Name: "review.txt", DataBase64: &data}}
	postQueueChange(t, f, req, 200)
	second := req
	second.ID = "shared"
	postQueueChange(t, f, second, 200)
	for _, r := range []commandQueueRequest{req, second} {
		if v, code, raw := f.req("DELETE", r); code != 200 || v.Receipt.State != "withdrawn" || len(v.Receipt.Attachments) != 1 {
			t.Fatal(code, raw)
		}
	}
	close(f.provider.release)
	if f.end(t, "parent", "done").NextTurnID != "" {
		t.Fatal("withdrawn attachment sent")
	}
	// A different browser can still recover bytes after the DELETE reply is lost.
	ch := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot, ExternalBroker: f.s.externalBroker}), true)
	w := queueMediaGET(ch, req.ID, f.saved.ID(), "attachments", "")
	var payload queueAttachmentPayload
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload.Files) != 1 || payload.Files[0].DataBase64 == nil || *payload.Files[0].DataBase64 != data {
		t.Fatal(w.Code, w.Body.String())
	}
	release := commandQueueRequest{Action: "release_attachments", ID: req.ID, Session: f.saved.ID()}
	v := postQueueChange(t, f, release, 200)
	postQueueChange(t, f, release, 200)
	if len(v.Items) != 1 || v.Items[0].ID != "shared" || queueMediaGET(ch, req.ID, f.saved.ID(), "attachment", "0").Code != 404 ||
		queueMediaGET(ch, second.ID, f.saved.ID(), "attachment", "0").Body.String() != "snapshot" {
		t.Fatal(v)
	}
	release.ID = second.ID
	postQueueChange(t, f, release, 200)
	postQueueChange(t, f, req, 200)
	if entries, err := os.ReadDir(filepath.Join(f.saved.Dir(), queueAttachmentDir)); err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestWebUIQueueRejectsUnsupportedInvalidAndForeignAttachmentsBeforeAdmission(t *testing.T) {
	f := newQueueFixture(t)
	defer func() { close(f.provider.release); f.end(t, "parent", "done") }()
	img := queueTestImage(t)
	bad := queuedText(f, "bad", "question")
	bad.Images = []webUIImagePayload{img} // ACP transport did not declare image support.
	postQueueChange(t, f, bad, 400)
	bad.Agent = builtinChatAgent
	bad.Model = ""
	bad.Effort = ""
	bad.Images[0].MIMEType = "image/jpeg"
	postQueueChange(t, f, bad, 400)
	bad.Images = nil
	path := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	bad.Files = []webUIFilePayload{{Path: path}}
	postQueueChange(t, f, bad, 400)
	t.Run("symlink escape", func(t *testing.T) {
		if err := os.Symlink(path, filepath.Join(f.s.workspaceRoot, "link")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("cannot create test symlink:", err)
			}
			t.Fatal(err)
		}
		link := bad
		link.Files = []webUIFilePayload{{Path: "link"}}
		postQueueChange(t, f, link, 400)
	})
	bad.Kind, bad.Text = "", "/compact"
	postQueueChange(t, f, bad, 400)
	bad.Action = "resume"
	postQueueChange(t, f, bad, 400)
	for _, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin"} {
		if err := os.WriteFile(filepath.Join(f.s.workspaceRoot, name), make([]byte, maxWebUIFileBytes), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bad.Action, bad.Kind, bad.Text = "enqueue", "message", ""
	bad.Files = nil
	for _, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin"} {
		bad.Files = append(bad.Files, webUIFilePayload{Path: name})
	}
	postQueueChange(t, f, bad, 413)
	if _, err := os.Stat(filepath.Join(f.saved.Dir(), queueAttachmentDir)); !os.IsNotExist(err) {
		t.Fatal("invalid payload wrote a blob", err)
	}
}

func TestWebUIQueueCorruptAttachmentFailsWithoutSendingTextAndPausesFollowers(t *testing.T) {
	client := &imagePromptClient{groupFakeACP: groupFakeACP{name: "opencode"}}
	f := newQueueFixtureWithImages(t, client)
	req := queuedText(f, "damaged", "examine")
	req.Images = []webUIImagePayload{queueTestImage(t)}
	postQueueChange(t, f, req, 200)
	postQueueChange(t, f, queuedText(f, "later", "follow"), 200)
	q, err := readCommandQueue(f.saved, "")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := queueBlobPath(q.find(req.ID).Blob)
	if err = os.WriteFile(filepath.Join(f.saved.Dir(), path), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	close(f.provider.release)
	next := f.end(t, "parent", "done").NextTurnID
	f.end(t, next, "error")
	v, code, raw := f.req("GET", req)
	if code != 200 || !v.Paused || v.Receipt.State != "failed" || !strings.Contains(v.Receipt.Error, "校验失败") || client.prompt != "" || client.received.Model != "" {
		t.Fatal(raw)
	}
}

func TestWebUIQueuedAttachmentEditsKeepOriginalBytesAndRetryExactMutation(t *testing.T) {
	client := &imagePromptClient{groupFakeACP: groupFakeACP{name: "opencode"}}
	f := newQueueFixtureWithImages(t, client)
	tmp := filepath.Join(f.s.workspaceRoot, "source.md")
	if err := os.WriteFile(tmp, []byte("from enqueue"), 0600); err != nil {
		t.Fatal(err)
	}
	req := queuedText(f, "card", "draft")
	originalImage := queueTestImage(t)
	req.Images = []webUIImagePayload{originalImage}
	req.Files = []webUIFilePayload{{Path: "source.md"}}
	postQueueChange(t, f, req, 200)
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	edit := queueHold(f, req.ID, "edit", 0)
	postQueueChange(t, f, edit, 200)
	edit.Action, edit.Operation, edit.Text = "edit", "save-media", ""
	indices := []int{1, 1}
	edit.KeepAttachments = &indices
	postQueueChange(t, f, edit, 400)
	added := base64.StdEncoding.EncodeToString([]byte("new file"))
	indices = []int{1}
	edit.Files = []webUIFilePayload{{Name: "added.md", DataBase64: &added}}
	v := postQueueChange(t, f, edit, 200)
	if len(v.Receipt.Attachments) != 2 || v.Receipt.Attachments[0].Path != "source.md" || v.Receipt.Attachments[1].Name != "added.md" || v.Receipt.BodyVersion != 1 || v.Receipt.Picks.Effort != "high" {
		t.Fatal(v)
	}
	postQueueChange(t, f, req, 200)
	postQueueChange(t, f, edit, 200)
	change := edit
	indicesChanged := []int{}
	change.KeepAttachments = &indicesChanged
	postQueueChange(t, f, change, 409)
	proof := httptest.NewRecorder()
	f.ch.handleCommandQueue(proof, httptest.NewRequest("GET", "/api/v1/webui/command-queue?session_id="+f.saved.ID()+"&id=card&operation_id=save-media", nil))
	if !strings.Contains(proof.Body.String(), `"operation_committed":true`) || strings.Contains(proof.Body.String(), added) {
		t.Fatal(proof.Body.String())
	}
	hold := queueHold(f, req.ID, "second-edit", 1)
	postQueueChange(t, f, hold, 200)
	hold.Action, hold.Operation, hold.KeepAttachments = "edit", "drop-all", &indicesChanged
	postQueueChange(t, f, hold, 400) // Empty message with no remaining attachment.
	hold.Action, hold.KeepAttachments = "release", nil
	postQueueChange(t, f, hold, 200)
	close(f.provider.release)
	next := f.end(t, "parent", "done").NextTurnID
	f.end(t, next, "done")
	if !strings.Contains(client.prompt, "from enqueue") || !strings.Contains(client.prompt, "new file") || len(client.received.Images) != 0 || client.received.Model != "chosen" {
		t.Fatal(client.prompt, client.received)
	}
	postQueueChange(t, f, edit, 200)
	if files, err := os.ReadDir(filepath.Join(f.saved.Dir(), queueAttachmentDir)); err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
}

func TestWebUIQueueAttachmentGCReclaimsUncommittedBytesWithoutTouchingOtherFiles(t *testing.T) {
	f := newQueueFixture(t)
	defer func() { close(f.provider.release); f.end(t, "parent", "done") }()
	data := base64.StdEncoding.EncodeToString([]byte("not committed"))
	orphan, err := writeQueueBundle(f.saved, queueAttachmentPayload{Files: []webUIFilePayload{{Name: "file", DataBase64: &data}}})
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.saved.Dir(), queueAttachmentDir, "other.txt")
	if err = os.WriteFile(other, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(f.saved.Dir(), queueAttachmentDir, "."+orphan+".json."+strings.Repeat("1", 32)+".tmp")
	if err = os.WriteFile(partial, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	req := queuedText(f, "question", "next")
	postQueueChange(t, f, req, 200)
	path, _ := queueBlobPath(orphan)
	if _, err = os.Stat(filepath.Join(f.saved.Dir(), path)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(other); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	_, _, _ = f.req("DELETE", req)
}
