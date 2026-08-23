package toolsext

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
)

// stubEmbedder produces deterministic vectors: notes about "clustering"
// land near a "cluster ip problem" query, others don't.
func stubEmbedder(calls *int) func([]string) ([][]float32, error) {
	return func(texts []string) ([][]float32, error) {
		*calls++
		vecs := make([][]float32, len(texts))
		for i, t := range texts {
			t = strings.ToLower(t)
			var v [3]float32
			if strings.Contains(t, "cluster") || strings.Contains(t, "registerallprovidersip") || strings.Contains(t, "subnet") {
				v = [3]float32{1, 0.1, 0}
			} else if strings.Contains(t, "recipe") {
				v = [3]float32{0, 1, 0}
			} else {
				v = [3]float32{0.2, 0.2, 1}
			}
			vecs[i] = v[:]
		}
		return vecs, nil
	}
}

func TestChunkNote(t *testing.T) {
	chunks := chunkNote("para one\n\npara two\n\n" + strings.Repeat("x", 3000))
	if len(chunks) < 3 {
		t.Fatalf("oversized paragraph must split: %d chunks", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 1300 {
			t.Fatalf("chunk exceeds bound: %d", len(c))
		}
	}
	if len(chunkNote("")) != 0 {
		t.Fatal("empty note must produce no chunks")
	}
}

func TestCosine(t *testing.T) {
	if c := cosine([]float32{1, 0}, []float32{1, 0}); c < 0.999 {
		t.Fatalf("identical vectors: %f", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 1}); c > 0.001 {
		t.Fatalf("orthogonal vectors: %f", c)
	}
	if cosine([]float32{1}, []float32{1, 2}) != 0 {
		t.Fatal("length mismatch must be 0")
	}
}

func TestVaultIndexIncremental(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	v := setupVault(t) // from vault_test.go: two real notes
	_ = v
	oldEmbed, oldModel := embedFn, core.EmbedModel
	calls := 0
	embedFn = stubEmbedder(&calls)
	core.EmbedModel = "stub-model"
	defer func() { embedFn, core.EmbedModel = oldEmbed, oldModel }()

	idx, err := ensureVaultIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Chunks) == 0 {
		t.Fatal("index must contain chunks")
	}
	firstCalls := calls

	// Second run, nothing changed: zero embedding calls.
	if _, err := ensureVaultIndex(); err != nil {
		t.Fatal(err)
	}
	if calls != firstCalls {
		t.Fatalf("unchanged vault must not re-embed (calls %d → %d)", firstCalls, calls)
	}

	// Touch one note: only it re-embeds.
	p := filepath.Join(core.VaultPath, "GovCloud Networking.md")
	os.WriteFile(p, []byte("# GovCloud Networking\n\nENI subnet assignment, updated.\n"), 0o644)
	now := time.Now().Add(2 * time.Second)
	os.Chtimes(p, now, now)
	idx2, err := ensureVaultIndex()
	if err != nil {
		t.Fatal(err)
	}
	if calls != firstCalls+1 {
		t.Fatalf("exactly one batch for one changed file, got %d extra", calls-firstCalls)
	}
	for _, c := range idx2.Chunks {
		if c.Path == "GovCloud Networking.md" && !strings.Contains(c.Text, "updated") {
			t.Fatal("changed file's chunks must be fresh")
		}
	}
}

func TestSemanticHitsAndHybridSearch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setupVault(t)
	oldEmbed, oldModel := embedFn, core.EmbedModel
	calls := 0
	embedFn = stubEmbedder(&calls)
	core.EmbedModel = "stub-model"
	defer func() { embedFn, core.EmbedModel = oldEmbed, oldModel }()

	hits, err := semanticVaultHits("that cluster ip problem", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].chunk.Path, "SQL AG Setup") {
		t.Fatalf("semantic top hit must be the AG note: %+v", hits)
	}

	// Hybrid: a query with no keyword match still returns the semantic section.
	sb := &agent.Sandbox{Root: t.TempDir()}
	out := toolVaultSearch(sb, agent.ToolArgs{"query": "cluster ip trouble"})
	if !strings.Contains(out, "Related by meaning") || !strings.Contains(out, "SQL AG Setup") {
		t.Fatalf("hybrid search missing semantic section:\n%s", out)
	}
	if !strings.Contains(out, "no keyword matches") {
		t.Fatalf("keyword miss must be labeled:\n%s", out)
	}
}

var _ = fmt.Sprintf // keep fmt for future assertions

func TestChunkCodeLineNumbers(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString(fmt.Sprintf("line %d padding padding padding\n", i))
	}
	chunks := chunkCode(b.String())
	if len(chunks) < 2 {
		t.Fatalf("100 lines must span multiple chunks: %d", len(chunks))
	}
	if chunks[0].StartLine != 1 {
		t.Fatalf("first chunk starts at 1, got %d", chunks[0].StartLine)
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartLine <= chunks[i-1].StartLine {
			t.Fatal("start lines must increase")
		}
		if !strings.Contains(chunks[i].Text, fmt.Sprintf("line %d ", chunks[i].StartLine)) {
			t.Fatalf("chunk %d claims start %d but text disagrees", i, chunks[i].StartLine)
		}
	}
}

func TestCodeSearchEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(root, "db.go"), []byte("package db\n\n// connect retries with cluster failover and subnet checks\nfunc connect() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "bake.go"), []byte("package bake\n\n// recipe for bread\nfunc bake() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "node_modules", "junk.js"), []byte("cluster cluster cluster"), 0o644)
	oldEmbed, oldModel := embedFn, core.EmbedModel
	calls := 0
	embedFn = stubEmbedder(&calls)
	core.EmbedModel = "stub-model"
	defer func() { embedFn, core.EmbedModel = oldEmbed, oldModel }()

	sb := &agent.Sandbox{Root: root}
	out := toolCodeSearch(sb, agent.ToolArgs{"query": "cluster ip problem handling"})
	if !strings.Contains(out, "db.go:") {
		t.Fatalf("semantic hit must name db.go with a line: %s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("skip dirs must be excluded: %s", out)
	}
	firstCalls := calls
	_ = toolCodeSearch(sb, agent.ToolArgs{"query": "cluster again"})
	if calls != firstCalls+1 { // only the query embedding, no re-index
		t.Fatalf("unchanged workdir must not re-embed: %d extra calls", calls-firstCalls)
	}
}
