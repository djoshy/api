package scanner

import (
	"errors"
	"io"
	"strings"

	"github.com/goccy/go-yaml/token"
)

// IndentState state for indent
type IndentState int

const (
	// IndentStateEqual equals previous indent
	IndentStateEqual IndentState = iota
	// IndentStateUp more indent than previous
	IndentStateUp
	// IndentStateDown less indent than previous
	IndentStateDown
	// IndentStateKeep uses not indent token
	IndentStateKeep
)

// Scanner holds the scanner's internal state while processing a given text.
// It can be allocated as part of another data structure but must be initialized via Init before use.
type Scanner struct {
	source     []rune
	sourcePos  int
	sourceSize int
	// line number. This number starts from 1.
	line int
	// column number. This number starts from 1.
	column int
	// offset represents the offset from the beginning of the source.
	offset int
	// lastDelimColumn is the last column needed to compare indent is retained.
	lastDelimColumn int
	// indentNum indicates the number of spaces used for indentation.
	indentNum int
	// prevLineIndentNum indicates the number of spaces used for indentation at previous line.
	prevLineIndentNum int
	// indentLevel indicates the level of indent depth. This value does not match the column value.
	indentLevel            int
	docStartColumn         int
	isFirstCharAtLine      bool
	isAnchor               bool
	startedFlowSequenceNum int
	startedFlowMapNum      int
	indentState            IndentState
	savedPos               *token.Position
}

func (s *Scanner) pos() *token.Position {
	return &token.Position{
		Line:        s.line,
		Column:      s.column,
		Offset:      s.offset,
		IndentNum:   s.indentNum,
		IndentLevel: s.indentLevel,
	}
}

func (s *Scanner) bufferedToken(ctx *Context) *token.Token {
	if s.savedPos != nil {
		tk := ctx.bufferedToken(s.savedPos)
		s.savedPos = nil
		return tk
	}
	line := s.line
	column := s.column - len(ctx.buf)
	level := s.indentLevel
	if ctx.isSaveIndentMode() {
		line -= s.newLineCount(ctx.buf)
		column = strings.Index(string(ctx.obuf), string(ctx.buf)) + 1
		// Since we are in a literal, folded or raw folded
		// we can use the indent level from the last token.
		last := ctx.lastToken()
		if last != nil { // The last token should never be nil here.
			level = last.Position.IndentLevel + 1
		}
	}
	return ctx.bufferedToken(&token.Position{
		Line:        line,
		Column:      column,
		Offset:      s.offset - len(ctx.buf),
		IndentNum:   s.indentNum,
		IndentLevel: level,
	})
}

func (s *Scanner) progressColumn(ctx *Context, num int) {
	s.column += num
	s.offset += num
	ctx.progress(num)
}

func (s *Scanner) progressLine(ctx *Context) {
	s.prevLineIndentNum = s.indentNum
	s.column = 1
	s.line++
	s.offset++
	s.indentNum = 0
	s.isFirstCharAtLine = true
	s.isAnchor = false
	ctx.progress(1)
}

func (s *Scanner) isNewLineChar(c rune) bool {
	if c == '\n' {
		return true
	}
	if c == '\r' {
		return true
	}
	return false
}

func (s *Scanner) newLineCount(src []rune) int {
	size := len(src)
	cnt := 0
	for i := 0; i < size; i++ {
		c := src[i]
		switch c {
		case '\r':
			if i+1 < size && src[i+1] == '\n' {
				i++
			}
			cnt++
		case '\n':
			cnt++
		}
	}
	return cnt
}

func (s *Scanner) updateIndentLevel() {
	if s.prevLineIndentNum < s.indentNum {
		s.indentLevel++
	} else if s.prevLineIndentNum > s.indentNum {
		if s.indentLevel > 0 {
			s.indentLevel--
		}
	}
}

