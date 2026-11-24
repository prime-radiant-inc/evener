package appwire

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const fileMentionDPMaxLen = 512

// NormalizeFileSearchQuery strips leading "./" prefixes from a file-mention query.
func NormalizeFileSearchQuery(query string) string {
	for strings.HasPrefix(query, "./") {
		query = query[2:]
	}
	return query
}

// FileSearchQueryIncludesDotfiles reports whether normalized (or raw) query
// explicitly targets a dot-prefixed segment via leading "." or "/.".
func FileSearchQueryIncludesDotfiles(normalized string) bool {
	q := NormalizeFileSearchQuery(normalized)
	return strings.HasPrefix(q, ".") || strings.Contains(q, "/.")
}

// ScoreFileMention scores candidate path against query using path-aware fuzzy
// alignment, returning the score, matched character indices, and whether all
// query characters matched in order.
func ScoreFileMention(path, query string) (score int, positions []int, ok bool) {
	norm := NormalizeFileSearchQuery(query)
	if norm == "" {
		return 0, nil, true
	}
	if prefix, _, frag, hasSlash := splitDirPrefixQuery(norm); hasSlash {
		if strings.HasPrefix(path, prefix) {
			prefixRuneLen := utf8.RuneCountInString(prefix)
			prefixPos := make([]int, prefixRuneLen)
			for k := range prefixRuneLen {
				prefixPos[k] = k
			}
			if frag == "" {
				return 0, prefixPos, true
			}
			relScore, relPos, matched := scoreNormalizedFileMention(path[len(prefix):], frag)
			if !matched {
				return 0, nil, false
			}
			positions = make([]int, 0, prefixRuneLen+len(relPos))
			positions = append(positions, prefixPos...)
			for _, pos := range relPos {
				positions = append(positions, prefixRuneLen+pos)
			}
			return relScore, positions, true
		}
		if frag == "" {
			return 0, nil, false
		}
	}
	return scoreNormalizedFileMention(path, norm)
}

func scoreNormalizedFileMention(path, norm string) (score int, positions []int, ok bool) {
	if norm == "" {
		return 0, nil, true
	}
	if !isSubsequenceFold(path, norm) {
		return 0, nil, false
	}
	p := []rune(path)
	q := []rune(norm)

	pFold := make([]rune, len(p))
	slashCount := 0
	basenameStart := 0
	for i, r := range p {
		pFold[i] = foldFileRune(r)
		if r == '/' {
			slashCount++
			basenameStart = i + 1
		}
	}

	qFold := make([]rune, len(q))
	for i, r := range q {
		qFold[i] = foldFileRune(r)
	}

	posBonus := make([]int, len(p))
	for j := range p {
		b := 1
		if j == 0 || p[j-1] == '/' {
			b += 24
		} else if p[j-1] == '_' || p[j-1] == '-' || p[j-1] == '.' || p[j-1] == ' ' || (isLowerFileRune(p[j-1]) && isUpperFileRune(p[j])) {
			b += 14
		}
		if j >= basenameStart {
			b += 12
		}
		posBonus[j] = b
	}

	var alignScore int
	if len(p) > fileMentionDPMaxLen {
		positions = make([]int, 0, len(q))
		qi := 0
		for j := 0; j < len(p) && qi < len(q); j++ {
			if pFold[j] == qFold[qi] {
				positions = append(positions, j)
				qi++
			}
		}
		if len(positions) != len(q) {
			return 0, nil, false
		}
		alignScore = scoreFileAlignmentPositions(p, q, posBonus, positions)
	} else {
		positions, alignScore, ok = alignFileMentionDP(p, pFold, q, qFold, posBonus)
		if !ok {
			return 0, nil, false
		}
	}

	baseFold := pFold[basenameStart:]
	exactBase := slices.Equal(baseFold, qFold)
	basePrefix := len(baseFold) >= len(qFold) && slices.Equal(baseFold[:len(qFold)], qFold)
	fullPrefix := len(pFold) >= len(qFold) && slices.Equal(pFold[:len(qFold)], qFold)
	hasQuerySlash := slices.Contains(q, '/')
	allInBase := !hasQuerySlash && len(positions) > 0 && positions[0] >= basenameStart

	score = alignScore
	if exactBase {
		score += 200
	}
	if basePrefix {
		score += 100
	}
	if fullPrefix {
		score += 80
	}
	if allInBase {
		score += 60
	}
	score -= 2 * slashCount
	score -= len(p) / 8
	return score, positions, true
}

