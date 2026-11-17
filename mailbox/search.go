package mailbox

import (
	"slices"
	"strings"
	"unicode/utf8"
)

type searchTerm struct {
	field string
	value string
}

func Search(messages []Message, query string) []Message {
	var words []string
	var current strings.Builder

	quoted := false

	for _, character := range query {
		switch {
		case character == '"':
			quoted = !quoted

		case character == ' ' && !quoted:
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}

		default:
			current.WriteRune(character)
		}
	}

	if current.Len() > 0 {
		words = append(words, current.String())
	}

	var terms []searchTerm

	for _, word := range words {
		field, value, found := strings.Cut(word, ":")

		switch {
		case found && (field == "subject" || field == "body" || field == "person") && value != "":
			terms = append(terms, searchTerm{field: field, value: strings.ToLower(value)})

		default:
			terms = append(terms, searchTerm{value: strings.ToLower(word)})
		}
	}

	if len(terms) == 0 {
		return nil
	}

	type scored struct {
		message Message
		score   int
	}

	var results []scored

	for _, message := range messages {
		total := 0

		for _, term := range terms {
			subjectScore := matchScore(message.Subject, term.value)

			personScore := matchScore(message.From.Name, term.value)
			personScore = max(personScore, matchScore(message.From.Email, term.value))

			for _, recipient := range slices.Concat(message.To, message.Cc, message.Bcc) {
				personScore = max(personScore, matchScore(recipient.Name, term.value))
				personScore = max(personScore, matchScore(recipient.Email, term.value))
			}

			bodyScore := matchScore(message.Text, term.value) / 2

			var termScore int

			switch term.field {
			case "subject":
				termScore = subjectScore

			case "person":
				termScore = personScore

			case "body":
				termScore = bodyScore

			default:
				termScore = max(subjectScore, personScore, bodyScore)
			}

			if termScore == 0 {
				total = 0
				break
			}

			total += termScore
		}

		if total > 0 {
			results = append(results, scored{message: message, score: total})
		}
	}

	slices.SortStableFunc(results, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}

		return b.message.Date.Compare(a.message.Date)
	})

	found := make([]Message, len(results))
	for i, result := range results {
		found[i] = result.message
	}

	return found
}

func matchScore(text, value string) int {
	text = strings.ToLower(text)

	index := strings.Index(text, value)

	wordStart := index == 0
	if index > 0 {
		previous := text[index-1]
		wordStart = !(previous >= 'a' && previous <= 'z') && !(previous >= '0' && previous <= '9')
	}

	switch {
	case wordStart:
		return 120

	case index > 0:
		return 100

	case len([]rune(value)) < 3:
		return 0
	}

	valueRunes := []rune(value)
	fewestGaps := 3

	for start, first := range text {
		if first != valueRunes[0] {
			continue
		}

		_, width := utf8.DecodeRuneInString(text[start:])
		matched := 1
		consumed := 1

		for _, character := range text[start+width:] {
			if matched == len(valueRunes) || consumed == len(valueRunes)+2 {
				break
			}

			consumed++

			if character == valueRunes[matched] {
				matched++
			}
		}

		if matched == len(valueRunes) {
			fewestGaps = min(fewestGaps, consumed-len(valueRunes))
		}
	}

	if fewestGaps > 2 {
		return 0
	}

	return 60 - fewestGaps*10
}
