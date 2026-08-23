// Local semantic search — embeddings over the vault.
//
// Substring search fails exactly when memory does: you remember the
// *concept* ("that clustering IP problem") but not the keyword
// (RegisterAllProvidersIP). With an embedding model loaded in LM Studio and
// "embed_model" set in config, vault_search gains a "related by meaning"
// section powered by a local index.
//
// Design: notes are chunked by paragraph (~1200 chars), embedded via the
// standard /v1/embeddings endpoint in batches, and cached in a gob file
// under ~/.agent/index/ keyed by vault path. The index is INCREMENTAL:
// each search rescans the vault by (path, mtime, size) and embeds only
// new or changed chunks — a stable vault costs one query embedding per
// search, nothing more. No vector database; at personal-vault scale a
// linear cosine scan over a few thousand chunks is microseconds.
//
// Everything degrades: no embed_model configured → keyword search only,
// silently. Embedding server errors → keyword results plus one dim note.
package toolsext

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
)

// embedFn is swappable for tests.
var embedFn = embedTexts

type vaultChunk struct {
	Path  string // relative to vault root
	MTime int64
	Size  int64
	Text  string
	Vec   []float32
}

type vaultIndex struct {
	Model  string // index is invalid if the embed model changed
	Chunks []vaultChunk
}