func (s *Scanner) updateIndentState(ctx *Context) {
	if s.lastDelimColumn > 0 {
		if s.lastDelimColumn < s.column {
			s.indentState = IndentStateUp
		} else {
			// If lastDelimColumn and s.column are the same,
			// treat as Down state since it is the same column as delimiter.
			s.indentState = IndentStateDown
		}
	} else {
		s.indentState = s.indentStateFromIndentNumDifference()
	}
}

func (s *Scanner) indentStateFromIndentNumDifference() IndentState {
	switch {
	case s.prevLineIndentNum < s.indentNum:
		return IndentStateUp
	case s.prevLineIndentNum == s.indentNum:
		return IndentStateEqual
	default:
		return IndentStateDown
	}
}

func (s *Scanner) updateIndent(ctx *Context, c rune) {
	if s.isFirstCharAtLine && s.isNewLineChar(c) && ctx.isDocument() {
		return
	}
	if s.isFirstCharAtLine && c == ' ' {
		s.indentNum++
		return
	}
	if !s.isFirstCharAtLine {
		s.indentState = IndentStateKeep
		return
	}
	s.updateIndentLevel()
	s.updateIndentState(ctx)
	s.isFirstCharAtLine = false
}

func (s *Scanner) isChangedToIndentStateDown() bool {
	return s.indentState == IndentStateDown
}

func (s *Scanner) isChangedToIndentStateUp() bool {
	return s.indentState == IndentStateUp
}

func (s *Scanner) addBufferedTokenIfExists(ctx *Context) {
	ctx.addToken(s.bufferedToken(ctx))
}

func (s *Scanner) breakLiteral(ctx *Context) {
	s.docStartColumn = 0
	ctx.breakLiteral()
}

func (s *Scanner) scanSingleQuote(ctx *Context) (tk *token.Token, pos int) {
	ctx.addOriginBuf('\'')
	srcpos := s.pos()
	startIndex := ctx.idx + 1
	src := ctx.src
	size := len(src)
	value := []rune{}
	isFirstLineChar := false
	isNewLine := false
	for idx := startIndex; idx < size; idx++ {
		if !isNewLine {
			s.progressColumn(ctx, 1)
		} else {
			isNewLine = false
		}
		c := src[idx]
		pos = idx + 1
		ctx.addOriginBuf(c)
		if s.isNewLineChar(c) {
			value = append(value, ' ')
			isFirstLineChar = true
			isNewLine = true
			s.progressLine(ctx)
			continue
		} else if c == ' ' && isFirstLineChar {
			continue
		} else if c != '\'' {
			value = append(value, c)
			isFirstLineChar = false
			continue
		}
		if idx+1 < len(ctx.src) && ctx.src[idx+1] == '\'' {
			// '' handle as ' character
			value = append(value, c)
			ctx.addOriginBuf(c)
			idx++
			continue
		}
		s.progressColumn(ctx, 1)
		tk = token.SingleQuote(string(value), string(ctx.obuf), srcpos)
		pos = idx - startIndex + 1
		return
	}
	return
}

func hexToInt(b rune) int {
	if b >= 'A' && b <= 'F' {
		return int(b) - 'A' + 10
	}
	if b >= 'a' && b <= 'f' {
		return int(b) - 'a' + 10
	}
	return int(b) - '0'
}

func hexRunesToInt(b []rune) int {
	sum := 0
	for i := 0; i < len(b); i++ {
		sum += hexToInt(b[i]) << (uint(len(b)-i-1) * 4)
	}
	return sum
}

