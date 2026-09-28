package indicator

import (
	"math"
	"testing"

	"github.com/quant4dad/internal/domain"
)

func mkBars(closes []float64) []*domain.Bar {
	bars := make([]*domain.Bar, len(closes))
	for i, c := range closes {
		bars[i] = &domain.Bar{Open: c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 100}
	}
	return bars
}

func TestMA(t *testing.T) {
	bars := mkBars([]float64{1, 2, 3, 4, 5})
	c, _ := Get("MA")
	s, err := c.Compute(bars, map[string]any{"period": 3})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Outputs["value"]
	want := []float64{math.NaN(), math.NaN(), 2, 3, 4}
	for i := range want {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				t.Errorf("MA[%d] got %v want NaN", i, got[i])
			}
			continue
		}
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("MA[%d] got %v want %v", i, got[i], want[i])
		}
	}
}

func TestEMA(t *testing.T) {
	bars := mkBars([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	c, _ := Get("EMA")
	s, err := c.Compute(bars, map[string]any{"period": 3})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Outputs["value"]
	if math.IsNaN(got[2]) || math.Abs(got[2]-2) > 1e-9 {
		t.Errorf("EMA[2] want 2 got %v", got[2])
	}
	// monotonically increasing input → monotonically increasing EMA
	for i := 3; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("EMA not monotonic at i=%d: %v then %v", i, got[i-1], got[i])
		}
	}
}

func TestRSI(t *testing.T) {
	// all gains → RSI should hit 100
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = float64(i + 1)
	}
	bars := mkBars(closes)
	c, _ := Get("RSI")
	s, err := c.Compute(bars, map[string]any{"period": 14})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Outputs["value"]
	if math.IsNaN(got[14]) {
		t.Fatal("RSI[14] is NaN")
	}
	if got[len(got)-1] < 99 {
		t.Errorf("RSI on monotonic up want ~100 got %v", got[len(got)-1])
	}
}

func TestMACD(t *testing.T) {
	closes := make([]float64, 60)
	for i := range closes {
		closes[i] = float64(i) + 100
	}
	bars := mkBars(closes)
	c, _ := Get("MACD")
	s, err := c.Compute(bars, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"dif", "dea", "macd"} {
		if _, ok := s.Outputs[k]; !ok {
			t.Errorf("MACD missing output %s", k)
		}
	}
}

func TestKDJ(t *testing.T) {
	closes := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	bars := mkBars(closes)
	c, _ := Get("KDJ")
	s, err := c.Compute(bars, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := s.Outputs["k"]
	if math.IsNaN(k[len(k)-1]) {
		t.Fatal("KDJ.k last NaN")
	}
}

func TestBOLL(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = 100
	}
	bars := mkBars(closes)
	c, _ := Get("BOLL")
	s, err := c.Compute(bars, map[string]any{"period": 20, "mult": 2.0})
	if err != nil {
		t.Fatal(err)
	}
	// constant series → std == 0 → upper == mid == lower at the tail
	last := len(closes) - 1
	if math.Abs(s.Outputs["upper"][last]-100) > 1e-9 {
		t.Errorf("BOLL upper want 100 got %v", s.Outputs["upper"][last])
	}
	if math.Abs(s.Outputs["lower"][last]-100) > 1e-9 {
		t.Errorf("BOLL lower want 100 got %v", s.Outputs["lower"][last])
	}
}

func TestATR(t *testing.T) {
	bars := mkBars([]float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25})
	c, _ := Get("ATR")
	s, err := c.Compute(bars, map[string]any{"period": 14})
	if err != nil {
		t.Fatal(err)
	}
	v := s.Outputs["value"]
	if math.IsNaN(v[len(v)-1]) || v[len(v)-1] <= 0 {
		t.Errorf("ATR last want positive got %v", v[len(v)-1])
	}
}
