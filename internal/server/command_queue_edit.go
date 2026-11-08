package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"time"

	"bqagent/internal/session"
)

const queueEditLifetime = 2 * time.Minute

// Edit tokens are private to the browser that generated them. Only the held
// flag goes in the shared snapshot. A restarted owner drops abandoned holds.
type queueEditHold struct {
	row     string
	version uint64
	until   time.Time
	timer   *time.Timer
}

func (req commandQueueRequest) validateEditAction() error {
	switch req.Action {
	case "hold", "renew", "release", "edit":
		if !validTurnID(req.EditID) || ((req.Action == "hold" || req.Action == "edit") && req.BodyVersion == nil) {
			return fmt.Errorf("缺少有效的编辑 ID 或内容版本")
		}
	case "move":
		if !validTurnID(req.Anchor) || (req.Position != "before" && req.Position != "after") {
			return fmt.Errorf("需要有效的相邻条目与移动方向")
		}
	default:
		return nil
	}
	if !validTurnID(req.ID) || ((req.Action == "edit" || req.Action == "move") && !validTurnID(req.Operation)) {
		return fmt.Errorf("无效的队列条目或操作 ID")
	}
	return nil
}

func (req commandQueueRequest) mutationDigest() string {
	fields := []any{req.Action, req.ID, req.EditID, req.BodyVersion, req.Text, req.Anchor, req.Position}
	if req.KeepAttachments != nil {
		fields = append(fields, *req.KeepAttachments, req.attachmentDigest)
	}
	data, _ := json.Marshal(fields)
	return queueTextDigest(string(data))
}

func (q *commandQueue) mutationReplay(req commandQueueRequest) (bool, error) {
	if req.Action != "edit" && req.Action != "move" {
		return false, nil
	}
	if _, exists := q.Sends[req.Operation]; exists {
		return true, fmt.Errorf("操作 ID 已用于立即发送")
	}
	digest, ok := q.Mutations[req.Operation]
	if ok && digest != req.mutationDigest() {
		return true, fmt.Errorf("队列操作 ID 已用于其他修改")
	}
	return ok, nil
}

// Preserve the positions of running and settled entries, including receipts
// that are no longer visible. Only waiting entries can be a move anchor.
func (q *commandQueue) move(id, anchor, position string) error {
	var slots []int
	var waiting []queuedCommand
	from, to := -1, -1
	for i, row := range q.Entries {
		if row.State != "waiting" {
			continue
		}
		if row.ID == id {
			from = len(waiting)
		}
		if row.ID == anchor {
			to = len(waiting)
		}
		slots = append(slots, i)
		waiting = append(waiting, row)
	}
	if from < 0 || to < 0 {
		return fmt.Errorf("条目或相邻条目已离开等待队列")
	}
	if from == to {
		return nil
	}
	row := waiting[from]
	waiting = slices.Delete(waiting, from, from+1)
	if from < to {
		to--
	}
	if position == "after" {
		to++
	}
	waiting = slices.Insert(waiting, to, row)
	for i, slot := range slots {
		q.Entries[slot] = waiting[i]
	}
	return nil
}

func (l *queueLease) removeEdit(key string) {
	if hold := l.editors[key]; hold != nil {
		hold.timer.Stop()
		delete(l.editors, key)
	}
}

func (l *queueLease) retireEdits(row string) {
	for key, hold := range l.editors {
		if hold.row == row {
			l.removeEdit(key)
		}
	}
}

func (l *queueLease) hasOtherEdit(row, key string) bool {
	for token, hold := range l.editors {
		if token != key && hold.row == row {
			return true
		}
	}
	return false
}

func (ch *WebUIChannel) saveQueueEdit(svc *Service, info WorkspaceInfo, s *session.Session, q *commandQueue, drain bool) error {
	_, start, err := ch.commitCommandQueue(svc, info, s, q, "", drain)
	if err == nil && start != nil {
		start()
	}
	return err
}

