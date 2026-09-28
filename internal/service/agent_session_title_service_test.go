package service

import (
	"strings"
	"testing"
)

func TestNormalizeAgentSessionTitle(t *testing.T) {
	for input, want := range map[string]string{
		"  “趋势策略分析”  ": "趋势策略分析", "回测 🐉": "回测 🐉", "": "", "\xff": "",
		"第一行\n第二行": "", "<think>标题</think>": "", "```标题```": "", "{\"title\":\"标题\"}": "",
		"标题\x00": "", "隐藏\u202e标题": "", strings.Repeat("中", 49): "",
		strings.Repeat("中", 48): strings.Repeat("中", 48),
	} {
		if got := NormalizeAgentSessionTitle(input); got != want {
			t.Fatalf("input %q: got %q, want %q", input, got, want)
		}
	}
}
