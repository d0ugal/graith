package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkFindCodexRolloutByID(b *testing.B) {
	root := b.TempDir()

	const target = "canny"

	for i := 0; i < 2500; i++ {
		id := fmt.Sprintf("braw-%04d", i)
		if i == 2499 {
			id = target
		}

		path := filepath.Join(root, fmt.Sprintf("rollout-%04d-%s.jsonl", i, id))

		data := []byte(fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", id))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			b.Fatal(err)
		}
	}

	for name, id := range map[string]string{
		"first":  "braw-0000",
		"middle": "braw-1250",
		"last":   target,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				if _, ok := findCodexRolloutByID(root, id); !ok {
					b.Fatal("missing rollout")
				}
			}
		})
	}
}
