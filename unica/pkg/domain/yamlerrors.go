package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// FriendlyYAMLError translates a yaml.v3 parse error or an Ontology.Validate
// error into wording an operator without Go experience can act on, keeping
// whatever line number the underlying error carries. ParseYAML's callers
// (facts.checkYAML, in particular — the portal's own validate/publish
// requests go through it) pass their error through this function so nobody
// has to learn what "field x not found in type domain.Ontology" means.
//
// An error shape this function does not recognise is returned unchanged
// rather than guessed at: a wrong translation sends a person to fix the
// wrong line, which is strictly worse than an English passthrough they can
// at least paste into a search box.
func FriendlyYAMLError(err error) string {
	if err == nil {
		return ""
	}
	// ParseYAML's only wrapping around a decode failure; nothing downstream
	// of it adds another layer before checkYAML sees the error.
	msg := strings.TrimPrefix(err.Error(), "parse ontology: ")

	if friendly, ok := translateUnmarshalErrors(msg); ok {
		return friendly
	}
	if friendly, ok := translateSyntaxError(msg); ok {
		return friendly
	}
	if friendly, ok := translateScalarError(msg); ok {
		return friendly
	}
	if friendly, ok := translateValidateError(msg); ok {
		return friendly
	}
	return msg
}

// --- yaml.v3 decode errors --------------------------------------------------
//
// dec.KnownFields(true) makes yaml.v3 batch every unmarshal problem it finds
// into one *yaml.TypeError, rendered as "yaml: unmarshal errors:" followed by
// one "  line N: <detail>" per problem. Each detail is translated on its own;
// an unrecognised detail still gets the "第 N 行：" frame (that much is a
// fact, not a guess) with its original English kept after it.

var reUnmarshalHeader = regexp.MustCompile(`^yaml: unmarshal errors:\n`)
var reUnmarshalLine = regexp.MustCompile(`^  line (\d+): (.+)$`)

func translateUnmarshalErrors(msg string) (string, bool) {
	if !reUnmarshalHeader.MatchString(msg) {
		return "", false
	}
	rest := reUnmarshalHeader.ReplaceAllString(msg, "")
	lines := strings.Split(rest, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		m := reUnmarshalLine.FindStringSubmatch(line)
		if m == nil {
			out = append(out, line)
			continue
		}
		lineNo, detail := m[1], m[2]
		out = append(out, fmt.Sprintf("第 %s 行：%s", lineNo, translateUnmarshalDetail(detail)))
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, "；"), true
}

var reFieldNotFound = regexp.MustCompile(`^field (\S+) not found in type domain\.\w+$`)
var reDuplicateKey = regexp.MustCompile(`^mapping key "(.*)" already defined at line (\d+)$`)
var reCannotUnmarshalValue = regexp.MustCompile("^cannot unmarshal (!![a-z]+) `(.*)` into (\\S+)$")
var reCannotUnmarshalBare = regexp.MustCompile(`^cannot unmarshal (!![a-z]+) into (\S+)$`)

func translateUnmarshalDetail(detail string) string {
	if m := reFieldNotFound.FindStringSubmatch(detail); m != nil {
		return fmt.Sprintf("出现未声明的字段「%s」，请检查字段名有没有写错、或者是不是缩进到了不该在的层级", m[1])
	}
	if m := reDuplicateKey.FindStringSubmatch(detail); m != nil {
		return fmt.Sprintf("键「%s」重复声明了（另一处在第 %s 行），请合并或删掉其中一个", m[1], m[2])
	}
	if m := reCannotUnmarshalValue.FindStringSubmatch(detail); m != nil {
		return fmt.Sprintf("这里的值类型不对——写的是%s「%s」，但这个位置需要%s",
			yamlTagZH(m[1]), m[2], goTypeZH(m[3]))
	}
	if m := reCannotUnmarshalBare.FindStringSubmatch(detail); m != nil {
		return fmt.Sprintf("这里的内容类型不对——写成了%s，但这个位置需要%s",
			yamlTagZH(m[1]), goTypeZH(m[2]))
	}
	return detail
}

