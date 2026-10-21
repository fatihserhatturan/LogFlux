package detect

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

var t0 = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

func msg(level models.LogLevel, text string) *models.LogEntry {
	e := models.NewLogEntry()
	e.Level = level
	e.Message = text
	e.Source = "app"
	return e
}

func TestTokenize_Masks(t *testing.T) {
	tests := map[string]string{
		"user=42 logged in from 10.0.0.7:8080":              "user=<NUM> logged in from <IP>",
		"request 550e8400-e29b-41d4-a716-446655440000 done": "request <UUID> done",
		"mail to bob@example.com failed":                    "mail to <EMAIL> failed",
		"took 125ms at 10:15:30":                            "took <NUM> at <TIME>",
		"ptr 0xdeadbeef":                                    "ptr <HEX>",
		"retry #3 (of 5)":                                   "retry #<NUM> (of <NUM>)",
		"first line\n\tat stack":                            "first line",
	}
	for in, want := range tests {
		if got := strings.Join(Tokenize(in), " "); got != want {
			t.Errorf("Tokenize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDrain_ClustersSimilarLines(t *testing.T) {
	d := NewDrain(DrainOptions{})
	lines := []string{
		"Connection to db-1 timed out after 30s",
		"Connection to db-2 timed out after 31s",
		"Connection to cache timed out after 5s",
		"User alice logged in",
		"User bob logged in",
	}
	ids := make([]int, len(lines))
	for i, l := range lines {
		c, _ := d.Add(msg(models.LevelError, l), t0)
		ids[i] = c.ID
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Errorf("timeout lines should share a cluster: %v", ids)
	}
	if ids[3] != ids[4] || ids[3] == ids[0] {
		t.Errorf("login lines should share a separate cluster: %v", ids)
	}
	c, _ := d.Get(ids[0])
	if got := c.Template(); got != "Connection to <*> timed out after <NUM>" {
		t.Errorf("template = %q", got)
	}
	if c.Size != 3 {
		t.Errorf("size = %d", c.Size)
	}
	if d.Len() != 2 {
		t.Errorf("clusters = %d, want 2", d.Len())
	}
}

func TestDrain_CreatedFlagAndMaxLevel(t *testing.T) {
	d := NewDrain(DrainOptions{})
	_, created := d.Add(msg(models.LevelInfo, "disk usage 50%"), t0)
	if !created {
		t.Error("first line should create a cluster")
	}
	c, created := d.Add(msg(models.LevelError, "disk usage 99%"), t0)
	if created {
		t.Error("similar line should not create a cluster")
	}
	if c.MaxLevel != models.LevelError || c.Sample != "disk usage 99%" {
		t.Errorf("max level/sample not updated: %+v", c)
	}
}

func TestDrain_MaxClusters(t *testing.T) {
	d := NewDrain(DrainOptions{MaxClusters: 2})
	d.Add(msg(models.LevelInfo, "alpha"), t0)
	d.Add(msg(models.LevelInfo, "beta gamma"), t0)
	if c, _ := d.Add(msg(models.LevelInfo, "delta epsilon zeta"), t0); c != nil {
		t.Error("expected nil cluster when limit reached")
	}
}

func TestDrain_SaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain.json")
	d := NewDrain(DrainOptions{})
	c1, _ := d.Add(msg(models.LevelError, "Connection to db-1 timed out"), t0)
	d.Add(msg(models.LevelError, "Connection to db-2 timed out"), t0)
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}

	d2 := NewDrain(DrainOptions{})
	if err := d2.Load(path); err != nil {
		t.Fatal(err)
	}
	c, created := d2.Add(msg(models.LevelError, "Connection to db-3 timed out"), t0)
	if created || c.ID != c1.ID || c.Size != 3 {
		t.Errorf("loaded tree should match existing cluster: created=%v %+v", created, c)
	}
	c, _ = d2.Add(msg(models.LevelInfo, "brand new line"), t0)
	if c.ID <= c1.ID {
		t.Errorf("new IDs must continue after loaded ones, got %d", c.ID)
	}
	if err := NewDrain(DrainOptions{}).Load(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Errorf("missing state file should not error: %v", err)
	}
}

// TestDrain_HDFSLike checks template quality on synthetic lines in the format
// of the Loghub HDFS dataset: 2000 lines from 6 event types should give
// roughly 6 templates, not hundreds.
func TestDrain_HDFSLike(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	blk := func() string { return fmt.Sprintf("blk_%d", r.Int63()) }
	ip := func() string { return fmt.Sprintf("10.250.%d.%d:%d", r.Intn(255), r.Intn(255), 50010+r.Intn(10)) }
	events := []func() string{
		func() string { return fmt.Sprintf("Receiving block %s src: /%s dest: /%s", blk(), ip(), ip()) },
		func() string {
			return fmt.Sprintf("PacketResponder %d for block %s terminating", r.Intn(3), blk())
		},
		func() string {
			return fmt.Sprintf("Received block %s of size %d from /%s", blk(), r.Intn(1<<26), ip())
		},
		func() string {
			return fmt.Sprintf("BLOCK* NameSystem.addStoredBlock: blockMap updated: %s is added to %s size %d", ip(), blk(), r.Intn(1<<26))
		},
		func() string {
			return fmt.Sprintf("BLOCK* NameSystem.allocateBlock: /user/root/rand/_temporary/_task_%d_m_%06d_0/part-%05d. %s", r.Intn(1e6), r.Intn(1000), r.Intn(1000), blk())
		},
		func() string {
			return fmt.Sprintf("Deleting block %s file /mnt/hadoop/dfs/data/current/subdir%d/%s", blk(), r.Intn(64), blk())
		},
	}

	d := NewDrain(DrainOptions{})
	for i := 0; i < 2000; i++ {
		d.Add(msg(models.LevelInfo, events[r.Intn(len(events))]()), t0)
	}
	if n := d.Len(); n < len(events) || n > 2*len(events) {
		for _, c := range d.Clusters() {
			t.Logf("%5d  %s", c.Size, c.Template())
		}
		t.Errorf("got %d templates for %d event types", n, len(events))
	}
}
