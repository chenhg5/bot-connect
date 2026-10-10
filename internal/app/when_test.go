package app

import (
	"strings"
	"testing"
	"time"
)

func TestParseWhenChinese(t *testing.T) {
	sat := time.Date(2026, 10, 10, 20, 0, 0, 0, time.Local) // Saturday
	mon := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)  // Monday
	cases := []struct {
		now  time.Time
		in   string
		want string
	}{
		{sat, "下周三", "10-14 18:00"},
		{sat, "下周三前", "10-14 18:00"},
		{mon, "下周三（10月15日）", "10-21 18:00"},
		{mon, "下周三 (10/15)", "10-21 18:00"},
		{sat, "周三", "10-14 18:00"},
		{sat, "下周五 12:00", "10-16 12:00"},
		{sat, "下下周一", "10-19 18:00"},
		{sat, "明天下午", "10-11 18:00"},
		{sat, "后天上午", "10-12 12:00"},
		{sat, "月底", "10-31 18:00"},
		{sat, "17号", "10-17 18:00"},
		{sat, "5号", "11-05 18:00"},
		{mon, "周三", "10-14 18:00"},
		{mon, "下周三", "10-21 18:00"},
		{mon, "本周五", "10-16 18:00"},
		{mon, "今晚", "10-12 21:00"},
		{mon, "星期五 15:30", "10-16 15:30"},
		{mon, "10-17 18:00", "10-17 18:00"},
		{mon, "2h", "10-12 11:00"},
	}
	for _, c := range cases {
		got, err := ParseWhen(c.in, c.now)
		if err != nil || got.Format("01-02 15:04") != c.want {
			t.Errorf("%s on %s: got %v %v, want %s", c.in, c.now.Format("Mon"), got, err, c.want)
		}
	}
	if _, err := ParseWhen("下周三 10-14", mon); err == nil || !strings.Contains(err.Error(), "10-21") {
		t.Fatalf("a conflicting date must be pointed out: %v", err)
	}
	if got, _ := ParseWhen("下周三 10-21", mon); got == nil || got.Format("01-02") != "10-21" {
		t.Fatal("a matching date is fine")
	}
	if d := DescribeDate(time.Date(2026, 10, 21, 18, 0, 0, 0, time.Local), mon); d != "10-21 周三（下周，10-19 那一周） 18:00" {
		t.Fatal(d)
	}
	if _, err := ParseWhen("某天", sat); err == nil {
		t.Fatal("nonsense accepted")
	}
}