func yamlTagZH(tag string) string {
	switch tag {
	case "!!str":
		return "文本"
	case "!!int":
		return "整数"
	case "!!float":
		return "小数"
	case "!!bool":
		return "布尔值（true/false）"
	case "!!map":
		return "映射（键值对）"
	case "!!seq":
		return "列表"
	case "!!null":
		return "空值"
	default:
		return strings.TrimPrefix(tag, "!!")
	}
}

func goTypeZH(t string) string {
	switch t {
	case "string":
		return "文本"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return "整数"
	case "float32", "float64":
		return "小数"
	case "bool":
		return "布尔值（true/false）"
	default:
		if strings.HasPrefix(t, "[]") || strings.HasPrefix(t, "map[") {
			return "一个列表或映射"
		}
		if strings.Contains(t, ".") {
			// A domain.* struct: the field wants a nested block (indented
			// key: value pairs), not a scalar.
			return "结构化的内容（缩进写出的一组键值对），而不是单个值"
		}
		return t
	}
}

// --- yaml.v3 syntax errors ---------------------------------------------------
//
// Errors that never reach the unmarshal stage: the document itself does not
// parse as YAML. These come back as a single "yaml: line N: <detail>" line.

var reSyntaxError = regexp.MustCompile(`^yaml: line (\d+): (.+)$`)

// reSyntaxErrorNoLine catches the scanner errors yaml.v3 raises before it has
// any line to report (a document that never resolves to valid YAML at all,
// like a bare "::: not yaml :::"). These come back as "yaml: <detail>" with
// no "line N:" component, so reSyntaxError never matches them — without this
// fallback, a detail this file already knows how to translate (e.g. "mapping
// values are not allowed in this context") would leak straight through in
// English just because this particular occurrence has no line number.
var reSyntaxErrorNoLine = regexp.MustCompile(`^yaml: (.+)$`)

func translateSyntaxError(msg string) (string, bool) {
	if m := reSyntaxError.FindStringSubmatch(msg); m != nil {
		lineNo, detail := m[1], m[2]
		zh, _ := translateSyntaxDetail(detail)
		return fmt.Sprintf("第 %s 行：%s", lineNo, zh), true
	}
	if m := reSyntaxErrorNoLine.FindStringSubmatch(msg); m != nil {
		// No line number to frame the message with here, so only surface a
		// translation when the detail is one this file actually recognises;
		// an unrecognised detail falls through to FriendlyYAMLError's
		// unchanged-passthrough rather than being framed with nothing to add.
		if zh, ok := translateSyntaxDetail(m[1]); ok {
			return zh, true
		}
	}
	return "", false
}

func translateSyntaxDetail(detail string) (string, bool) {
	switch {
	case detail == "mapping values are not allowed in this context":
		return "这一行的写法不对，常见原因是 `:` 后面多写了内容，或者缩进把这一行放到了不该在的层级，请对照上下文检查缩进", true
	case detail == "found a tab character that violates indentation":
		return "这一行用 Tab 缩进了——YAML 只能用空格缩进，请把 Tab 换成空格", true
	case strings.HasPrefix(detail, "did not find expected ',' or ']'"):
		return "方括号 `[ ]` 里的列表没写完整，缺少逗号或右中括号", true
	case strings.HasPrefix(detail, "did not find expected ',' or '}'"):
		return "花括号 `{ }` 里的映射没写完整，缺少逗号或右花括号", true
	case strings.HasPrefix(detail, "did not find expected key"):
		return "这里应该是一个键（key: 值），但没找到，请检查缩进或有没有多写了符号", true
	case strings.HasPrefix(detail, "did not find expected node content"):
		return "这里缺少内容，请检查是不是少写了取值，或者多写了一个冒号", true
	case strings.HasPrefix(detail, "block sequence entries are not allowed in this context"):
		return "这一行不能直接写列表项（- ...），请检查上一行是不是漏写了键名和冒号，或者缩进层级不对", true
	default:
		// Frame (when there is one) is still Chinese and the line number is
		// real; the detail itself is left in English rather than
		// mistranslated.
		return detail, false
	}
}

// --- StringList's own UnmarshalYAML errors -----------------------------------
//
// These come straight back from yaml.go without the "yaml: unmarshal
// errors:" wrapper yaml.v3 adds for struct-tag mismatches, because they are
// returned by StringList.UnmarshalYAML itself.

