package prompt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// TemplateInfo represents metadata and content of a prompt template.
type TemplateInfo struct {
	Name        string `json:"name"`
	Source      string `json:"source"` // "builtin" or file path
	Description string `json:"description"`
	Content     string `json:"content"`
}

// TemplateData holds variables available to templates during rendering.
type TemplateData struct {
	Message     string
	Output      string
	Link        string
	FromAgent   string
	FromSession string
	ToAgent     string
	ToSession   string
}

// Manager manages built-in and user-customized prompt templates.
type Manager struct {
	customDir string
	builtins  map[string]TemplateInfo
}

// NewDefaultManager creates a Manager configured with the default user template directory.
func NewDefaultManager() *Manager {
	var customDir string
	if home, err := os.UserHomeDir(); err == nil {
		customDir = filepath.Join(home, ".config", "relay", "templates")
	}
	return NewManager(customDir)
}

// NewManager creates a Manager with an explicit custom directory.
func NewManager(customDir string) *Manager {
	m := &Manager{
		customDir: customDir,
		builtins:  make(map[string]TemplateInfo),
	}
	m.registerBuiltins()
	return m
}

func (m *Manager) registerBuiltins() {
	m.builtins["verification"] = TemplateInfo{
		Name:        "verification",
		Source:      "builtin",
		Description: "Dual-engine review driving /verification-before-completion (ponytail-review + architecture/mopheus-code-review)",
		Content: strings.TrimSpace(`
请严格执行 ` + "`/verification-before-completion`" + ` 技能对本改动进行双引擎审查：

1. 【Phase A: ponytail-review】
   - 调用方冲击面（Caller Blast Radius）：检查改动函数/导出的外部调用方是否完整兼容；
   - 代码精简（Lean & Anti-Bloat）：检查是否有不必要的抽象、多余代码或可被现有公用函数替代的实现；
   - 并发与边界守卫：检查 check-then-write、竞态与异常分支。

2. 【Phase B: 架构合规审计】
   - 若在 enmotech/mopheus：执行 ` + "`mopheus-code-review`" + `，严格匹配改动文件的组件守卫（1A-1G）；
   - 若在其他仓库：执行通用质量守卫与 AGENTS.md / CLAUDE.md 规范。

3. 【裁决格式】
   - 必须在报告末尾给出明确判定：` + "`Verdict: APPROVED`" + ` 或 ` + "`Verdict: CHANGES REQUESTED (N blocking)`" + `。
{{if .Message}}
---
【上下文与改动详情】
{{.Message}}
{{end}}`),
	}

	m.builtins["re-review"] = TemplateInfo{
		Name:        "re-review",
		Source:      "builtin",
		Description: "Follow-up review verifying closure of blocking items from previous round",
		Content: strings.TrimSpace(`
请针对上一轮审查遗留意见的修复改动进行复核（执行 ` + "`/verification-before-completion`" + `）：

1. 核验上一轮的 [Blocking] 阻断项是否已全部关闭；
2. 检查本次修复代码是否引入新的破坏或多余代码（Phase A: ponytail-review）；
3. 检查架构合规性（Phase B）；
4. 给出终审裁决（` + "`Verdict: APPROVED`" + ` 或 ` + "`Verdict: CHANGES REQUESTED`" + `）。
{{if .Message}}
---
【修复说明与上下文】
{{.Message}}
{{end}}`),
	}
}

// List returns all available templates sorted by name.
// Custom templates in customDir override built-in templates with the same name.
func (m *Manager) List() ([]TemplateInfo, error) {
	templates := make(map[string]TemplateInfo)

	// 1. Add builtins
	for k, v := range m.builtins {
		templates[k] = v
	}

	// 2. Discover custom templates from customDir
	if m.customDir != "" {
		if entries, err := os.ReadDir(m.customDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				ext := filepath.Ext(name)
				if ext != ".tmpl" && ext != ".md" && ext != ".txt" {
					continue
				}
				baseName := strings.TrimSuffix(name, ext)
				fullPath := filepath.Join(m.customDir, name)
				content, err := os.ReadFile(fullPath)
				if err != nil {
					continue
				}
				templates[baseName] = TemplateInfo{
					Name:        baseName,
					Source:      fullPath,
					Description: fmt.Sprintf("Custom template from %s", fullPath),
					Content:     string(content),
				}
			}
		}
	}

	res := make([]TemplateInfo, 0, len(templates))
	for _, v := range templates {
		res = append(res, v)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Name < res[j].Name
	})
	return res, nil
}

// Get finds a template by name or file path.
func (m *Manager) Get(nameOrPath string) (*TemplateInfo, error) {
	// 1. If it directly points to an existing file on disk
	if _, err := os.Stat(nameOrPath); err == nil {
		content, err := os.ReadFile(nameOrPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read template file '%s': %w", nameOrPath, err)
		}
		baseName := strings.TrimSuffix(filepath.Base(nameOrPath), filepath.Ext(nameOrPath))
		return &TemplateInfo{
			Name:        baseName,
			Source:      nameOrPath,
			Description: fmt.Sprintf("Custom template file from %s", nameOrPath),
			Content:     string(content),
		}, nil
	}

	// 2. Check custom directory
	if m.customDir != "" {
		for _, ext := range []string{".tmpl", ".md", ".txt", ""} {
			candidate := filepath.Join(m.customDir, nameOrPath+ext)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				content, err := os.ReadFile(candidate)
				if err != nil {
					return nil, fmt.Errorf("failed to read custom template '%s': %w", candidate, err)
				}
				return &TemplateInfo{
					Name:        nameOrPath,
					Source:      candidate,
					Description: fmt.Sprintf("Custom template from %s", candidate),
					Content:     string(content),
				}, nil
			}
		}
	}

	// 3. Check builtins
	if tmpl, ok := m.builtins[nameOrPath]; ok {
		return &tmpl, nil
	}

	// Not found - collect available names for helpful error message
	all, _ := m.List()
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.Name)
	}
	return nil, fmt.Errorf("template '%s' not found (available templates: %s)", nameOrPath, strings.Join(names, ", "))
}

// Render compiles and renders the template using Go text/template syntax.
func (m *Manager) Render(nameOrPath string, data TemplateData) (string, error) {
	tmplInfo, err := m.Get(nameOrPath)
	if err != nil {
		return "", err
	}

	rawContent := tmplInfo.Content
	// If template does not contain template tags and Message is provided, append it safely
	if !strings.Contains(rawContent, "{{") && strings.TrimSpace(data.Message) != "" {
		rawContent = rawContent + "\n\n---\n" + strings.TrimSpace(data.Message)
	}

	tmpl, err := template.New(tmplInfo.Name).Parse(rawContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template '%s': %w", tmplInfo.Name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render template '%s': %w", tmplInfo.Name, err)
	}

	return strings.TrimSpace(buf.String()), nil
}
