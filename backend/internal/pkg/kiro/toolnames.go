package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// maxToolNameLength 是发给 Kiro 的工具名长度上限——参考实现 AIClient2API
// （KIRO_MAX_TOOL_NAME_LENGTH）用的同一个值。这个具体数字没有被我们自己的
// 真实账号测试确认过，取一个参考实现验证过的值，好过自己随手拍一个。
const maxToolNameLength = 64

// ToolNameMap 记录一次请求内「客户端原始工具名 → 发给 Kiro 的安全名字」的
// 双向映射。同一个实例必须贯穿 kiro.BuildRequest（写映射：工具声明与历史
// tool_use 都要转换）与 StreamTranslator/非流式响应转换（读映射：把 Kiro
// 回显的名字换回客户端原名）——两侧共享同一个实例，否则同一个原始名字在
// 请求侧与响应侧可能因为遇到顺序不同而映射到不一样的安全名字，导致客户端
// 收到一个自己从未声明过的工具名。
//
// 背景（移植自参考实现，未经我们自己真实账号验证）：交叉阅读三份参考实现
// 源码（Kiro-Go、kiro2cc-proxy、AIClient2API）时发现，Kiro-Go
// （proxy/translator.go 的 sanitizeToolName）断言 CodeWhisperer 只接受纯
// camelCase 工具名（不含下划线/连字符），非法名字会让整个请求 400；
// AIClient2API 独立实现了同样的截断+哈希改名策略。这条约束一旦为真，最
// 典型的触发场景是 MCP 工具——`mcp__server__tool_name` 这类双下划线命名
// 在 Kiro 场景下会必现 400，而且现象会长得很像历史上的 "Kiro400" 事故，
// 不会有人往工具名上想。对已经是"纯字母数字、不以数字开头"的名字（真实
// 场景下的绝大多数工具名），Sanitize 是完全的空操作，本次改动前的行为不变，
// 代价可控；假设一旦成立，收益是避免一整类 MCP 场景下的 Kiro400。上线前
// 应尽量找一个真实账号 + 一个挂了 MCP 工具的分组验证一次。
type ToolNameMap struct {
	toKiro   map[string]string
	toClient map[string]string
	used     map[string]bool
}

// NewToolNameMap 创建一个空映射。
//
// nil 接收者上的 ToKiro/ToClient 都是安全的空操作（原样返回输入，不做任何
// 转换）——未接线的调用方（测试、以后新增的调用点忘记构造映射）不会因此
// panic，只是拿不到 sanitize，与本包其它按需开启的机制（PromptCacheTracker
// 等）保持同样的降级习惯。
func NewToolNameMap() *ToolNameMap {
	return &ToolNameMap{
		toKiro:   make(map[string]string),
		toClient: make(map[string]string),
		used:     make(map[string]bool),
	}
}

// ToKiro 返回 name 发给 Kiro 时应该使用的安全名字。同一个 map 实例内，
// 同一个 name 恒定返回同一个结果——不管是被工具声明（processTools）还是
// 历史 tool_use（assistantEntry）先调用到。
func (m *ToolNameMap) ToKiro(name string) string {
	if m == nil || name == "" {
		return name
	}
	if v, ok := m.toKiro[name]; ok {
		return v
	}

	safe := sanitizeToolName(name)
	for attempt := 1; m.used[safe]; attempt++ {
		// 两个不同的原始名字清洗后恰好相同（如 "foo-bar" 与 "foo_bar" 都会
		// 变成 "fooBar"）——基于原始名字 + 尝试序号重新算一个确定性的短
		// 哈希后缀。attempt 保证即便在极小概率的哈希碰撞下也能在有限步内
		// 终止，不会让 ToKiro 陷入死循环。
		safe = disambiguateToolName(name, attempt)
	}

	m.toKiro[name] = safe
	m.toClient[safe] = name
	m.used[safe] = true
	return safe
}

// ToClient 把 Kiro 回显的工具名换回客户端原名。找不到映射（这个名字从未
// 经过 ToKiro 转换，或 map 为 nil）时原样返回——调用方不需要区分"没转换过"
// 和"map 未接线"这两种情况，行为一致。
func (m *ToolNameMap) ToClient(kiroName string) string {
	if m == nil {
		return kiroName
	}
	if v, ok := m.toClient[kiroName]; ok {
		return v
	}
	return kiroName
}

// isSafeToolName 判断一个名字是否已经是 Kiro（据信）能接受的形态：纯 ASCII
// 字母数字、且不以数字开头。已经合法的名字必须原样返回、不做任何改写——
// 这是本机制"对绝大多数工具名零行为变化"的关键。
func isSafeToolName(name string) bool {
	if name == "" || len(name) > maxToolNameLength {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// sanitizeToolName 把任意名字转成纯 camelCase：分隔符（下划线/连字符/空格/
// 其它非字母数字字符）本身丢弃，紧跟其后的字母大写；分隔符之外的非字母数字
// 字符同样整体丢弃。超长时截断并追加原始名字的短哈希后缀，避免仅靠截断
// 导致不同名字碰撞。
func sanitizeToolName(name string) string {
	if isSafeToolName(name) {
		return name
	}

	var b strings.Builder
	upperNext := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			if upperNext && r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			_, _ = b.WriteRune(r)
			upperNext = false
		default:
			upperNext = true
		}
	}

	out := b.String()
	if out == "" {
		// 名字里没有任何字母数字字符（纯符号/纯 emoji 之类的极端输入）——
		// 给一个固定前缀，好过发一个空字符串工具名给上游。
		out = "tool"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "t" + out
	}
	if len(out) > maxToolNameLength {
		out = truncateWithHash(out, name, "")
	}
	return out
}

// disambiguateToolName 是 ToKiro 碰撞重试用的候选生成器。
func disambiguateToolName(name string, attempt int) string {
	base := sanitizeToolName(name)
	return truncateWithHash(base, name, strconv.Itoa(attempt))
}

// truncateWithHash 把 base 截断到给自身与哈希后缀腾出空间，后缀取
// sha256(name + salt) 的前 6 个十六进制字符——足够小的碰撞概率，换来比
// 完整哈希更短的工具名。
func truncateWithHash(base, name, salt string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + salt))
	suffix := hex.EncodeToString(sum[:])[:6]
	limit := max(maxToolNameLength-len(suffix), 0)
	if len(base) > limit {
		base = base[:limit]
	}
	return base + suffix
}