var reScalarError = regexp.MustCompile(`^line (\d+): (assertion values must be scalars|expected a scalar or a sequence)$`)

func translateScalarError(msg string) (string, bool) {
	m := reScalarError.FindStringSubmatch(msg)
	if m == nil {
		return "", false
	}
	lineNo := m[1]
	var detail string
	if m[2] == "assertion values must be scalars" {
		detail = "这里的取值必须是简单的数字或文本，不能是列表或映射嵌套在里面"
	} else {
		detail = "这里的取值格式不对，应该写成单个值，或者用 `[a, b]` 这样的列表写多个值"
	}
	return fmt.Sprintf("第 %s 行：%s", lineNo, detail), true
}

// --- Ontology.Validate / validateAliases errors ------------------------------
//
// These carry no line number — they are structural checks over the parsed
// ontology, not the text — so the translation is the whole message. Each
// entry here mirrors one fmt.Errorf in ontology.go/aliases.go; the ones that
// already read in Chinese (the value-range checks in ontology.go) are left
// alone because they need no translation.

type validateRule struct {
	pattern *regexp.Regexp
	build   func(m []string) string
}

var validateRules = []validateRule{
	{regexp.MustCompile(`^product_line is required$`), func(m []string) string {
		return "product_line 是必填项，请在第一行写 `product_line: 你的产品线名称`"
	}},
	{regexp.MustCompile(`^(.+): no properties declared$`), func(m []string) string {
		return fmt.Sprintf("「%s」没有声明任何属性，properties 至少要写一条，否则本体里没有可注入的事实", m[1])
	}},
	{regexp.MustCompile(`^(.+): denial (\d+) has an empty term$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 denies 缺少 term（要否认的说法），请补上", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): class (\S+) has unknown parent (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」里，类「%s」的父类「%s」没有声明，请先在 classes 里定义它，或检查名字有没有写错", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): class hierarchy has a cycle at (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的分类体系（classes）出现了环状继承：从「%s」沿 subclass_of 往上找父类，最终绕回了自己，请检查这条链条", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): a disjoint group needs at least two classes, got (.+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的 disjoint 分组至少要写两个类，实际是 %s，请补全或删掉这一组", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): disjoint group references unknown class (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的 disjoint 分组引用了未声明的类「%s」", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): "(.+)" is the built-in dimension and cannot be redeclared$`), func(m []string) string {
		return fmt.Sprintf("「%s」不能在 dimensions 里重新声明「%s」，它是系统内置的维度名（保留字）", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): dimension (\S+) lists no values$`), func(m []string) string {
		return fmt.Sprintf("「%s」的维度「%s」没有列出任何取值，values 至少要写一个", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): value "(.+)" appears in both (\S+) and (\S+); scope qualifiers are resolved by value and must be unambiguous$`), func(m []string) string {
		return fmt.Sprintf("「%s」里，取值「%s」同时出现在「%s」和「%s」——一个取值只能属于一个维度或类，否则系统无法判断它指的是哪一个", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): dimension (\S+) labels undeclared value "(.+)"$`), func(m []string) string {
		return fmt.Sprintf("「%s」的维度「%s」给未声明的取值「%s」写了 label，请先把这个取值加到 values 里", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) has unknown domain (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的属性「%s」的 domain（适用的类）「%s」没有声明，请先在 classes 里定义它", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) is an enum with no values$`), func(m []string) string {
		return fmt.Sprintf("「%s」的属性「%s」是枚举类型（enum）但没有列出取值，请在 range.values 里补上可选值", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) is functional but requires (\d+) values$`), func(m []string) string {
		return fmt.Sprintf("「%s」的属性「%s」标了 functional（单值），但 min_cardinality 又要求至少 %s 个值，两者矛盾，请二选一", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) labels undeclared value "(.+)"$`), func(m []string) string {
		return fmt.Sprintf("「%s」的属性「%s」给未声明的取值「%s」写了 label，请先把这个取值加到 range.values 里", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) requires itself$`), func(m []string) string {
		return fmt.Sprintf("「%s」的属性「%s」在 requires 里写了自己，一个属性不能要求自己同时成立", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) requires undeclared property (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的属性「%s」在 requires 里引用了未声明的属性「%s」，请先声明它", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+) references unknown class (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 assertions 引用了未声明的类「%s」", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+) scopes on undeclared dimension (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 assertions 按未声明的维度「%s」限定作用域，请先声明这个维度", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+) uses "(.+)", which is not a value of dimension (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 assertions 用了「%s」，但它不是维度「%s」下的合法取值", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+) sets undeclared property (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 assertions 给未声明的属性「%s」赋值，请先在 properties 里声明它", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): property (\S+) does not apply to (\S*) \(domain is (\S+)\)$`), func(m []string) string {
		class := m[3]
		if class == "" {
			class = "（未指定 class 的默认作用域）"
		}
		return fmt.Sprintf("「%s」的属性「%s」不适用于「%s」（它的 domain 是「%s」），请检查 assertions 里的 class 是否写对", m[1], m[2], class, m[4])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+) sets functional property (\S+) to (\d+) values$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 assertions 给单值属性（functional）「%s」赋了 %s 个值，functional 属性只能有一个值", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+) sets (\S+) to (\d+) values, minimum is (\d+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的第 %s 条 assertions 给属性「%s」赋了 %s 个值，但至少需要 %s 个（min_cardinality）", m[1], m[2], m[3], m[4], m[5])
	}},
	{regexp.MustCompile(`^(.+): assertion (\d+): (.+)$`), func(m []string) string {
		// The inner error is CheckValue's, and already reads in Chinese.
		return fmt.Sprintf("「%s」的第 %s 条 assertions：%s", m[1], m[2], m[3])
	}},
	// --- aliases.go -----------------------------------------------------
	{regexp.MustCompile(`^(.+): alias target (\S+) lists no phrases$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」没有列出任何说法，请在它下面至少写一条", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): alias "(.*)" for (\S+) is shorter than (\d+) characters$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名「%s」（%s）太短了，至少要 %s 个字符，太短的别名容易在无关句子里误命中", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): alias "(.*)" is listed twice under (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名「%s」在「%s」下重复写了两次，删掉一个就行", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): alias "(.*)" points at both (\S+) and (\S+); a customer phrase must mean one thing$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名「%s」同时指向「%s」和「%s」——一个客户说法只能对应一个含义，请只保留其中一个", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): alias "(.*)" for (\S+) is already the canonical wording of (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名「%s」本来就是「%s」的正式名称——它已经是「%s」自己的写法了，不能再当别名用", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): the alias table renders to (\d+) characters, over the (\d+)-character budget; it is injected on every turn and would crowd out the facts themselves$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名表渲染后有 %s 个字符，超过了 %s 字符的上限——它会在每次回答时注入，写太多会挤占本体事实本身的篇幅，请精简一些别名", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): alias target (\S+): property (\S+) is not declared$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」引用了未声明的属性「%s」，请检查拼写", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): alias target (\S+): unknown kind$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」的种类不认识，只能是 property、value、class 或 deny 之一", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): alias target (\S+): "(.*)" is not a declared value of (\S+)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」引用了「%s」，但这不是「%s」的一个已声明取值", m[1], m[2], m[3], m[4])
	}},
	{regexp.MustCompile(`^(.+): alias target (\S+): class (\S+) is not declared$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」引用了未声明的类「%s」", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): alias target (\S+): no denial has the term "(.*)"$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」引用了一个不存在的否认项「%s」，denies 里没有这条 term", m[1], m[2], m[3])
	}},
	{regexp.MustCompile(`^(.+): alias target "(.*)" must be kind:name$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」格式不对，应该写成「种类:名称」，例如 property:退货窗口", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): alias target "(.*)" must be value:<property>\.<value>$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」格式不对，指向取值时要写成 value:属性名.取值，例如 value:保修期.12", m[1], m[2])
	}},
	{regexp.MustCompile(`^(.+): alias target "(.*)": unknown kind "(.*)" \(property, value, class or deny\)$`), func(m []string) string {
		return fmt.Sprintf("「%s」的别名目标「%s」的种类「%s」不认识，只能是 property、value、class 或 deny 之一", m[1], m[2], m[3])
	}},
}

func translateValidateError(msg string) (string, bool) {
	for _, rule := range validateRules {
		if m := rule.pattern.FindStringSubmatch(msg); m != nil {
			return rule.build(m), true
		}
	}
	return "", false
}
