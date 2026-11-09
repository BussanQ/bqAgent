package session

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"bqagent/internal/atomicfile"
	"bqagent/internal/privatefile"
	"bqagent/internal/safepath"
)

// A clear adds a divider. A rewind replaces its preceding context with the
// retained prefix. Both keep the old directory available to pinned readers.
type ContextBoundary struct {
	Replaced  string `json:"replaced_thread,omitempty"`
	Forked    string `json:"forked_thread,omitempty"`
	RewindTo  string `json:"rewind_to,omitempty"`
	Directory string `json:"directory"`
	Operation string `json:"operation"`
	Agent     string `json:"agent,omitempty"`
}

func (s *Session) RefreshContexts() error {
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := root.ReadFile("contexts.json")
	if errors.Is(err, os.ErrNotExist) {
		s.contexts = nil
		return nil
	}
	if err != nil {
		return err
	}
	var list []ContextBoundary
	if err = json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, b := range list {
		if b.Operation == "" || safepath.ValidateComponent(b.Directory) != nil || !b.validFork() {
			return fmt.Errorf("invalid context boundary")
		}
		resolved, err := safepath.Resolve(s.Dir(), filepath.Join(".contexts", b.Directory))
		if err != nil {
			return err
		}
		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			return fmt.Errorf("missing context directory %q", b.Directory)
		}
	}
	s.contexts = list
	return nil
}

func (s *Session) ContextNumber() int { return len(s.contexts) }
func (s *Session) ContextDir() string {
	if len(s.contexts) == 0 {
		return s.Dir()
	}
	return filepath.Join(s.Dir(), ".contexts", s.contexts[len(s.contexts)-1].Directory)
}

// ContextAt pins the historical reader to the prefix ending at that boundary.
func (s *Session) ContextAt(number int) (*Session, bool) {
	if number < 0 || number > len(s.contexts) {
		return nil, false
	}
	pinned := *s
	pinned.contexts = s.contexts[:number:number]
	return &pinned, true
}

func (s *Session) FindClear(operation string) (ContextBoundary, bool) {
	b, ok := s.FindBoundary(operation)
	return b, ok && b.RewindTo == ""
}

func (s *Session) FindBoundary(operation string) (ContextBoundary, bool) {
	for _, b := range s.contexts {
		if b.Operation == operation {
			return b, true
		}
	}
	return ContextBoundary{}, false
}

func (s *Session) BoundaryAfter(number int) ContextBoundary { return s.contexts[number] }

// The caller holds the conversation lock. Prepare all provider and queue state
// in a private directory before publishing one atomic manifest replacement.
func (s *Session) StartContext(operation, agent string, prepare func(*Session) error) error {
	return s.startContext(ContextBoundary{Operation: operation, Agent: agent}, prepare)
}

func (s *Session) RewindContext(operation, agent, target string, prepare func(*Session) error) error {
	return s.RewindFork(operation, agent, target, "", "", prepare)
}

func (s *Session) RewindFork(operation, agent, target, replaced, forked string, prepare func(*Session) error) error {
	if target == "" {
		return errors.New("missing rewind target")
	}
	b := ContextBoundary{Operation: operation, Agent: agent, RewindTo: target, Replaced: replaced, Forked: forked}
	if !b.validFork() {
		return errors.New("invalid rewind fork")
	}
	return s.startContext(b, prepare)
}

func (b ContextBoundary) validFork() bool {
	return b.Replaced == "" && b.Forked == "" || b.RewindTo != "" && b.Replaced != "" && b.Forked != "" && b.Replaced != b.Forked
}

// Retained prompts may still identify the ancestor in which they were sent.
// A clear separates unrelated native sessions even in the same BQ chat.
func (s *Session) HasRewindAncestor(agent, head, source string) bool {
	if source == "" || head == "" {
		return false
	}
	if head == source {
		return true
	}
	for i := len(s.contexts) - 1; i >= 0; i-- {
		b := s.contexts[i]
		if b.RewindTo == "" {
			break
		}
		if b.Agent == agent && b.Forked == head {
			head = b.Replaced
			if head == source {
				return true
			}
		}
	}
	return false
}

func (s *Session) startContext(b ContextBoundary, prepare func(*Session) error) error {
	operation := b.Operation
	if operation == "" {
		return errors.New("missing context operation ID")
	}
	if err := s.RefreshContexts(); err != nil {
		return err
	}
	if existing, ok := s.FindBoundary(operation); ok {
		if existing.Agent != b.Agent || existing.RewindTo != b.RewindTo || existing.Replaced != b.Replaced || existing.Forked != b.Forked {
			return errors.New("context operation ID already used with another target")
		}
		return nil
	}
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll(".contexts", privatefile.DirMode); err != nil {
		return err
	}
	next := *s
	b.Directory = rand.Text()
	next.contexts = append(slices.Clone(s.contexts), b)
	dir := filepath.Join(".contexts", b.Directory)
	if _, err = safepath.Resolve(s.Dir(), dir); err != nil {
		return err
	}
	if err = root.Mkdir(dir, privatefile.DirMode); err != nil {
		return err
	}
	if err = prepare(&next); err != nil {
		_ = root.RemoveAll(dir)
		return err
	}
	// The manifest is in the session root, one directory above the staged
	// entry. Sync that entry's parent before making the manifest durable.
	if runtime.GOOS != "windows" {
		parent, err := root.Open(".contexts")
		if err == nil {
			err = parent.Sync()
			_ = parent.Close()
		}
		if err != nil {
			_ = root.RemoveAll(dir)
			return err
		}
	}
	data, err := json.Marshal(next.contexts)
	if err != nil {
		_ = root.RemoveAll(dir)
		return err
	}
	if err = atomicfile.WriteRoot(root, "contexts.json", data, privatefile.FileMode); err != nil {
		// A directory fsync can report an error after rename. Never remove a
		// published directory, including when a read-back itself fails.
		if readErr := s.RefreshContexts(); readErr == nil {
			if found, ok := s.FindBoundary(operation); ok && found == b {
				return nil
			}
			_ = root.RemoveAll(dir)
		}
		return err
	}
	s.contexts = next.contexts
	return nil
}
