package memory

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	MaxEntries     = 500
	MaxContentSize = 2000
	MaxTotalSize   = 256 << 10
	DefaultLimit   = 12
)

type Kind string

const (
	KindUserPreference Kind = "user_preference"
	KindProjectFact    Kind = "project_fact"
	KindLesson         Kind = "lesson"
	KindDecision       Kind = "decision"
)

type Entry struct {
	ID          string     `json:"id"`
	Revision    int        `json:"revision"`
	Kind        Kind       `json:"kind"`
	Content     string     `json:"content"`
	SourceRunID string     `json:"source_run_id,omitempty"`
	Confidence  float64    `json:"confidence"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	Supersedes  []string   `json:"supersedes,omitempty"`
	State       string     `json:"state"`
	Sensitivity string     `json:"sensitivity"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
}

type Result struct {
	Entry Entry   `json:"entry"`
	Score float64 `json:"score"`
}

type Index struct {
	Version   int                 `json:"version"`
	UpdatedAt time.Time           `json:"updated_at"`
	Terms     map[string][]string `json:"terms"`
}

type Store struct {
	dir    string
	legacy []string
}

func NewStore(dir string, legacyPaths ...string) *Store { return &Store{dir: dir, legacy: legacyPaths} }
func (s *Store) EntriesPath() string                    { return filepath.Join(s.dir, "entries.jsonl") }

func (s *Store) Add(kind Kind, content, source string, confidence float64, sensitivity string, supersedes []string) (Entry, error) {
	content = strings.TrimSpace(content)
	if !validKind(kind) {
		return Entry{}, fmt.Errorf("invalid memory kind %q", kind)
	}
	if content == "" || len(content) > MaxContentSize {
		return Entry{}, fmt.Errorf("memory content must contain 1-%d characters", MaxContentSize)
	}
	if containsSecret(content) {
		return Entry{}, fmt.Errorf("memory content appears to contain a secret and was rejected")
	}
	entries, err := s.Active()
	if err != nil {
		return Entry{}, err
	}
	if len(entries) >= MaxEntries {
		return Entry{}, fmt.Errorf("memory capacity reached (%d active entries)", MaxEntries)
	}
	total := len(content)
	for _, entry := range entries {
		total += len(entry.Content)
	}
	if total > MaxTotalSize {
		return Entry{}, fmt.Errorf("memory capacity reached (%d bytes)", MaxTotalSize)
	}
	for _, entry := range entries {
		sim := similarity(entry.Content, content)
		if sim >= .92 {
			return Entry{}, fmt.Errorf("duplicate memory conflicts with %s", entry.ID)
		}
		if sim >= .70 && !contains(supersedes, entry.ID) {
			return Entry{}, fmt.Errorf("potential memory conflict with %s; replace it or include it in supersedes", entry.ID)
		}
	}
	if confidence <= 0 {
		confidence = .8
	}
	if confidence > 1 {
		confidence = 1
	}
	now := time.Now().UTC()
	state := "active"
	sensitivity = strings.ToLower(strings.TrimSpace(sensitivity))
	if sensitivity == "" {
		sensitivity = "normal"
	}
	if sensitivity == "sensitive" {
		state = "pending"
	}
	entry := Entry{ID: newID(), Revision: 1, Kind: kind, Content: content, SourceRunID: source, Confidence: confidence, CreatedAt: now, UpdatedAt: now, Supersedes: supersedes, State: state, Sensitivity: sensitivity}
	if err := s.append(entry); err != nil {
		return Entry{}, err
	}
	for _, id := range supersedes {
		_, _ = s.Remove(id, source, "superseded")
	}
	_ = s.RebuildIndex()
	return entry, nil
}

