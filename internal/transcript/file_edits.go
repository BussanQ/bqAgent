package transcript

// FileEdits is display evidence from one successful native tool call. Line
// numbers are one-based; zero means that side has no known file position.
type FileEdits struct {
	Files   []EditFile `json:"files"`
	Partial bool       `json:"partial,omitempty"`
}

type EditFile struct {
	Path    string     `json:"path"`
	OldPath string     `json:"old_path,omitempty"`
	Kind    string     `json:"kind"`
	Lines   []EditLine `json:"lines,omitempty"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
	Partial bool       `json:"partial,omitempty"`
	Snippet bool       `json:"snippet,omitempty"`
}

type EditLine struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}
