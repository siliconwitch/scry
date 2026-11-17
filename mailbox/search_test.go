package mailbox

import (
	"slices"
	"testing"
	"time"
)

func TestSearch(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	messages := []Message{
		{ID: "ticket", Subject: "Your train tickets", From: Address{Name: "Northline Rail", Email: "tickets@example.com"}, Cc: []Address{{Name: "Robin Hale", Email: "robin@example.org"}}, Text: "Booking reference NR8K2QX", Date: now},
		{ID: "walk", Subject: "Saturday walk?", From: Address{Name: "Alex Moreno", Email: "alex@example.com"}, Text: "Meet at the bridge car park", Date: now.Add(-time.Hour)},
		{ID: "leads", Subject: "Verified contacts", From: Address{Name: "Fast Leads", Email: "sales@example.org"}, Text: "Grow your pipeline", Date: now.Add(-3 * time.Hour)},
		{ID: "flight", Subject: "Trip confirmed", From: Address{Name: "Skylark Air", Email: "noreply@example.com"}, To: []Address{{Name: "Alex Moreno", Email: "alex@example.com"}}, Text: "Flight SK 214 seat 17A", Date: now.Add(-2 * time.Hour)},
	}

	cases := []struct {
		query string
		want  []string
	}{
		{query: "", want: nil},
		{query: "tickets", want: []string{"ticket"}},
		{query: "tckts", want: []string{"ticket"}},
		{query: "alex", want: []string{"walk", "flight"}},
		{query: "person:alex subject:trip", want: []string{"flight"}},
		{query: "subject:bridge", want: nil},
		{query: "body:bridge", want: []string{"walk"}},
		{query: `body:"car park"`, want: []string{"walk"}},
		{query: "body:brdge", want: []string{"walk"}},
		{query: "body:sat", want: []string{"flight"}},
		{query: "nr8k2qx", want: []string{"ticket"}},
		{query: "alex nothing-matches", want: nil},
		{query: "zz", want: nil},
		{query: "person:alex", want: []string{"walk", "flight"}},
		{query: "person:sales", want: []string{"leads"}},
		{query: "person:robin", want: []string{"ticket"}},
		{query: "person:alx", want: []string{"walk", "flight"}},
	}

	for _, test := range cases {
		var got []string

		for _, message := range Search(messages, test.query) {
			got = append(got, message.ID)
		}

		if !slices.Equal(got, test.want) {
			t.Errorf("Search(%q) = %v, want %v", test.query, got, test.want)
		}
	}
}
