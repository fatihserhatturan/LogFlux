// Package detect finds anomalies in the log stream: new error patterns,
// error-rate spikes, silent sources and user-defined threshold rules.
package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Wildcard replaces variable tokens in templates
const Wildcard = "<*>"

// masks replace common variable values before clustering (applied in order).
// This follows Drain3's masking, which noticeably improves template quality.
var masks = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<UUID>"},
	{regexp.MustCompile(`\b[\w.+-]+@[\w-]+\.[\w.-]+\b`), "<EMAIL>"},
	{regexp.MustCompile(`\b\d{4}[-/]\d{2}[-/]\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`), "<DATE>"},
	{regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`), "<IP>"},
	{regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?\b`), "<TIME>"},
	{regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{1,4}\b`), "<IP>"},
	{regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`), "<HEX>"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`), "<HEX>"},
	// Numbers after a delimiter (keeps "user=42" as one token: "user=<NUM>")
	{regexp.MustCompile(`(^|[\s=:(\[,#])[-+]?\d+(?:\.\d+)?(?:ms|s|m|h|kb|mb|gb|%)?\b`), "${1}<NUM>"},
}

// DrainOptions configures the template miner
type DrainOptions struct {
	Depth       int     // parse tree depth incl. root, length and leaf levels (default 4, min 4)
	Similarity  float64 // min share of matching tokens to join a cluster (default 0.4)
	MaxChildren int     // max children per tree node before falling back to <*> (default 100)
	MaxClusters int     // stop creating clusters above this (default 50000)
}

// Cluster is a log template and its statistics
type Cluster struct {
	ID        int              `json:"id"`
	Tokens    []string         `json:"tokens"`
	Size      int64            `json:"size"`
	FirstSeen time.Time        `json:"first_seen"`
	LastSeen  time.Time        `json:"last_seen"`
	MaxLevel  models.LogLevel  `json:"max_level"`
	Sources   map[string]int64 `json:"sources"`
	Sample    string           `json:"sample"`
}

// Template returns the template text
func (c *Cluster) Template() string {
	return strings.Join(c.Tokens, " ")
}

type node struct {
	children map[string]*node
	clusters []*Cluster
}

func newNode() *node {
	return &node{children: make(map[string]*node)}
}

// Drain is an online log template miner (He et al., "Drain: An Online Log
// Parsing Approach with Fixed Depth Tree", ICWS 2017). It is safe for
// concurrent use.
type Drain struct {
	opts DrainOptions

	mu       sync.RWMutex
	root     *node
	clusters map[int]*Cluster
	nextID   int
}

// NewDrain creates an empty template miner
func NewDrain(opts DrainOptions) *Drain {
	if opts.Depth < 4 {
		opts.Depth = 4
	}
	if opts.Similarity <= 0 || opts.Similarity > 1 {
		opts.Similarity = 0.4
	}
	if opts.MaxChildren <= 0 {
		opts.MaxChildren = 100
	}
	if opts.MaxClusters <= 0 {
		opts.MaxClusters = 50000
	}
	return &Drain{opts: opts, root: newNode(), clusters: make(map[int]*Cluster), nextID: 1}
}

// Tokenize masks variables and splits a message into tokens
func Tokenize(msg string) []string {
	// Only the first line matters for multiline entries (stack traces)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	for _, m := range masks {
		msg = m.re.ReplaceAllString(msg, m.repl)
	}
	return strings.Fields(msg)
}

// Add assigns e to a cluster, creating one if no template is similar enough.
// It returns the cluster (nil when MaxClusters is reached) and whether it is new.
func (d *Drain) Add(e *models.LogEntry, now time.Time) (*Cluster, bool) {
	tokens := Tokenize(e.Message)

	d.mu.Lock()
	defer d.mu.Unlock()

	c := d.search(tokens)
	created := false
	if c != nil {
		// Generalise positions that differ
		for i, tok := range tokens {
			if c.Tokens[i] != tok && c.Tokens[i] != Wildcard {
				c.Tokens[i] = Wildcard
			}
		}
	} else {
		if len(d.clusters) >= d.opts.MaxClusters {
			return nil, false
		}
		c = &Cluster{
			ID:        d.nextID,
			Tokens:    append([]string(nil), tokens...),
			FirstSeen: now,
			Sources:   make(map[string]int64),
			Sample:    truncate(e.Message, 500),
			MaxLevel:  e.Level,
		}
		d.nextID++
		d.clusters[c.ID] = c
		d.insert(c)
		created = true
	}

	c.Size++
	c.LastSeen = now
	if e.Level.Severity() > c.MaxLevel.Severity() {
		c.MaxLevel = e.Level
		c.Sample = truncate(e.Message, 500)
	}
	if _, ok := c.Sources[e.Source]; ok || len(c.Sources) < 20 {
		c.Sources[e.Source]++
	}
	return c, created
}

// search finds the most similar cluster for tokens, or nil
func (d *Drain) search(tokens []string) *Cluster {
	n := d.root.children[strconv.Itoa(len(tokens))]
	if n == nil {
		return nil
	}
	for depth := 0; depth < d.prefixLen() && depth < len(tokens); depth++ {
		next := n.children[tokens[depth]]
		if next == nil {
			next = n.children[Wildcard]
		}
		if next == nil {
			return nil
		}
		n = next
	}

	var best *Cluster
	bestSim, bestParams := -1.0, -1
	for _, c := range n.clusters {
		sim, params := similarity(c.Tokens, tokens)
		if sim > bestSim || (sim == bestSim && params > bestParams) {
			best, bestSim, bestParams = c, sim, params
		}
	}
	if best == nil || bestSim < d.opts.Similarity {
		return nil
	}
	return best
}

// insert adds a cluster to the parse tree
func (d *Drain) insert(c *Cluster) {
	lengthKey := strconv.Itoa(len(c.Tokens))
	n := d.root.children[lengthKey]
	if n == nil {
		n = newNode()
		d.root.children[lengthKey] = n
	}
	for depth := 0; depth < d.prefixLen() && depth < len(c.Tokens); depth++ {
		tok := c.Tokens[depth]
		if hasDigit(tok) {
			tok = Wildcard
		}
		next := n.children[tok]
		if next == nil {
			if len(n.children) >= d.opts.MaxChildren {
				tok = Wildcard
				next = n.children[Wildcard]
			}
			if next == nil {
				next = newNode()
				n.children[tok] = next
			}
		}
		n = next
	}
	n.clusters = append(n.clusters, c)
}

// prefixLen is how many leading tokens route a message through the tree.
// Depth counts the root, the length level and the leaf, as in Drain3, where
// the default depth of 4 routes by the first token only.
func (d *Drain) prefixLen() int {
	return d.opts.Depth - 3
}

// similarity returns the share of equal tokens and the number of wildcards
func similarity(template, tokens []string) (float64, int) {
	if len(template) == 0 {
		return 1, 0
	}
	same, params := 0, 0
	for i, t := range template {
		switch t {
		case Wildcard:
			params++
		case tokens[i]:
			same++
		}
	}
	return float64(same) / float64(len(template)), params
}

// Get returns a copy of a cluster by ID
func (d *Drain) Get(id int) (Cluster, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c, ok := d.clusters[id]
	if !ok {
		return Cluster{}, false
	}
	return copyCluster(c), true
}

// Clusters returns copies of all clusters
func (d *Drain) Clusters() []Cluster {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Cluster, 0, len(d.clusters))
	for _, c := range d.clusters {
		out = append(out, copyCluster(c))
	}
	return out
}

// Len returns the number of clusters
func (d *Drain) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.clusters)
}

// Save writes all clusters to path atomically
func (d *Drain) Save(path string) error {
	data, err := json.Marshal(d.Clusters())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load restores clusters saved with Save (a missing file is not an error)
func (d *Drain) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var clusters []*Cluster
	if err := json.Unmarshal(data, &clusters); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range clusters {
		if c.Sources == nil {
			c.Sources = make(map[string]int64)
		}
		d.clusters[c.ID] = c
		d.insert(c)
		if c.ID >= d.nextID {
			d.nextID = c.ID + 1
		}
	}
	return nil
}

func copyCluster(c *Cluster) Cluster {
	out := *c
	out.Tokens = append([]string(nil), c.Tokens...)
	out.Sources = make(map[string]int64, len(c.Sources))
	for k, v := range c.Sources {
		out.Sources[k] = v
	}
	return out
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
