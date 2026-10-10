// Package taskstate defines session-scoped coding task progress, independently
// of transport turns and conversation compaction.
package taskstate

import (
	"fmt"
	"strings"
	"time"
)

type Criterion struct {
	Description string `json:"description"`
	Done        bool   `json:"done"`
	Evidence    string `json:"evidence,omitempty"`
}

type Verification struct {
	Command string `json:"command"`
	Status  string `json:"status"` // passed, failed, unavailable
	Details string `json:"details"`
}

type State struct {
	Goal         string         `json:"goal"`
	Status       string         `json:"status"` // active, completed, blocked, awaiting_input, cancelled
	Criteria     []Criterion    `json:"criteria"`
	Remaining    []string       `json:"remaining"`
	Verification []Verification `json:"verification"`
	Reason       string         `json:"reason,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type Store interface {
	LoadTaskState() (*State, error)
	SaveTaskState(State) error
}

func (state State) Validate() error {
	if strings.TrimSpace(state.Goal) == "" || len(state.Criteria) == 0 {
		return fmt.Errorf("goal and acceptance criteria are required")
	}
	switch state.Status {
	case "active", "completed":
	case "blocked", "awaiting_input", "cancelled":
		if strings.TrimSpace(state.Reason) == "" {
			return fmt.Errorf("%s requires a reason", state.Status)
		}
	default:
		return fmt.Errorf("invalid task status %q", state.Status)
	}
	for _, criterion := range state.Criteria {
		if strings.TrimSpace(criterion.Description) == "" {
			return fmt.Errorf("empty acceptance criterion")
		}
		if criterion.Done && strings.TrimSpace(criterion.Evidence) == "" {
			return fmt.Errorf("completed criteria require evidence")
		}
		if state.Status == "completed" && !criterion.Done {
			return fmt.Errorf("acceptance criteria remain unfinished")
		}
	}
	for _, check := range state.Verification {
		if strings.TrimSpace(check.Command) == "" || strings.TrimSpace(check.Details) == "" {
			return fmt.Errorf("verification requires command and result details")
		}
		switch check.Status {
		case "passed", "failed", "unavailable":
		default:
			return fmt.Errorf("invalid verification status %q", check.Status)
		}
		if state.Status == "completed" && check.Status != "passed" {
			return fmt.Errorf("verification has not passed")
		}
	}
	if state.Status == "completed" && (len(state.Remaining) > 0 || len(state.Verification) == 0) {
		return fmt.Errorf("completion requires no remaining work and recorded verification")
	}
	return nil
}