func splitDirPrefixQuery(norm string) (prefix, dirNoSlash, frag string, hasSlash bool) {
	lastSlash := strings.LastIndexByte(norm, '/')
	if lastSlash < 0 {
		return "", "", "", false
	}
	return norm[:lastSlash+1], norm[:lastSlash], norm[lastSlash+1:], true
}

func hasExactDirPrefixScope(items []FileSearchItem, norm string) (prefix, frag string, scoped bool) {
	prefix, dirNoSlash, frag, hasSlash := splitDirPrefixQuery(norm)
	if !hasSlash {
		return "", "", false
	}
	if frag == "" {
		return prefix, "", true
	}
	for _, item := range items {
		if strings.HasPrefix(item.Path, prefix) || (dirNoSlash != "" && item.Kind == "dir" && item.Path == dirNoSlash) {
			return prefix, frag, true
		}
	}
	return "", "", false
}

func rankEmptyFileSearchItems(items []FileSearchItem, limit int) []FileSearchItem {
	if len(items) == 0 {
		return []FileSearchItem{}
	}
	type emptyRankedItem struct {
		item     FileSearchItem
		segments int
	}
	decorated := make([]emptyRankedItem, len(items))
	for i, item := range items {
		decorated[i] = emptyRankedItem{
			item:     item,
			segments: strings.Count(item.Path, "/"),
		}
	}
	slices.SortFunc(decorated, func(a, b emptyRankedItem) int {
		if c := cmp.Compare(a.segments, b.segments); c != 0 {
			return c
		}
		if c := cmp.Compare(fileKindRank(a.item.Kind), fileKindRank(b.item.Kind)); c != 0 {
			return c
		}
		return strings.Compare(a.item.Path, b.item.Path)
	})
	if limit > 0 && len(decorated) > limit {
		decorated = decorated[:limit]
	}
	out := make([]FileSearchItem, len(decorated))
	for i, d := range decorated {
		out[i] = d.item
	}
	return out
}

// RankFileSearchItems filters, scores, and sorts items against query, returning
// up to limit items (all matches when limit <= 0) as a non-nil slice.
func RankFileSearchItems(items []FileSearchItem, query string, limit int) []FileSearchItem {
	norm := NormalizeFileSearchQuery(query)
	if norm == "" {
		return rankEmptyFileSearchItems(items, limit)
	}
	prefix, frag, scoped := hasExactDirPrefixScope(items, norm)
	if scoped && frag == "" {
		var underDir []FileSearchItem
		for _, item := range items {
			if strings.HasPrefix(item.Path, prefix) {
				underDir = append(underDir, item)
			}
		}
		return rankEmptyFileSearchItems(underDir, limit)
	}

	type scoredItem struct {
		item     FileSearchItem
		score    int
		segments int
		length   int
	}
	var scored []scoredItem
	for _, item := range items {
		if scoped && !strings.HasPrefix(item.Path, prefix) {
			continue
		}
		score, _, ok := ScoreFileMention(item.Path, norm)
		if !ok {
			continue
		}
		scored = append(scored, scoredItem{
			item:     item,
			score:    score,
			segments: strings.Count(item.Path, "/"),
			length:   utf8.RuneCountInString(item.Path),
		})
	}
	if len(scored) == 0 {
		return []FileSearchItem{}
	}
	slices.SortFunc(scored, func(a, b scoredItem) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		if c := cmp.Compare(fileKindRank(a.item.Kind), fileKindRank(b.item.Kind)); c != 0 {
			return c
		}
		if c := cmp.Compare(a.segments, b.segments); c != 0 {
			return c
		}
		if c := cmp.Compare(a.length, b.length); c != 0 {
			return c
		}
		return strings.Compare(a.item.Path, b.item.Path)
	})
	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]FileSearchItem, len(scored))
	for i, s := range scored {
		out[i] = s.item
	}
	return out
}

