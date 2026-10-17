package delegation

import (
	"strings"
	"testing"
)

func goodCard() Card {
	return Card{
		Ask:       "年会场地选 A 酒店还是 B 园区？",
		Situation: "年会 12 月 20 日，80 人。两家都还有档期，报价都在预算内。A 在市中心，B 离公司近但要自己搭舞台。",
		Example:   "按往年流程：A 包场地、音响和餐，B 只给场地，舞台和餐要另外找三家供应商。",
		Options: []CardOption{
			{Key: "A", Label: "A 酒店", Then: "本周签约，我只需对接一家，11 月底前能全部确认", Cost: "比 B 贵 1.2 万，大家通勤平均多 30 分钟"},
			{Key: "B", Label: "B 园区", Then: "省 1.2 万，我要另外找舞台和餐饮，12 月初才能定完", Cost: "多三家供应商，任何一家掉链子都可能延期"},
		},
		Recommend:  "A",
		Why:        "日期只剩两个月，少一个环节就少一个延期的机会，1.2 万在预算内。",
		Verified:   []string{"两家 12 月 20 日都有档期，报价单已收到"},
		Unverified: []string{"A 的停车位够不够 80 人，我明天去现场确认，你不用管"},
		IfNoAnswer: "周五前不回复我就先给 A 交定金锁档期（可退）。",
	}
}

func TestCardProblems(t *testing.T) {
	if p := goodCard().Problems(); len(p) != 0 {
		t.Fatalf("a complete card passes: %v", p)
	}
	c := goodCard()
	c.Ask = "选 A 吗？另外要不要顺便订大巴？"
	if !strings.Contains(strings.Join(c.Problems(), ";"), "只问一个问题") {
		t.Fatal("one question")
	}
	c = goodCard()
	c.Options = c.Options[:1]
	if len(c.Problems()) == 0 {
		t.Fatal("at least two options")
	}
	c = goodCard()
	c.Options[1].Then = ""
	c.Recommend = "C"
	if p := strings.Join(c.Problems(), ";"); !strings.Contains(p, "then") || !strings.Contains(p, "recommend") {
		t.Fatalf("consequences and a recommendation: %s", p)
	}
	c = goodCard()
	c.Situation = strings.Repeat("情况很复杂，", 60)
	if !strings.Contains(strings.Join(c.Problems(), ";"), "30 秒读不完") {
		t.Fatal("too long to read in thirty seconds")
	}
	r := goodCard().Render()
	if !strings.HasPrefix(r, "**年会场地选 A 酒店还是 B 园区？**") || !strings.Contains(r, "A. A 酒店** 👈 推荐") || !strings.Contains(r, "细节（可跳过）") {
		t.Fatalf("question first, recommendation marked, details last:\n%s", r)
	}
}

func TestUnexplained(t *testing.T) {
	got := Unexplained("可能会晚一点，我再看一下")
	if !strings.Contains(strings.Join(got, ","), "可能会") || !strings.Contains(strings.Join(got, ","), "看一下") {
		t.Fatalf("vague phrases are flagged: %v", got)
	}
	if got := Unexplained("场地已确认"); len(got) != 0 {
		t.Fatalf("plain facts pass: %v", got)
	}
	if got := Unexplained("走 OA 流程", "OA"); len(got) != 1 {
		t.Fatalf("a project's own terms come from data: %v", got)
	}
	if got := Unexplained("走 OA（公司的审批系统）流程", "OA"); len(got) != 0 {
		t.Fatalf("explained right after is fine: %v", got)
	}
}