// All transitions run under queueMu, including parent completion, Stop and
// timer expiry. A last release can hand off an idle head in the same commit.
func (ch *WebUIChannel) editCommandQueue(ctx context.Context, svc *Service, info WorkspaceInfo, s *session.Session, q *commandQueue, req commandQueueRequest) (commandQueueView, int, error) {
	var empty commandQueueView
	view := func() commandQueueView { return q.view(s.ID(), req.ID) }
	if replay, err := q.mutationReplay(req); replay {
		if err != nil {
			return empty, 409, err
		}
		return view(), 200, nil
	}
	l := svc.webTurns.queueLeases[s.ID()]
	hold := l.editors[req.EditID]
	if hold != nil && hold.row != req.ID {
		return empty, 409, fmt.Errorf("编辑 ID 已用于其他条目")
	}
	if req.Action == "release" {
		if err := ch.releaseQueueEdit(svc, info, s, q, l, req.EditID); err != nil {
			return empty, 500, err
		}
		return view(), 200, nil
	}

	row := q.find(req.ID)
	if row == nil || row.State != "waiting" {
		return empty, 409, fmt.Errorf("条目已离开等待队列")
	}
	if req.BodyVersion != nil && row.BodyVersion != *req.BodyVersion {
		return empty, 409, fmt.Errorf("条目已被修改，请核对最新内容")
	}
	changedAttachments := false
	switch req.Action {
	case "hold":
		if hold != nil {
			if hold.version != row.BodyVersion || !time.Now().Before(hold.until) {
				return empty, 409, fmt.Errorf("编辑已过期，请重新打开")
			}
			return view(), 200, nil
		}
		if !row.Editing {
			row.Editing = true
			if err := ch.saveQueueEdit(svc, info, s, q, false); err != nil {
				return empty, 500, err
			}
		}
		if l.editors == nil {
			l.editors = make(map[string]*queueEditHold)
		}
		hold = &queueEditHold{row: row.ID, version: row.BodyVersion, until: time.Now().Add(queueEditLifetime)}
		l.editors[req.EditID] = hold
		hold.timer = time.AfterFunc(queueEditLifetime, func() { ch.expireQueueEdit(svc, info, s.ID(), l, req.EditID, hold) })
		return view(), 200, nil
	case "renew", "edit":
		if hold == nil || !row.Editing || hold.version != row.BodyVersion || !time.Now().Before(hold.until) {
			return empty, 409, fmt.Errorf("编辑已过期或条目已被修改")
		}
		if req.Action == "renew" {
			hold.until = time.Now().Add(queueEditLifetime)
			hold.timer.Reset(queueEditLifetime)
			return view(), 200, nil
		}
		if row.Kind == "message" {
			hasAttachments := len(row.Attachments) > 0
			if req.KeepAttachments != nil {
				hasAttachments = len(*req.KeepAttachments)+len(req.Images)+len(req.Files) > 0
			}
			if !validQueuedText(row.Agent, req.Text, hasAttachments) {
				return empty, 400, fmt.Errorf("请输入发给原 Agent 的消息或添加附件")
			}
			if req.KeepAttachments != nil {
				var err error
				changedAttachments, err = svc.editQueueAttachments(ctx, s, q, row, req)
				if err != nil {
					return empty, queueAttachmentStatus(err), err
				}
			}
		} else {
			if req.KeepAttachments != nil {
				return empty, 400, fmt.Errorf("/compact 不接受附件")
			}
			binding, err := svc.externalBroker.QueuedCompactBinding(ctx, row.Agent, s.ID(), req.Text)
			if err != nil {
				return empty, 400, err
			}
			if saved := q.Bindings[row.ID]; saved != "" && binding != saved {
				return empty, 409, fmt.Errorf("原生会话已经更换")
			}
		}
	case "move":
		if err := q.move(req.ID, req.Anchor, req.Position); err != nil {
			return empty, 409, err
		}
	}

	changedBody := req.Action == "edit" && (req.Text != row.Text || changedAttachments)
	if req.Action == "edit" {
		if changedBody {
			if row.TextDigest == "" {
				row.TextDigest = queueTextDigest(row.Text)
			}
			row.Text = req.Text
			row.BodyVersion++
		}
		row.Editing = !changedBody && l.hasOtherEdit(row.ID, req.EditID)
	}
	if q.Mutations == nil {
		q.Mutations = make(map[string]string)
	}
	q.Mutations[req.Operation] = req.mutationDigest()
	if req.Action == "edit" {
		if q.EditedRows == nil {
			q.EditedRows = make(map[string]string)
		}
		q.EditedRows[req.Operation] = req.ID
	}
	if err := ch.saveQueueEdit(svc, info, s, q, true); err != nil {
		return empty, 500, err
	}
	if changedBody {
		l.retireEdits(req.ID)
	} else if req.Action == "edit" {
		l.removeEdit(req.EditID)
	}
	return view(), 200, nil
}

func (ch *WebUIChannel) releaseQueueEdit(svc *Service, info WorkspaceInfo, s *session.Session, q *commandQueue, l *queueLease, key string) error {
	hold := l.editors[key]
	if hold == nil {
		return nil
	}
	if row := q.find(hold.row); row != nil && row.State == "waiting" && row.Editing && !l.hasOtherEdit(hold.row, key) {
		row.Editing = false
		if err := ch.saveQueueEdit(svc, info, s, q, true); err != nil {
			return err
		}
	}
	l.removeEdit(key)
	return nil
}

func (ch *WebUIChannel) expireQueueEdit(svc *Service, info WorkspaceInfo, id string, l *queueLease, key string, hold *queueEditHold) {
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	if ch.lifetime.Err() != nil || reg.queueLeases[id] != l || l.editors[key] != hold {
		return
	}
	if remaining := time.Until(hold.until); remaining > 0 {
		hold.timer.Reset(remaining)
		return
	}
	s, err := svc.store.Open(id)
	if errors.Is(err, os.ErrNotExist) {
		reg.releaseQueue(id)
		return
	}
	if err == nil {
		var q *commandQueue
		q, err = reg.readQueue(s, ch.lifetime, false)
		if err == nil {
			err = ch.releaseQueueEdit(svc, info, s, q, l, key)
		}
		if err == nil {
			reg.releaseIdleQueue(s, q)
			return
		}
	}
	log.Printf("release expired queue editor: %v", err)
	hold.timer.Reset(5 * time.Second)
}