func fileKindRank(kind string) int {
	if kind == "file" {
		return 0
	}
	return 1
}

func alignFileMentionDP(p, pFold, q, qFold []rune, posBonus []int) ([]int, int, bool) {
	const negInf = -1_000_000_000
	m := len(q)
	n := len(p)

	prevScore := make([]int, n)
	currScore := make([]int, n)
	back := make([]int16, m*n)

	for j := range n {
		if pFold[j] == qFold[0] {
			s := posBonus[j]
			if p[j] == q[0] {
				s += 2
			}
			prevScore[j] = s
		} else {
			prevScore[j] = negInf
		}
	}

	for i := 1; i < m; i++ {
		bestGapScore := negInf
		bestGapIdx := -1
		for j := range n {
			currScore[j] = negInf
			back[i*n+j] = -1
			if j >= 2 {
				prevIdx := j - 2
				if prevScore[prevIdx] > negInf/2 {
					cand := prevScore[prevIdx] + prevIdx
					if cand > bestGapScore {
						bestGapScore = cand
						bestGapIdx = prevIdx
					}
				}
			}
			if j < i || pFold[j] != qFold[i] {
				continue
			}
			bestPrevScore := negInf
			bestPrev := -1
			if bestGapIdx >= 0 {
				bestPrevScore = bestGapScore - j - 1
				bestPrev = bestGapIdx
			}
			if prevScore[j-1] > negInf/2 {
				consecScore := prevScore[j-1] + 16
				if consecScore > bestPrevScore {
					bestPrevScore = consecScore
					bestPrev = j - 1
				}
			}
			if bestPrev < 0 {
				continue
			}
			s := bestPrevScore + posBonus[j]
			if p[j] == q[i] {
				s += 2
			}
			currScore[j] = s
			back[i*n+j] = int16(bestPrev)
		}
		prevScore, currScore = currScore, prevScore
	}

	bestAlignScore := negInf
	endJ := -1
	for j := m - 1; j < n; j++ {
		if prevScore[j] > bestAlignScore {
			bestAlignScore = prevScore[j]
			endJ = j
		}
	}
	if endJ < 0 {
		return nil, 0, false
	}

	positions := make([]int, m)
	cur := endJ
	for i := m - 1; i >= 0; i-- {
		positions[i] = cur
		if i > 0 {
			cur = int(back[i*n+cur])
		}
	}
	return positions, bestAlignScore, true
}

func scoreFileAlignmentPositions(p, q []rune, posBonus, positions []int) int {
	score := 0
	for i, j := range positions {
		score += posBonus[j]
		if p[j] == q[i] {
			score += 2
		}
		if i > 0 {
			prev := positions[i-1]
			if j == prev+1 {
				score += 16
			} else {
				skipped := j - prev - 1
				score -= 3 + (skipped - 1)
			}
		}
	}
	return score
}

func isSubsequenceFold(path, query string) bool {
	if query == "" {
		return true
	}
	qi := 0
	qr, qsize := utf8.DecodeRuneInString(query)
	qFold := foldFileRune(qr)
	for _, pr := range path {
		if foldFileRune(pr) == qFold {
			qi += qsize
			if qi >= len(query) {
				return true
			}
			qr, qsize = utf8.DecodeRuneInString(query[qi:])
			qFold = foldFileRune(qr)
		}
	}
	return false
}

func foldFileRune(r rune) rune {
	if r <= unicode.MaxASCII {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}
	return unicode.ToLower(r)
}

func isLowerFileRune(r rune) bool {
	if r <= unicode.MaxASCII {
		return r >= 'a' && r <= 'z'
	}
	return unicode.IsLower(r)
}

func isUpperFileRune(r rune) bool {
	if r <= unicode.MaxASCII {
		return r >= 'A' && r <= 'Z'
	}
	return unicode.IsUpper(r)
}