func (s *Scanner) scanDoubleQuote(ctx *Context) (tk *token.Token, pos int) {
	ctx.addOriginBuf('"')
	srcpos := s.pos()
	startIndex := ctx.idx + 1
	src := ctx.src
	size := len(src)
	value := []rune{}
	isFirstLineChar := false
	isNewLine := false
	for idx := startIndex; idx < size; idx++ {
		if !isNewLine {
			s.progressColumn(ctx, 1)
		} else {
			isNewLine = false
		}
		c := src[idx]
		pos = idx + 1
		ctx.addOriginBuf(c)
		if s.isNewLineChar(c) {
			value = append(value, ' ')
			isFirstLineChar = true
			isNewLine = true
			s.progressLine(ctx)
			continue
		} else if c == ' ' && isFirstLineChar {
			continue
		} else if c == '\\' {
			isFirstLineChar = false
			if idx+1 >= size {
				value = append(value, c)
				continue
			}
			nextChar := src[idx+1]
			progress := 0
			switch nextChar {
			case 'b':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, '\b')
			case 'e':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, '\x1B')
			case 'f':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, '\f')
			case 'n':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, '\n')
			case 'r':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, '\r')
			case 'v':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, '\v')
			case 'L': // LS (#x2028)
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, []rune{'\xE2', '\x80', '\xA8'}...)
			case 'N': // NEL (#x85)
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, []rune{'\xC2', '\x85'}...)
			case 'P': // PS (#x2029)
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, []rune{'\xE2', '\x80', '\xA9'}...)
			case '_': // #xA0
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, []rune{'\xC2', '\xA0'}...)
			case '"':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, nextChar)
			case 'x':
				progress = 3
				if idx+progress >= size {
					// TODO: need to return error
					//err = errors.New("invalid escape character \\x")
					return
				}
				codeNum := hexRunesToInt(src[idx+2 : idx+progress+1])
				value = append(value, rune(codeNum))
			case 'u':
				progress = 5
				if idx+progress >= size {
					// TODO: need to return error
					//err = errors.New("invalid escape character \\u")
					return
				}
				codeNum := hexRunesToInt(src[idx+2 : idx+progress+1])
				value = append(value, rune(codeNum))
			case 'U':
				progress = 9
				if idx+progress >= size {
					// TODO: need to return error
					//err = errors.New("invalid escape character \\U")
					return
				}
				codeNum := hexRunesToInt(src[idx+2 : idx+progress+1])
				value = append(value, rune(codeNum))
			case '\\':
				progress = 1
				ctx.addOriginBuf(nextChar)
				value = append(value, c)
			default:
				value = append(value, c)
			}
			idx += progress
			s.progressColumn(ctx, progress)
			continue
		} else if c != '"' {
			value = append(value, c)
			isFirstLineChar = false
			continue
		}
		s.progressColumn(ctx, 1)
		tk = token.DoubleQuote(string(value), string(ctx.obuf), srcpos)
		pos = idx - startIndex + 1
		return
	}
	return
}

func (s *Scanner) scanQuote(ctx *Context, ch rune) (tk *token.Token, pos int) {
	if ch == '\'' {
		return s.scanSingleQuote(ctx)
	}
	return s.scanDoubleQuote(ctx)
}

func (s *Scanner) isMergeKey(ctx *Context) bool {
	if ctx.repeatNum('<') != 2 {
		return false
	}
	src := ctx.src
	size := len(src)
	for idx := ctx.idx + 2; idx < size; idx++ {
		c := src[idx]
		if c == ' ' {
			continue
		}
		if c != ':' {
			return false
		}
		if idx+1 < size {
			nc := src[idx+1]
			if nc == ' ' || s.isNewLineChar(nc) {
				return true
			}
		}
	}
	return false
}

func (s *Scanner) scanTag(ctx *Context) (tk *token.Token, pos int) {
	ctx.addOriginBuf('!')
	ctx.progress(1) // skip '!' character
	for idx, c := range ctx.src[ctx.idx:] {
		pos = idx + 1
		ctx.addOriginBuf(c)
		switch c {
		case ' ', '\n', '\r':
			value := ctx.source(ctx.idx-1, ctx.idx+idx)
			tk = token.Tag(value, string(ctx.obuf), s.pos())
			pos = len([]rune(value))
			return
		}
	}
	return
}

