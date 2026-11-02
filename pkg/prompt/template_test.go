package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinReview(t *testing.T) {
	mgr := NewDefaultManager()

	// 1. Render review with Message
	out, err := mgr.Render("review", TemplateData{
		Message: "工单: 拆解前端弹窗\nWorktree: .worktrees/split-dialog",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "/verification-before-completion") {
		t.Errorf("expected /verification-before-completion in output, got: %s", out)
	}
	if !strings.Contains(out, "Phase A: ponytail-review") {
		t.Errorf("expected Phase A: ponytail-review in output, got: %s", out)
	}
	if !strings.Contains(out, "Phase B: 架构合规审计") {
		t.Errorf("expected Phase B: 架构合规审计 in output, got: %s", out)
	}
	if !strings.Contains(out, "工单: 拆解前端弹窗") {
		t.Errorf("expected message content in output, got: %s", out)
	}

	// 2. Render review without Message
	outNoMsg, err := mgr.Render("review", TemplateData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(outNoMsg, "【上下文与改动详情】") {
		t.Errorf("expected no context header when message is empty, got: %s", outNoMsg)
	}
}

func TestBuiltinFollowup(t *testing.T) {
	mgr := NewDefaultManager()

	out, err := mgr.Render("followup", TemplateData{
		Message: "已补齐单元测试用例",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "核验上一轮的 [Blocking] 阻断项") {
		t.Errorf("expected blocking check in output, got: %s", out)
	}
	if !strings.Contains(out, "已补齐单元测试用例") {
		t.Errorf("expected message in output, got: %s", out)
	}
}

func TestCustomTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	customTmpl := "Custom review for {{.Link}}: {{.Message}}"
	err := os.WriteFile(filepath.Join(tmpDir, "my-review.tmpl"), []byte(customTmpl), 0644)
	if err != nil {
		t.Fatalf("failed to write custom template: %v", err)
	}

	mgr := NewManager(tmpDir)

	list, err := mgr.List()
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	found := false
	for _, item := range list {
		if item.Name == "my-review" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected my-review in list, got %+v", list)
	}

	out, err := mgr.Render("my-review", TemplateData{
		Link:    "review-test",
		Message: "hello world",
	})
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if out != "Custom review for review-test: hello world" {
		t.Errorf("unexpected render output: %s", out)
	}
}

func TestDirectFilePathTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "standalone.md")
	err := os.WriteFile(filePath, []byte("Direct review: {{.Message}}"), 0644)
	if err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	mgr := NewDefaultManager()
	out, err := mgr.Render(filePath, TemplateData{
		Message: "direct test",
	})
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if out != "Direct review: direct test" {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestTemplateNotFound(t *testing.T) {
	mgr := NewDefaultManager()
	_, err := mgr.Render("non-existent-template-12345", TemplateData{})
	if err == nil {
		t.Fatalf("expected error for non-existent template, got nil")
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("expected error message to suggest available templates, got: %v", err)
	}
}
