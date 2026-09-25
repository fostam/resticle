package restic

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fostam/resticle/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestArgvSnapshot renders every job of testdata/config.yaml into the restic
// command lines it produces, and compares them against a golden file.
// Regenerate with:
//
//	go test ./internal/restic/ -run TestArgvSnapshot -update
//
// Any diff here is a change in what restic is actually asked to do — the
// retention that expires snapshots, the paths that get backed up, the subset
// that gets verified. Read the diff before accepting it.
func TestArgvSnapshot(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config.yaml"))
	if err != nil {
		t.Fatalf("load testdata/config.yaml: %v", err)
	}
	cfg.Resolve()
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("testdata config is invalid: %v", errs)
	}

	names := make([]string, 0, len(cfg.Jobs))
	for n := range cfg.Jobs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			j := cfg.Jobs[name]
			var b strings.Builder
			if j.Backup != nil {
				b.WriteString("backup: " + strings.Join(BackupArgs(j, false), " ") + "\n")
			}
			if j.Forget != nil {
				b.WriteString("forget: " + strings.Join(ForgetArgs(j, false), " ") + "\n")
			}
			{
				// Index 0 is deterministic; rotation is covered in state tests.
				if args, ok := CheckArgs(j, 0); ok {
					b.WriteString("check:  " + strings.Join(args, " ") + "\n")
				} else {
					b.WriteString("check:  (none)\n")
				}
			}
			got := b.String()

			golden := filepath.Join("..", "..", "testdata", "argv", name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("command lines changed\n got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