func (s *Scanner) scanComment(ctx *Context) (tk *token.Token, pos int) {
	ctx.addOriginBuf('#')
	ctx.progress(1) // skip '#' character
	for idx, c := range ctx.src[ctx.idx:] {
		ctx.addOriginBuf(c)
		switch c {
		case '\n', '\r':
			if ctx.previousChar() == '\\' {
				continue
			}
			value := ctx.source(ctx.idx, ctx.idx+idx)
			tk = token.Comment(value, string(ctx.obuf), s.pos())
			pos = len([]rune(value)) + 1
			return
		}
	}
	// document ends with comment.
	value := string(ctx.src[ctx.idx:])
	tk = token.Comment(value, string(ctx.obuf), s.pos())
	pos = len([]rune(value)) + 1
	return
}

func trimCommentFromLiteralOpt(text string) (string, error) {
	idx := strings.Index(text, "#")
	if idx < 0 {
		return text, nil
	}
	if idx == 0 {
		return "", errors.New("invalid literal header")
	}
	return text[:idx-1], nil
}

func (s *Scanner) scanLiteral(ctx *Context, c rune) {
	ctx.addOriginBuf(c)
	if ctx.isEOS() {
		if ctx.isLiteral {
			ctx.addBuf(c)
		}
		value := ctx.bufferedSrc()
		ctx.addToken(token.String(string(value), string(ctx.obuf), s.pos()))
		ctx.resetBuffer()
		s.progressColumn(ctx, 1)
	} else if s.isNewLineChar(c) {
		if ctx.isLiteral {
			ctx.addBuf(c)
		} else {
			ctx.addBuf(' ')
		}
		s.progressLine(ctx)
	} else if s.isFirstCharAtLine && c == ' ' {
		if 0 < s.docStartColumn && s.docStartColumn <= s.column {
			ctx.addBuf(c)
		}
		s.progressColumn(ctx, 1)
	} else {
		if s.docStartColumn == 0 {
			s.docStartColumn = s.column
		}
		ctx.addBuf(c)
		s.progressColumn(ctx, 1)
	}
}

func (s *Scanner) scanNewLine(ctx *Context, c rune) {
	if len(ctx.buf) > 0 && s.savedPos == nil {
		s.savedPos = s.pos()
		s.savedPos.Column -= len(ctx.bufferedSrc())
	}

	// if the following case, origin buffer has unnecessary two spaces.
	// So, `removeRightSpaceFromOriginBuf` remove them, also fix column number too.
	// ---
	// a:[space][space]
	//   b: c
	removedNum := ctx.removeRightSpaceFromBuf()
	if removedNum > 0 {
		s.column -= removedNum
		s.offset -= removedNum
		if s.savedPos != nil {
			s.savedPos.Column -= removedNum
		}
	}

	// There is no problem that we ignore CR which followed by LF and normalize it to LF, because of following YAML1.2 spec.
	// > Line breaks inside scalar content must be normalized by the YAML processor. Each such line break must be parsed into a single line feed character.
	// > Outside scalar content, YAML allows any line break to be used to terminate lines.
	// > -- https://yaml.org/spec/1.2/spec.html
	if c == '\r' && ctx.nextChar() == '\n' {
		ctx.addOriginBuf('\r')
		ctx.progress(1)
		c = '\n'
	}

	if ctx.isEOS() {
		s.addBufferedTokenIfExists(ctx)
	} else if s.isAnchor {
		s.addBufferedTokenIfExists(ctx)
	}
	ctx.addBuf(' ')
	ctx.addOriginBuf(c)
	ctx.isSingleLine = false
	s.progressLine(ctx)
}

