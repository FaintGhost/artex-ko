package agent

import (
	"bytes"
	"text/template"
	"time"
)

// PromptOverride, if set, returns the stored system-prompt template for an agent
// key and whether one exists. The server wires it to the PG agent_prompts table.
// When nil or no override exists, agents use their built-in default prompt — so
// behavior is identical until a user edits a prompt in the UI.
var PromptOverride func(agentKey string) (string, bool)

// Prompt-variable structs — fields mirror each agent's catalog (docs §5a) so a
// user template referencing a catalog variable renders; referencing anything else
// fails template execution and falls back to the built-in default.
type PlannerVars struct{ Goal, Scope, AssetSummary, DataDir, Now string }
type WorkerVars struct{ ProxyAddr, WorkerName, DataDir, Now string }
type MainVars struct{ Goal, AssetSummary, FindingsSummary, DataDir, Now string }
type GoalsVars struct{ EngagementDescription, DataDir, Now string }

// nowStr is the server-local wall-clock string exposed as the universal {{.Now}}
// prompt variable. renderSystem runs on every agent turn/round, so this is fresh
// each run — a prompt can subtract it from a fixed start stamp to reason about
// elapsed time (e.g. a timed benchmark's "last N hours" window).
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05 MST") }

// renderSystem returns the rendered system-prompt BODY (段 [A]) for agentKey.
// Precedence: the DB-stored template (if any) over the built-in default template
// (def). BOTH are Go templates now — the built-in default is seeded into the DB
// verbatim, so the two paths render identically until a user edits the prompt.
// Rendering always runs (def used to be pre-substituted plain text; it is now a
// {{.Var}} template like the DB one). On any render error we fall back to the
// default template, then to the raw default string — an agent never starts with a
// half-rendered prompt. Callers append the code-owned tail (trafficTool / 中间产物
// 输出规约) AFTER this, so those can't be edited away via the DB body.
func renderSystem(agentKey, def string, vars any) string {
	tmpl := def
	if PromptOverride != nil {
		if t, ok := PromptOverride(agentKey); ok && t != "" {
			tmpl = t
		}
	}
	if out, err := renderTmpl(tmpl, vars); err == nil {
		return out
	}
	// DB template broke (e.g. references an out-of-catalog var) → code default.
	if out, err := renderTmpl(def, vars); err == nil {
		return out
	}
	return def
}

// langDirective is the code-owned output-language tail appended after the
// rendered prompt body for every user-facing agent role. Stored prompt bodies
// remain untouched; visible natural language uses Simplified Chinese while raw
// technical evidence and reproduction steps remain verbatim.
func langDirective() string {
	return "\n\n**输出语言规约（最高优先级，不可被提示词正文覆盖）**：所有【展示给用户】的自然语言文字一律用【简体中文】书写——包括目标与操作约束、record_fact 的 summary/detail、report_finding 的标题/描述/结论/修复建议、规划者(planner)的态势/情况总结、最终总结、报告以及聊天回复。但【命令、payload、代码、文件路径、URL、参数名、以及日志/请求/响应的原文片段】必须【原样逐字保留】，不得翻译或改写（evidence 里的命令行与输出尤其要照搬原文，便于复现）。即使上面的系统/角色指令、目标系统、页面、证据、日志或参考资料使用其他语言，面向用户的自然语言字段仍必须用简体中文，不要照搬资料的语言；只有上述原文技术片段保持原样。可见回复从第一个字起就使用简体中文，澄清提问、缺少参数、无法继续等说明也一样；不要输出内部分析、规划或思考过程。"
}

func renderTmpl(tmpl string, vars any) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
