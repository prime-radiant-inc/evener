package msgrender

import "strings"

type shellCommandLine struct {
	text   string
	indent int
}

var shellCommandOperators = []string{";;&", "&&", "||", "|&", ";;", ";&", "|", ";"}

func shellOperatorAt(command string, index int) string {
	for _, operator := range shellCommandOperators {
		if operator == "|" && index > 0 && command[index-1] == '>' {
			continue
		}
		if strings.HasPrefix(command[index:], operator) {
			return operator
		}
	}
	return ""
}

func shellSyntheticBoundaryEnd(command string, end int) int {
	next := end
	for next < len(command) && (command[next] == ' ' || command[next] == '\t') {
		next++
	}
	if next == len(command) || command[next] == '\n' {
		return -1
	}
	if command[next] == '\\' && next+1 < len(command) && command[next+1] == '\n' {
		return -1
	}
	return next
}

func shellCommentAt(command string, index, lineStart int) bool {
	if index == lineStart {
		return true
	}
	previous := command[index-1]
	if previous != ' ' && previous != '\t' {
		return false
	}
	backslashes := 0
	for cursor := index - 2; cursor >= 0 && command[cursor] == '\\'; cursor-- {
		backslashes++
	}
	return backslashes%2 == 0
}

func formatShellCommand(command string) []shellCommandLine {
	lines := make([]shellCommandLine, 0, 1)
	lineStart := 0
	continuationIndent := 0
	var quote byte
	escaped := false
	comment := false
	depth := make([]byte, 0)

	appendLine := func(end int, indent int) {
		lines = append(lines, shellCommandLine{text: command[lineStart:end], indent: indent})
		lineStart = end + 1
	}

	for index := 0; index < len(command); {
		character := command[index]

		if character == '\n' {
			appendLine(index, continuationIndent)
			continuationIndent = 0
			comment = false
			escaped = false
			index++
			continue
		}

		if comment {
			index++
			continue
		}

		if quote != 0 {
			if quote == '\'' {
				if character == quote {
					quote = 0
				}
				index++
				continue
			}
			if escaped {
				escaped = false
				index++
				continue
			}
			if character == '\\' {
				escaped = true
				index++
				continue
			}
			if character == quote {
				quote = 0
			}
			index++
			continue
		}

		if escaped {
			escaped = false
			index++
			continue
		}

		if character == '\\' {
			escaped = true
			index++
			continue
		}

		if character == '\'' || character == '"' || character == '`' {
			quote = character
			index++
			continue
		}

		if character == '#' && shellCommentAt(command, index, lineStart) {
			comment = true
			index++
			continue
		}

		if character == '(' || character == '{' {
			depth = append(depth, character)
			index++
			continue
		}

		if len(depth) > 0 {
			opener := depth[len(depth)-1]
			if (character == ')' && opener == '(') || (character == '}' && opener == '{') {
				depth = depth[:len(depth)-1]
				index++
				continue
			}
		}

		if len(depth) == 0 {
			operator := shellOperatorAt(command, index)
			if operator != "" {
				end := index + len(operator)
				boundary := shellSyntheticBoundaryEnd(command, end)
				if boundary >= 0 {
					lines = append(lines, shellCommandLine{text: command[lineStart:boundary], indent: continuationIndent})
					lineStart = boundary
					continuationIndent = 2
					index = boundary
					continue
				}
				index = end
				continue
			}
		}

		index++
	}

	lines = append(lines, shellCommandLine{text: command[lineStart:], indent: continuationIndent})
	return lines
}
