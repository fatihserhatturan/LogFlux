package metrics

import (
	"strings"
	"sync"
	"testing"
)

func TestRegistry_WriteTo(t *testing.T) {
	reg := NewRegistry()
	c := reg.NewCounterVec("test_total", "A test counter.", "input", "level")
	c.Inc("app", "ERROR")
	c.Add(2, "app", "ERROR")
	c.Inc("sys", "INFO")
	reg.NewGaugeFunc("test_gauge", "A gauge.", func() float64 { return 7 })
	f := reg.NewFuncVec("test_func_total", "A func counter.", "counter", "input")
	f.Set(`we"ird`, func() float64 { return 4 })

	var b strings.Builder
	if _, err := reg.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	for _, want := range []string{
		"# TYPE test_total counter",
		`test_total{input="app",level="ERROR"} 3`,
		`test_total{input="sys",level="INFO"} 1`,
		"# TYPE test_gauge gauge",
		"test_gauge 7",
		`test_func_total{input="we\"ird"} 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if c.Value("app", "ERROR") != 3 {
		t.Errorf("Value = %d, want 3", c.Value("app", "ERROR"))
	}
}

func TestCounterVec_Concurrent(t *testing.T) {
	c := NewRegistry().NewCounterVec("c", "c", "k")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Inc("x")
			}
		}()
	}
	wg.Wait()
	if got := c.Value("x"); got != 8000 {
		t.Errorf("got %d, want 8000", got)
	}
}
