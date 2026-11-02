package render

// AnswerMarkdown preserves the answer mirror's title, body, and appended newline.
func AnswerMarkdown(title, body string) string {
	return "# " + title + "\n\n" + body + "\n"
}
