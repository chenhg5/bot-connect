package brain

import (
	"strings"
	"testing"
	"time"
)

func TestCalendar(t *testing.T) {
	sat := time.Date(2026, 10, 10, 20, 0, 0, 0, time.UTC)
	c := Calendar(sat)
	for _, want := range []string{"今天 10-10 周六", "本周 日10-11", "下周 一10-12", "三10-14", "下下周 一10-19"} {
		if !strings.Contains(c, want) {
			t.Fatalf("%q missing in %s", want, c)
		}
	}
	mon := Calendar(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if !strings.Contains(mon, "本周 二10-13") || !strings.Contains(mon, "下周 一10-19") {
		t.Fatal(mon)
	}
}