func (s *Scanner) scanFlowMapStart(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}

	ctx.addOriginBuf('{')
	ctx.addToken(token.MappingStart(string(ctx.obuf), s.pos()))
	s.startedFlowMapNum++
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanFlowMapEnd(ctx *Context) bool {
	if s.startedFlowMapNum <= 0 {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addOriginBuf('}')
	ctx.addToken(token.MappingEnd(string(ctx.obuf), s.pos()))
	s.startedFlowMapNum--
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanFlowArrayStart(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}

	ctx.addOriginBuf('[')
	ctx.addToken(token.SequenceStart(string(ctx.obuf), s.pos()))
	s.startedFlowSequenceNum++
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanFlowArrayEnd(ctx *Context) bool {
	if s.startedFlowSequenceNum <= 0 {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addOriginBuf(']')
	ctx.addToken(token.SequenceEnd(string(ctx.obuf), s.pos()))
	s.startedFlowSequenceNum--
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanFlowEntry(ctx *Context, c rune) bool {
	if s.startedFlowSequenceNum <= 0 && s.startedFlowMapNum <= 0 {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addOriginBuf(c)
	ctx.addToken(token.CollectEntry(string(ctx.obuf), s.pos()))
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanMapDelim(ctx *Context) bool {
	nc := ctx.nextChar()
	if s.startedFlowMapNum <= 0 && nc != ' ' && !s.isNewLineChar(nc) && !ctx.isNextEOS() {
		return false
	}

	// mapping value
	tk := s.bufferedToken(ctx)
	if tk != nil {
		s.lastDelimColumn = tk.Position.Column
		ctx.addToken(tk)
	} else if tk := ctx.lastToken(); tk != nil {
		// If the map key is quote, the buffer does not exist because it has already been cut into tokens.
		// Therefore, we need to check the last token.
		if tk.Indicator == token.QuotedScalarIndicator {
			s.lastDelimColumn = tk.Position.Column
		}
	}
	ctx.addToken(token.MappingValue(s.pos()))
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanDocumentStart(ctx *Context) bool {
	if s.indentNum != 0 {
		return false
	}
	if s.column != 1 {
		return false
	}
	if ctx.repeatNum('-') != 3 {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addToken(token.DocumentHeader(string(ctx.obuf)+"---", s.pos()))
	s.progressColumn(ctx, 3)
	return true
}

func (s *Scanner) scanDocumentEnd(ctx *Context) bool {
	if s.indentNum != 0 {
		return false
	}
	if s.column != 1 {
		return false
	}
	if ctx.repeatNum('.') != 3 {
		return false
	}

	ctx.addToken(token.DocumentEnd(string(ctx.obuf)+"...", s.pos()))
	s.progressColumn(ctx, 3)
	return true
}

func (s *Scanner) scanMergeKey(ctx *Context) bool {
	if !s.isMergeKey(ctx) {
		return false
	}

	s.lastDelimColumn = s.column
	ctx.addToken(token.MergeKey(string(ctx.obuf)+"<<", s.pos()))
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanRawFoldedChar(ctx *Context) bool {
	if !ctx.existsBuffer() {
		return false
	}
	if !s.isChangedToIndentStateUp() {
		return false
	}

	ctx.isRawFolded = true
	ctx.addBuf('-')
	ctx.addOriginBuf('-')
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanSequence(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}

	nc := ctx.nextChar()
	if nc != ' ' && !s.isNewLineChar(nc) {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addOriginBuf('-')
	tk := token.SequenceEntry(string(ctx.obuf), s.pos())
	s.lastDelimColumn = tk.Position.Column
	ctx.addToken(tk)
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanLiteralHeader(ctx *Context) (bool, error) {
	if ctx.existsBuffer() {
		return false, nil
	}

	progress, err := s.scanLiteralHeaderOption(ctx)
	if err != nil {
		return false, err
	}
	s.progressColumn(ctx, progress)
	s.progressLine(ctx)
	return true, nil
}

func (s *Scanner) scanLiteralHeaderOption(ctx *Context) (pos int, err error) {
	header := ctx.currentChar()
	ctx.addOriginBuf(header)
	ctx.progress(1) // skip '|' or '>' character
	for idx, c := range ctx.src[ctx.idx:] {
		pos = idx
		ctx.addOriginBuf(c)
		switch c {
		case '\n', '\r':
			value := ctx.source(ctx.idx, ctx.idx+idx)
			opt := strings.TrimRight(value, " ")
			orgOptLen := len(opt)
			opt, err = trimCommentFromLiteralOpt(opt)
			if err != nil {
				return
			}
			switch opt {
			case "", "+", "-",
				"0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
				hasComment := len(opt) < orgOptLen
				if header == '|' {
					if hasComment {
						commentLen := orgOptLen - len(opt)
						headerPos := strings.Index(string(ctx.obuf), "|")
						litBuf := ctx.obuf[:len(ctx.obuf)-commentLen-headerPos]
						commentBuf := ctx.obuf[len(litBuf):]
						ctx.addToken(token.Literal("|"+opt, string(litBuf), s.pos()))
						s.column += len(litBuf)
						s.offset += len(litBuf)
						commentHeader := strings.Index(value, "#")
						ctx.addToken(token.Comment(string(value[commentHeader+1:]), string(commentBuf), s.pos()))
					} else {
						ctx.addToken(token.Literal("|"+opt, string(ctx.obuf), s.pos()))
					}
					ctx.isLiteral = true
				} else if header == '>' {
					if hasComment {
						commentLen := orgOptLen - len(opt)
						headerPos := strings.Index(string(ctx.obuf), ">")
						foldedBuf := ctx.obuf[:len(ctx.obuf)-commentLen-headerPos]
						commentBuf := ctx.obuf[len(foldedBuf):]
						ctx.addToken(token.Folded(">"+opt, string(foldedBuf), s.pos()))
						s.column += len(foldedBuf)
						s.offset += len(foldedBuf)
						commentHeader := strings.Index(value, "#")
						ctx.addToken(token.Comment(string(value[commentHeader+1:]), string(commentBuf), s.pos()))
					} else {
						ctx.addToken(token.Folded(">"+opt, string(ctx.obuf), s.pos()))
					}
					ctx.isFolded = true
				}
				s.indentState = IndentStateKeep
				ctx.resetBuffer()
				ctx.literalOpt = opt
				return
			}
		}
	}
	err = errors.New("invalid literal header")
	return
}

func (s *Scanner) scanMapKey(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}

	nc := ctx.nextChar()
	if nc != ' ' {
		return false
	}

	ctx.addToken(token.MappingKey(s.pos()))
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanDirective(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}
	if s.indentNum != 0 {
		return false
	}

	ctx.addToken(token.Directive(string(ctx.obuf)+"%", s.pos()))
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scanAnchor(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addOriginBuf('&')
	ctx.addToken(token.Anchor(string(ctx.obuf), s.pos()))
	s.progressColumn(ctx, 1)
	s.isAnchor = true
	return true
}

func (s *Scanner) scanAlias(ctx *Context) bool {
	if ctx.existsBuffer() {
		return false
	}

	s.addBufferedTokenIfExists(ctx)
	ctx.addOriginBuf('*')
	ctx.addToken(token.Alias(string(ctx.obuf), s.pos()))
	s.progressColumn(ctx, 1)
	return true
}

func (s *Scanner) scan(ctx *Context) (pos int) {
	for ctx.next() {
		pos = ctx.nextPos()
		c := ctx.currentChar()

		// First, change the IndentState.
		// If the target character is the first character in a line, IndentState is Up/Down/Equal state.
		// The second and subsequent letters are Keep.
		s.updateIndent(ctx, c)

		// If IndentState is down, tokens are split, so the buffer accumulated until that point needs to be cutted as a token.
		if s.isChangedToIndentStateDown() {
			s.addBufferedTokenIfExists(ctx)
		}
		if ctx.isDocument() {
			if s.isChangedToIndentStateDown() {
				s.breakLiteral(ctx)
			} else {
				s.scanLiteral(ctx, c)
				continue
			}
		}
		switch c {
		case '{':
			if s.scanFlowMapStart(ctx) {
				return
			}
		case '}':
			if s.scanFlowMapEnd(ctx) {
				return
			}
		case '.':
			if s.scanDocumentEnd(ctx) {
				pos += 2
				return
			}
		case '<':
			if s.scanMergeKey(ctx) {
				pos++
				return
			}
		case '-':
			if s.scanDocumentStart(ctx) {
				pos += 2
				return
			}
			if s.scanRawFoldedChar(ctx) {
				continue
			}
			if s.scanSequence(ctx) {
				return
			}
			if ctx.existsBuffer() {
				// '-' is literal
				ctx.addBuf(c)
				ctx.addOriginBuf(c)
				s.progressColumn(ctx, 1)
				continue
			}
		case '[':
			if s.scanFlowArrayStart(ctx) {
				return
			}
		case ']':
			if s.scanFlowArrayEnd(ctx) {
				return
			}
		case ',':
			if s.scanFlowEntry(ctx, c) {
				return
			}
		case ':':
			if s.scanMapDelim(ctx) {
				return
			}
		case '|', '>':
			scanned, err := s.scanLiteralHeader(ctx)
			if err != nil {
				// TODO: returns syntax error object
				return
			}
			if scanned {
				continue
			}
		case '!':
			if !ctx.existsBuffer() {
				token, progress := s.scanTag(ctx)
				ctx.addToken(token)
				s.progressColumn(ctx, progress)
				if c := ctx.previousChar(); s.isNewLineChar(c) {
					s.progressLine(ctx)
				}
				pos += progress
				return
			}
		case '%':
			if s.scanDirective(ctx) {
				return
			}
		case '?':
			if s.scanMapKey(ctx) {
				return
			}
		case '&':
			if s.scanAnchor(ctx) {
				return
			}
		case '*':
			if s.scanAlias(ctx) {
				return
			}
		case '#':
			if !ctx.existsBuffer() || ctx.previousChar() == ' ' {
				s.addBufferedTokenIfExists(ctx)
				token, progress := s.scanComment(ctx)
				ctx.addToken(token)
				s.progressColumn(ctx, progress)
				s.progressLine(ctx)
				pos += progress
				return
			}
		case '\'', '"':
			if !ctx.existsBuffer() {
				token, progress := s.scanQuote(ctx, c)
				ctx.addToken(token)
				pos += progress
				// If the non-whitespace character immediately following the quote is ':', the quote should be treated as a map key.
				// Therefore, do not return and continue processing as a normal map key.
				if ctx.currentCharWithSkipWhitespace() == ':' {
					continue
				}
				return
			}
		case '\r', '\n':
			s.scanNewLine(ctx, c)
			continue
		case ' ':
			if ctx.isSaveIndentMode() || (!s.isAnchor && !s.isFirstCharAtLine) {
				ctx.addBuf(c)
				ctx.addOriginBuf(c)
				s.progressColumn(ctx, 1)
				continue
			}
			if s.isFirstCharAtLine {
				s.progressColumn(ctx, 1)
				ctx.addOriginBuf(c)
				continue
			}
			s.addBufferedTokenIfExists(ctx)
			pos-- // to rescan white space at next scanning for adding white space to next buffer.
			s.isAnchor = false
			return
		}
		ctx.addBuf(c)
		ctx.addOriginBuf(c)
		s.progressColumn(ctx, 1)
	}
	s.addBufferedTokenIfExists(ctx)
	return
}

// Init prepares the scanner s to tokenize the text src by setting the scanner at the beginning of src.
func (s *Scanner) Init(text string) {
	src := []rune(text)
	s.source = src
	s.sourcePos = 0
	s.sourceSize = len(src)
	s.line = 1
	s.column = 1
	s.offset = 1
	s.prevLineIndentNum = 0
	s.lastDelimColumn = 0
	s.indentLevel = 0
	s.indentNum = 0
	s.isFirstCharAtLine = true
}

// Scan scans the next token and returns the token collection. The source end is indicated by io.EOF.
func (s *Scanner) Scan() (token.Tokens, error) {
	if s.sourcePos >= s.sourceSize {
		return nil, io.EOF
	}
	ctx := newContext(s.source[s.sourcePos:])
	defer ctx.release()
	progress := s.scan(ctx)
	s.sourcePos += progress
	var tokens token.Tokens
	tokens = append(tokens, ctx.tokens...)
	return tokens, nil
}