// embedTexts calls the OpenAI-compatible embeddings endpoint.
func embedTexts(texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": core.EmbedModel, "input": texts})
	if err != nil {
		return nil, err
	}
	resp, err := agent.HTTPClient().Post(agent.CurBaseURL()+"/v1/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("embeddings: %s", out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("embeddings: bad index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// chunkNote splits markdown into ~maxChunk-char pieces on paragraph
// boundaries — big enough to carry meaning, small enough to pinpoint.
func chunkNote(text string) []string {
	const maxChunk = 1200
	paras := strings.Split(text, "\n\n")
	var chunks []string
	var cur strings.Builder
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(p) > maxChunk {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		// A single huge paragraph still gets split hard.
		for len(p) > maxChunk {
			chunks = append(chunks, p[:maxChunk])
			p = p[maxChunk:]
		}
		cur.WriteString(p)
	}
	if strings.TrimSpace(cur.String()) != "" {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func indexPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte(core.VaultPath))
	dir := filepath.Join(home, ".agent", "index")
	if os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	return filepath.Join(dir, "vault-"+hex.EncodeToString(h[:8])+".gob")
}

func loadVaultIndex() *vaultIndex {
	idx := &vaultIndex{Model: core.EmbedModel}
	p := indexPath()
	if p == "" {
		return idx
	}
	f, err := os.Open(p)
	if err != nil {
		return idx
	}
	defer func() { _ = f.Close() }()
	var loaded vaultIndex
	if gob.NewDecoder(f).Decode(&loaded) == nil && loaded.Model == core.EmbedModel {
		return &loaded
	}
	return idx // model changed or corrupt: rebuild from scratch
}

func saveVaultIndex(idx *vaultIndex) {
	p := indexPath()
	if p == "" {
		return
	}
	var buf bytes.Buffer
	if gob.NewEncoder(&buf).Encode(idx) != nil {
		return
	}
	_ = os.WriteFile(p, buf.Bytes(), 0o644)
}

// ensureVaultIndex brings the index up to date with the vault: unchanged
// files keep their vectors, new/changed files are re-chunked and embedded
// (batched), deleted files drop out. Returns the fresh index.
func ensureVaultIndex() (*vaultIndex, error) {
	idx := loadVaultIndex()
	// Existing chunks grouped by file identity.
	byFile := map[string][]vaultChunk{}
	for _, c := range idx.Chunks {
		key := fmt.Sprintf("%s|%d|%d", c.Path, c.MTime, c.Size)
		byFile[key] = append(byFile[key], c)
	}
	var fresh []vaultChunk
	var pendingTexts []string
	var pendingMeta []vaultChunk
	for _, p := range vaultNotes() {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(core.VaultPath, p)
		key := fmt.Sprintf("%s|%d|%d", rel, fi.ModTime().Unix(), fi.Size())
		if existing, ok := byFile[key]; ok {
			fresh = append(fresh, existing...) // unchanged: keep vectors
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, text := range chunkNote(string(data)) {
			pendingTexts = append(pendingTexts, text)
			pendingMeta = append(pendingMeta, vaultChunk{
				Path: rel, MTime: fi.ModTime().Unix(), Size: fi.Size(), Text: text,
			})
		}
	}
	if len(pendingTexts) > 0 {
		fmt.Println(core.Tint(core.ColorDim, fmt.Sprintf("  (indexing %d new/changed vault chunk(s) for semantic search)", len(pendingTexts))))
		const batch = 32
		for i := 0; i < len(pendingTexts); i += batch {
			end := min(i+batch, len(pendingTexts))
			vecs, err := embedFn(pendingTexts[i:end])
			if err != nil {
				return idx, err // keep the old index usable
			}
			for j, v := range vecs {
				pendingMeta[i+j].Vec = v
			}
		}
		fresh = append(fresh, pendingMeta...)
	}
	idx.Chunks = fresh
	saveVaultIndex(idx)
	return idx, nil
}

type semanticHit struct {
	chunk vaultChunk
	score float64
}

// semanticVaultHits returns the top-k chunks by meaning.
func semanticVaultHits(query string, k int) ([]semanticHit, error) {
	idx, err := ensureVaultIndex()
	if err != nil {
		return nil, err
	}
	if len(idx.Chunks) == 0 {
		return nil, nil
	}
	qv, err := embedFn([]string{query})
	if err != nil {
		return nil, err
	}
	hits := make([]semanticHit, 0, len(idx.Chunks))
	for _, c := range idx.Chunks {
		if len(c.Vec) == 0 {
			continue
		}
		hits = append(hits, semanticHit{c, cosine(qv[0], c.Vec)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// ---------- Codebase semantic search ----------
//
// search_files finds code by PATTERN; code_search finds it by MEANING:
// "where do we retry failed connections" lands on the backoff loop even
// though no line contains the word "retry". Same machinery as the vault
// index — incremental by (path, mtime, size), gob-cached per workdir,
// batched embeddings — with code-aware chunking that remembers line
// numbers so hits are jump-to-able.

var codeExts = map[string]bool{
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".cs": true, ".ps1": true, ".psm1": true, ".psd1": true, ".sql": true,
	".sh": true, ".bash": true, ".yaml": true, ".yml": true, ".tf": true,
	".toml": true, ".rs": true, ".java": true, ".rb": true, ".php": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true,
}

var codeSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "bin": true, "obj": true,
	"dist": true, "build": true, "target": true, "__pycache__": true,
}

type codeChunk struct {
	Path      string // relative to workdir
	MTime     int64
	Size      int64
	StartLine int // 1-based first line of the chunk
	Text      string
	Vec       []float32
}

type codeIndex struct {
	Model  string
	Chunks []codeChunk
}

// chunkCode splits source into ~1000-char pieces on line boundaries,
// tracking the 1-based start line of each piece.
func chunkCode(text string) []codeChunk {
	const maxChunk = 1000
	lines := strings.Split(text, "\n")
	var chunks []codeChunk
	var cur strings.Builder
	start := 1
	for i, line := range lines {
		if cur.Len() > 0 && cur.Len()+len(line) > maxChunk {
			chunks = append(chunks, codeChunk{StartLine: start, Text: cur.String()})
			cur.Reset()
			start = i + 1
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if strings.TrimSpace(cur.String()) != "" {
		chunks = append(chunks, codeChunk{StartLine: start, Text: cur.String()})
	}
	return chunks
}

func codeFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || codeSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !codeExts[filepath.Ext(d.Name())] || strings.HasSuffix(d.Name(), ".bak") {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Size() <= 256*1024 {
			files = append(files, p)
		}
		return nil
	})
	if len(files) > 2000 {
		files = files[:2000] // sanity bound for giant monorepos
	}
	return files
}

func codeIndexPath(root string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte(root))
	dir := filepath.Join(home, ".agent", "index")
	if os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	return filepath.Join(dir, "code-"+hex.EncodeToString(h[:8])+".gob")
}

// ensureCodeIndex mirrors ensureVaultIndex for the workdir's source files.
func ensureCodeIndex(root string) (*codeIndex, error) {
	idx := &codeIndex{Model: core.EmbedModel}
	if p := codeIndexPath(root); p != "" {
		if f, err := os.Open(p); err == nil {
			var loaded codeIndex
			if gob.NewDecoder(f).Decode(&loaded) == nil && loaded.Model == core.EmbedModel {
				idx = &loaded
			}
			_ = f.Close()
		}
	}
	byFile := map[string][]codeChunk{}
	for _, c := range idx.Chunks {
		key := fmt.Sprintf("%s|%d|%d", c.Path, c.MTime, c.Size)
		byFile[key] = append(byFile[key], c)
	}
	var fresh []codeChunk
	var pending []codeChunk
	for _, p := range codeFiles(root) {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		key := fmt.Sprintf("%s|%d|%d", rel, fi.ModTime().Unix(), fi.Size())
		if existing, ok := byFile[key]; ok {
			fresh = append(fresh, existing...)
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, c := range chunkCode(string(data)) {
			c.Path, c.MTime, c.Size = rel, fi.ModTime().Unix(), fi.Size()
			pending = append(pending, c)
		}
	}
	if len(pending) > 0 {
		fmt.Println(core.Tint(core.ColorDim, fmt.Sprintf("  (indexing %d new/changed code chunk(s) for semantic search)", len(pending))))
		texts := make([]string, len(pending))
		for i, c := range pending {
			// Path context in the embedded text helps retrieval enormously.
			texts[i] = c.Path + ":\n" + c.Text
		}
		const batch = 32
		for i := 0; i < len(texts); i += batch {
			end := min(i+batch, len(texts))
			vecs, err := embedFn(texts[i:end])
			if err != nil {
				return idx, err
			}
			for j, v := range vecs {
				pending[i+j].Vec = v
			}
		}
		fresh = append(fresh, pending...)
	}
	idx.Chunks = fresh
	if p := codeIndexPath(root); p != "" {
		var buf bytes.Buffer
		if gob.NewEncoder(&buf).Encode(idx) == nil {
			_ = os.WriteFile(p, buf.Bytes(), 0o644)
		}
	}
	return idx, nil
}

func toolCodeSearch(s *agent.Sandbox, a agent.ToolArgs) string {
	query := strings.TrimSpace(a.Str("query"))
	if query == "" {
		return "ERROR: query must not be empty"
	}
	idx, err := ensureCodeIndex(s.Root)
	if err != nil {
		return "ERROR: semantic index unavailable: " + err.Error() + " — fall back to search_files."
	}
	if len(idx.Chunks) == 0 {
		return "(no indexable source files in this workdir)"
	}
	qv, err := embedFn([]string{query})
	if err != nil {
		return "ERROR: " + err.Error() + " — fall back to search_files."
	}
	type hit struct {
		c codeChunk
		s float64
	}
	hits := make([]hit, 0, len(idx.Chunks))
	for _, c := range idx.Chunks {
		if len(c.Vec) > 0 {
			hits = append(hits, hit{c, cosine(qv[0], c.Vec)})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	if len(hits) > 6 {
		hits = hits[:6]
	}
	var b strings.Builder
	for _, h := range hits {
		snippet := h.c.Text
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		fmt.Fprintf(&b, "%s:%d (%.2f)\n%s\n\n", h.c.Path, h.c.StartLine, h.s, snippet)
	}
	return b.String()
}

// RegisterEmbedTools adds code_search when an embedding model is configured.
func RegisterEmbedTools() {
	if core.EmbedModel == "" {
		return
	}
	agent.RegisterTools(agent.Tool{
		Name: "code_search",
		Desc: "Semantic search over this workdir's source code — finds code by MEANING, not pattern: 'where do we retry failed connections' works without the word retry appearing. Complements search_files (exact patterns/regex). Results are path:line with a snippet; read_file the hits for full context.",
		Props: map[string]any{
			"query": map[string]any{"type": "string", "description": "What the code does, in plain words"},
		},
		Required: []string{"query"},
		Handler:  toolCodeSearch,
	})
	agent.MarkReadOnly("code_search")
	agent.RebuildToolSchemas()
}