func (s *Store) Replace(id string, kind Kind, content, source string, confidence float64, supersedes []string) (Entry, error) {
	current, err := s.Get(id)
	if err != nil {
		return Entry{}, err
	}
	if kind == "" {
		kind = current.Kind
	}
	if !validKind(kind) {
		return Entry{}, fmt.Errorf("invalid memory kind %q", kind)
	}
	content = strings.TrimSpace(content)
	if content == "" || len(content) > MaxContentSize {
		return Entry{}, fmt.Errorf("invalid memory content")
	}
	if containsSecret(content) {
		return Entry{}, fmt.Errorf("memory content appears to contain a secret and was rejected")
	}
	now := time.Now().UTC()
	current.Revision++
	current.Kind = kind
	current.Content = content
	current.SourceRunID = source
	current.UpdatedAt = now
	current.Supersedes = supersedes
	if confidence > 0 {
		current.Confidence = math.Min(confidence, 1)
	}
	if err := s.append(current); err != nil {
		return Entry{}, err
	}
	_ = s.RebuildIndex()
	return current, nil
}

func (s *Store) Remove(id, source, state string) (Entry, error) {
	entry, err := s.Get(id)
	if err != nil {
		return Entry{}, err
	}
	entry.Revision++
	entry.State = state
	if entry.State == "" {
		entry.State = "removed"
	}
	entry.SourceRunID = source
	entry.UpdatedAt = time.Now().UTC()
	if err := s.append(entry); err != nil {
		return Entry{}, err
	}
	_ = s.RebuildIndex()
	return entry, nil
}

func (s *Store) Confirm(id, source string) (Entry, error) {
	entry, err := s.Get(id)
	if err != nil {
		return Entry{}, err
	}
	if entry.State != "pending" {
		return Entry{}, fmt.Errorf("memory %s is not pending", id)
	}
	now := time.Now().UTC()
	entry.Revision++
	entry.State = "active"
	entry.ConfirmedAt = &now
	entry.UpdatedAt = now
	entry.SourceRunID = source
	if err := s.append(entry); err != nil {
		return Entry{}, err
	}
	_ = s.RebuildIndex()
	return entry, nil
}

