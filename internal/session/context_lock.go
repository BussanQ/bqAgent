package session

import (
	"errors"
	"os"
	"path/filepath"

	"bqagent/internal/privatefile"
)

var ErrContextBusy = errors.New("会话正在使用中，请等待当前操作结束")

func (s *Session) OpenContextLock(exclusive bool) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(s.Dir()))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile("."+s.ID()+".context.lock", os.O_CREATE|os.O_RDWR, privatefile.FileMode)
	if err != nil {
		return nil, err
	}
	if err = TryLockFile(f, exclusive); err != nil {
		_ = f.Close()
		return nil, err
	}
	// A provider may already have accepted a revert whose reply was lost.
	// Admit no new turns on that epoch until recovery settles the operation.
	if !exclusive {
		if err = s.RefreshContexts(); err == nil {
			var intent *PendingRewind
			intent, err = s.PendingRewind()
			if intent != nil {
				err = ErrRewindPending
			}
		}
		if err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}
