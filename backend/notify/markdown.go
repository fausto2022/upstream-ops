package notify

import (
	"fmt"
	"strings"
)

type MarkdownField struct {
	Label string
	Value any
}

func Detail(label string, value any) MarkdownField {
	return MarkdownField{Label: label, Value: value}
}

func MarkdownDetails(summary string, fields ...MarkdownField) string {
	var b strings.Builder
	if text := strings.TrimSpace(summary); text != "" {
		b.WriteString("> ")
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	for _, field := range fields {
		fmt.Fprintf(&b, "- *%s：* %s\n", field.Label, MarkdownCode(field.Value))
	}
	return strings.TrimSpace(b.String())
}

func MarkdownSection(title string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n#### ")
	b.WriteString(title)
	b.WriteString("\n")
	for _, item := range items {
		text := strings.TrimSpace(item)
		if text == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func MarkdownNote(label, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return fmt.Sprintf("\n\n> *%s：* %s", label, text)
}

func MarkdownCode(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	text = strings.NewReplacer(
		"\r\n", " ",
		"\r", " ",
		"\n", " ",
		"`", "'",
	).Replace(text)
	if text == "" {
		text = "—"
	}
	return "`" + text + "`"
}