func (s *Store) Search(query string, kinds []Kind, limit int) ([]Result, error) {
	_ = s.maybeMaintain()
	if limit <= 0 || limit > 100 {
		limit = DefaultLimit
	}
	entries, err := s.Active()
	if err != nil {
		return nil, err
	}
	q := tokenize(query)
	allowed := map[Kind]bool{}
	for _, kind := range kinds {
		allowed[kind] = true
	}
	results := make([]Result, 0)
	for _, entry := range entries {
		if len(allowed) > 0 && !allowed[entry.Kind] {
			continue
		}
		terms := tokenize(entry.Content)
		common := 0
		for term := range q {
			if terms[term] {
				common++
			}
		}
		if common == 0 && strings.TrimSpace(query) != "" {
			continue
		}
		score := float64(common)/math.Sqrt(float64(max(1, len(terms)))) + entry.Confidence
		if strings.Contains(strings.ToLower(entry.Content), strings.ToLower(strings.TrimSpace(query))) {
			score += 2
		}
		if entry.Kind == KindDecision || entry.Kind == KindUserPreference {
			score += .25
		}
		results = append(results, Result{Entry: entry, Score: score})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	now := time.Now().UTC()
	for i := range results {
		touched := results[i].Entry
		touched.Revision++
		touched.LastUsedAt = &now
		touched.UpdatedAt = now
		_ = s.append(touched)
		results[i].Entry = touched
	}
	return results, nil
}

func (s *Store) Get(id string) (Entry, error) {
	entries, err := s.latest()
	if err != nil {
		return Entry{}, err
	}
	entry, ok := entries[id]
	if !ok {
		return Entry{}, os.ErrNotExist
	}
	return entry, nil
}
func (s *Store) Active() ([]Entry, error) {
	entries, err := s.latest()
	if err != nil {
		return nil, err
	}
	result := []Entry{}
	for _, entry := range entries {
		if entry.State == "active" {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}
func (s *Store) ListAll() ([]Entry, error) {
	entries, err := s.latest()
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func (s *Store) Compact() (map[string]any, error) {
	_ = s.maybeMaintain()
	entries, err := s.Active()
	if err != nil {
		return nil, err
	}
	duplicates := []map[string]any{}
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			if sim := similarity(entries[i].Content, entries[j].Content); sim >= .60 {
				duplicates = append(duplicates, map[string]any{"left": entries[i].ID, "right": entries[j].ID, "similarity": sim})
			}
		}
	}
	_ = s.RebuildIndex()
	return map[string]any{"active": len(entries), "duplicate_candidates": duplicates, "mutated": false}, nil
}

func (s *Store) maybeMaintain() error {
	indexPath := filepath.Join(s.dir, "index.json")
	info, err := os.Stat(indexPath)
	if err == nil && time.Since(info.ModTime()) < 7*24*time.Hour {
		return nil
	}
	entries, err := s.Active()
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -180)
	for _, entry := range entries {
		last := entry.UpdatedAt
		if entry.LastUsedAt != nil {
			last = *entry.LastUsedAt
		}
		if entry.Kind == KindLesson && last.Before(cutoff) {
			_, _ = s.Remove(entry.ID, "maintenance", "stale")
		}
	}
	return s.RebuildIndex()
}

func (s *Store) RebuildIndex() error {
	entries, err := s.Active()
	if err != nil {
		return err
	}
	index := Index{Version: 1, UpdatedAt: time.Now().UTC(), Terms: map[string][]string{}}
	for _, entry := range entries {
		for term := range tokenize(entry.Content) {
			index.Terms[term] = append(index.Terms[term], entry.ID)
		}
	}
	return writeJSONAtomic(filepath.Join(s.dir, "index.json"), index)
}

func (s *Store) Migrate() error {
	marker := filepath.Join(s.dir, "migration.json")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	for _, path := range s.legacy {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, block := range splitLegacy(string(content)) {
			if len(block) > MaxContentSize {
				block = block[:MaxContentSize]
			}
			_, _ = s.Add(KindLesson, block, "legacy:"+path, .5, "normal", nil)
		}
	}
	return writeJSONAtomic(marker, map[string]any{"version": 1, "completed_at": time.Now().UTC()})
}

func (s *Store) latest() (map[string]Entry, error) {
	result := map[string]Entry{}
	file, err := os.Open(s.EntriesPath())
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			if old, ok := result[entry.ID]; !ok || entry.Revision >= old.Revision {
				result[entry.ID] = entry
			}
		}
	}
	return result, scanner.Err()
}
func (s *Store) append(entry Entry) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	content, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.EntriesPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(content, '\n'))
	return err
}

var secretPattern = regexp.MustCompile(`(?i)(api[_ -]?key|password|passwd|secret|bearer|authorization|cookie|token)\s*[:=]\s*\S+`)

func containsSecret(value string) bool { return secretPattern.MatchString(value) }
func validKind(kind Kind) bool {
	return kind == KindUserPreference || kind == KindProjectFact || kind == KindLesson || kind == KindDecision
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func newID() string { b := make([]byte, 5); _, _ = rand.Read(b); return "mem_" + hex.EncodeToString(b) }
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func tokenize(value string) map[string]bool {
	value = strings.ToLower(strings.TrimSpace(value))
	out := map[string]bool{}
	var word []rune
	flush := func() {
		if len(word) > 0 {
			out[string(word)] = true
			word = nil
		}
	}
	var han []rune
	flushHan := func() {
		for n := 2; n <= 3; n++ {
			for i := 0; i+n <= len(han); i++ {
				out[string(han[i:i+n])] = true
			}
		}
		if len(han) == 1 {
			out[string(han)] = true
		}
		han = nil
	}
	for _, r := range []rune(value) {
		if unicode.Is(unicode.Han, r) {
			flush()
			han = append(han, r)
			continue
		}
		flushHan()
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	flushHan()
	return out
}
func similarity(a, b string) float64 {
	aa, bb := tokenize(a), tokenize(b)
	if len(aa) == 0 || len(bb) == 0 {
		return 0
	}
	common := 0
	for k := range aa {
		if bb[k] {
			common++
		}
	}
	return float64(common) / float64(len(aa)+len(bb)-common)
}
func splitLegacy(value string) []string {
	parts := regexp.MustCompile(`(?m)^##+\s+.*$`).Split(value, -1)
	result := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(content, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
