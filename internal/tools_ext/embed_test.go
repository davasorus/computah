package toolsext

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
