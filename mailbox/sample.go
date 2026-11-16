package mailbox

import (
	"fmt"
	"time"
)

const samplePDF = `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj
4 0 obj << /Length 52 >> stream
BT /F1 18 Tf 40 70 Td (Sample ticket from scry) Tj ET
endstream endobj
5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj
trailer << /Root 1 0 R >>
%%EOF
`

const sampleInvite = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//scry//sample//EN
BEGIN:VEVENT
UID:sample-1@example.com
DTSTART:20261204T183000Z
DTEND:20261204T210000Z
SUMMARY:Winter social
LOCATION:The Lantern Room
END:VEVENT
END:VCALENDAR
`

const sampleTicketHTML = `<!doctype html>
<html><body style="margin:0;background:#f2f4f7;font-family:Helvetica,Arial,sans-serif;color:#1d2433">
<table width="100%" cellpadding="0" cellspacing="0" style="background:#f2f4f7;padding:24px 0">
<tr><td align="center">
<table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:8px;overflow:hidden">
<tr><td style="background:#0b5d4b;color:#ffffff;padding:20px 28px;font-size:20px;font-weight:bold">Northline Rail</td></tr>
<tr><td style="padding:28px">
<p style="margin:0 0 6px;font-size:14px;color:#5b6475">Booking reference</p>
<p style="margin:0 0 22px;font-size:26px;font-weight:bold;letter-spacing:2px">NR8K2QX</p>
<table width="100%" cellpadding="0" cellspacing="0" style="font-size:15px">
<tr><td style="padding:10px 0;border-top:1px solid #e3e7ee"><b>Outbound</b><br>Fri 12 Dec, 07:42</td>
<td style="padding:10px 0;border-top:1px solid #e3e7ee" align="right">Easton Central &rarr; Millbrook<br>Coach C, seat 41</td></tr>
<tr><td style="padding:10px 0;border-top:1px solid #e3e7ee"><b>Return</b><br>Sun 14 Dec, 18:15</td>
<td style="padding:10px 0;border-top:1px solid #e3e7ee" align="right">Millbrook &rarr; Easton Central<br>Coach B, seat 12</td></tr>
<tr><td style="padding:10px 0;border-top:1px solid #e3e7ee"><b>Total paid</b></td>
<td style="padding:10px 0;border-top:1px solid #e3e7ee" align="right"><b>&pound;64.20</b></td></tr>
</table>
<p style="margin:22px 0 0;font-size:13px;color:#5b6475">Your e-ticket is attached. Show it on your phone or print it.</p>
</td></tr></table>
</td></tr></table>
</body></html>`

const sampleFlightHTML = `<!doctype html>
<html><body style="margin:0;font-family:Georgia,serif;color:#222">
<div style="max-width:600px;margin:0 auto;padding:24px">
<h1 style="font-size:24px;color:#2a3d8f;margin:0 0 4px">Skylark Air</h1>
<p style="margin:0 0 24px;color:#777">Your trip is confirmed</p>
<div style="border:2px solid #2a3d8f;border-radius:10px;padding:20px">
<div style="display:flex;justify-content:space-between;font-size:32px;font-weight:bold"><span>LHX</span><span style="color:#aaa">&#9992;</span><span>OSV</span></div>
<div style="display:flex;justify-content:space-between;color:#555"><span>Thu 22 Jan, 09:10</span><span>12:35</span></div>
<hr style="border:none;border-top:1px dashed #bbb;margin:16px 0">
<p style="margin:0">Flight <b>SK 214</b> &middot; Seat <b>17A</b> &middot; 1 cabin bag</p>
</div>
<p style="margin-top:24px">Check-in opens 24 hours before departure.</p>
<img src="https://example.com/tracking-pixel.gif" width="1" height="1" alt="">
</div></body></html>`

const sampleNewsletterHTML = `<!doctype html>
<html><body style="margin:0;background:#fffaf0;font-family:-apple-system,Segoe UI,sans-serif">
<div style="max-width:620px;margin:0 auto;padding:32px 24px">
<p style="text-transform:uppercase;letter-spacing:3px;font-size:12px;color:#b05e27;margin:0">Bramble Books</p>
<h1 style="font-size:30px;margin:8px 0 16px;color:#3b2a1a">Long nights, good books</h1>
<img src="https://example.com/winter-banner.jpg" alt="Winter reading banner" style="width:100%;border-radius:6px">
<p style="font-size:16px;line-height:1.6;color:#4a3b2b">Six new arrivals for the darker evenings, plus a members' evening on the 9th with mulled cider and a reading from our staff picks.</p>
<p><a href="https://example.com/new-arrivals" style="display:inline-block;background:#b05e27;color:#fff;padding:12px 20px;border-radius:4px;text-decoration:none">See the new arrivals</a></p>
<p style="font-size:12px;color:#998">You're receiving this because you joined our reading list.</p>
</div></body></html>`

const sampleReceiptHTML = `<!doctype html>
<html><body style="font-family:Arial,sans-serif;color:#333;margin:0;padding:24px">
<h2 style="margin:0 0 16px">Fernwood Hotel</h2>
<p>Thanks for staying with us. Here is your receipt.</p>
<table cellpadding="8" style="border-collapse:collapse;min-width:360px">
<tr style="background:#f4f4f4"><th align="left">Item</th><th align="right">Amount</th></tr>
<tr><td>2 nights, double room</td><td align="right">&pound;238.00</td></tr>
<tr><td>Breakfast</td><td align="right">&pound;24.00</td></tr>
<tr style="border-top:2px solid #333"><td><b>Total</b></td><td align="right"><b>&pound;262.00</b></td></tr>
</table>
</body></html>`

func Sample(now time.Time) *Mailbox {
	day := 24 * time.Hour

	box := &Mailbox{
		accounts: []Account{
			{Name: "Personal", Email: "me@example.com", Folders: []string{Inbox, Sent, Archive, Spam, Trash, "Events", "Reference"}},
			{Name: "Work", Email: "me@example.org", Folders: []string{Inbox, Sent, Archive, Spam, Trash, "Events", "Reference"}},
			{Name: "Club", Email: "hello@example.net", Folders: []string{Inbox, Sent, Archive, Spam, Trash}},
		},
	}

	add := func(message Message) {
		box.nextID++
		message.ID = fmt.Sprintf("m%d", box.nextID)
		box.messages = append(box.messages, message)
	}

	me := Address{Name: "Sam Rivers", Email: "me@example.com"}
	work := Address{Name: "Sam Rivers", Email: "me@example.org"}
	club := Address{Name: "Hollow Lane Club", Email: "hello@example.net"}

	add(Message{
		Account: "Personal", Folder: Inbox, Unread: true,
		From:    Address{Name: "Northline Rail", Email: "tickets@example.com"},
		To:      []Address{me},
		Subject: "Your tickets: Easton Central to Millbrook",
		Date:    now.Add(-25 * time.Minute),
		HTML:    sampleTicketHTML,
		Text:    "Booking reference NR8K2QX. Outbound Fri 12 Dec 07:42 Easton Central to Millbrook, coach C seat 41. Return Sun 14 Dec 18:15. Total paid 64.20.",
		Attachments: []Attachment{
			{Name: "eticket-NR8K2QX.pdf", ContentType: "application/pdf", Data: []byte(samplePDF)},
		},
	})

	add(Message{
		Account: "Personal", Folder: Inbox, Unread: true,
		From:    Address{Name: "Alex Moreno", Email: "alex@example.com"},
		To:      []Address{me},
		Subject: "Saturday walk?",
		Date:    now.Add(-2 * time.Hour),
		Text:    "Hey Sam,\n\nFancy the ridge walk on Saturday if the weather holds? I was thinking we meet at the bridge car park around 9.\n\nI'll bring the flask.\n\nAlex",
	})

	add(Message{
		Account: "Work", Folder: Inbox, Unread: true,
		From:    Address{Name: "Priya Shah", Email: "priya@example.org"},
		To:      []Address{work},
		Subject: "Board revision B review notes",
		Date:    now.Add(-3 * time.Hour),
		Text:    "Hi Sam,\n\nNotes from the review this morning:\n\n- Move the test pads away from the antenna keep-out\n- Swap R14 to 0402 to free up space\n- Double check the regulator footprint against the new datasheet\n\nCan we aim to send revision B to the fab by Friday?\n\nPriya",
	})

	add(Message{
		Account: "Club", Folder: Inbox, Unread: true,
		From:    Address{Name: "Jordan Lee", Email: "jordan@example.net"},
		To:      []Address{club},
		Subject: "Winter social: invite attached",
		Date:    now.Add(-5 * time.Hour),
		Text:    "Hi all,\n\nThe winter social is on Thursday 4 December at The Lantern Room. The calendar invite is attached.\n\nPlease reply by Monday so we know numbers for food.\n\nJordan",
		Attachments: []Attachment{
			{Name: "winter-social.ics", ContentType: "text/calendar", Data: []byte(sampleInvite)},
		},
	})

	add(Message{
		Account: "Personal", Folder: Inbox,
		From:    Address{Name: "Skylark Air", Email: "noreply@example.com"},
		To:      []Address{me},
		Subject: "Trip confirmed: LHX to OSV, 22 January",
		Date:    now.Add(-1*day - 4*time.Hour),
		HTML:    sampleFlightHTML,
		Text:    "Your trip is confirmed. Flight SK 214, Thu 22 Jan 09:10 LHX to OSV, seat 17A, 1 cabin bag. Check-in opens 24 hours before departure.",
	})

	add(Message{
		Account: "Personal", Folder: Inbox,
		From:    Address{Name: "Bramble Books", Email: "news@example.com"},
		To:      []Address{me},
		Subject: "Long nights, good books",
		Date:    now.Add(-1*day - 9*time.Hour),
		HTML:    sampleNewsletterHTML,
		Text:    "Six new arrivals for the darker evenings, plus a members' evening on the 9th with mulled cider and a reading from our staff picks.",
	})

	add(Message{
		Account: "Work", Folder: Inbox,
		From:    Address{Name: "Chris Okafor", Email: "chris@example.org"},
		To:      []Address{work},
		Subject: "Re: Sensor calibration run",
		Date:    now.Add(-2*day - 1*time.Hour),
		Text:    "Calibration finished overnight. All twelve units are within spec apart from unit 7, which drifts about 0.4 degrees after an hour. I've put it aside for you to look at.\n\nChris\n\n> Can you kick off the calibration run before you leave?\n> Sam",
	})

	add(Message{
		Account: "Club", Folder: Inbox,
		From:    Address{Name: "Morgan Hale", Email: "morgan@example.net"},
		To:      []Address{club},
		Subject: "Membership renewal question",
		Date:    now.Add(-3 * day),
		Text:    "Hello,\n\nMy membership lapsed in October. Can I renew at the old rate, or do I need to join again?\n\nThanks,\nMorgan",
	})

	add(Message{
		Account: "Personal", Folder: "Events",
		From:    Address{Name: "Fernwood Hotel", Email: "stay@example.com"},
		To:      []Address{me},
		Subject: "Your receipt from Fernwood Hotel",
		Date:    now.Add(-9 * day),
		HTML:    sampleReceiptHTML,
		Text:    "Receipt: 2 nights double room 238.00, breakfast 24.00, total 262.00.",
	})

	add(Message{
		Account: "Personal", Folder: "Reference",
		From:    Address{Name: "Harbour Insurance", Email: "policies@example.com"},
		To:      []Address{me},
		Subject: "Your home insurance policy documents",
		Date:    now.Add(-40 * day),
		Text:    "Your policy number is HI-447120. Your documents are attached. Keep this email for your records.",
		Attachments: []Attachment{
			{Name: "policy-schedule.pdf", ContentType: "application/pdf", Data: []byte(samplePDF)},
			{Name: "terms.pdf", ContentType: "application/pdf", Data: []byte(samplePDF)},
		},
	})

	add(Message{
		Account: "Work", Folder: "Reference",
		From:    Address{Name: "IT Desk", Email: "it@example.org"},
		To:      []Address{work},
		Subject: "VPN setup instructions",
		Date:    now.Add(-60 * day),
		Text:    "To connect to the office network:\n\n1. Install the client from the portal\n2. Sign in with your work address\n3. Pick the nearest gateway\n\nAsk the IT desk if anything goes wrong.",
	})

	add(Message{
		Account: "Work", Folder: "Events",
		From:    Address{Name: "Expo Hall", Email: "visitors@example.org"},
		To:      []Address{work},
		Subject: "Your badge for Hardware Expo",
		Date:    now.Add(-14 * day),
		Text:    "Your visitor badge for Hardware Expo, 3 to 5 March. Bring this email or the attached badge to the entrance.",
		Attachments: []Attachment{
			{Name: "badge.pdf", ContentType: "application/pdf", Data: []byte(samplePDF)},
		},
	})

	for i, subject := range []string{"Weekend plans", "Photos from the lake", "Bike lock code", "Book club pick", "Dinner on Friday"} {
		add(Message{
			Account: "Personal", Folder: Archive,
			From:    Address{Name: "Alex Moreno", Email: "alex@example.com"},
			To:      []Address{me},
			Subject: subject,
			Date:    now.Add(-time.Duration(5+i*6) * day),
			Text:    "Archived sample message about " + subject + ".",
		})
	}

	for i, subject := range []string{"Sprint planning", "Supplier quote", "Lab booking", "Firmware release notes"} {
		add(Message{
			Account: "Work", Folder: Archive,
			From:    Address{Name: "Priya Shah", Email: "priya@example.org"},
			To:      []Address{work},
			Subject: subject,
			Date:    now.Add(-time.Duration(4+i*9) * day),
			Text:    "Archived sample message about " + subject + ".",
		})
	}

	add(Message{
		Account: "Personal", Folder: Sent,
		From:    me,
		To:      []Address{{Name: "Alex Moreno", Email: "alex@example.com"}},
		Subject: "Re: Photos from the lake",
		Date:    now.Add(-6 * day),
		Text:    "These are great, thanks! The one of the heron is going on the wall.",
	})

	add(Message{
		Account: "Personal", Folder: Spam, Unread: true,
		From:    Address{Name: "Prize Desk", Email: "winner@example.com"},
		To:      []Address{me},
		Subject: "You have been selected!!!",
		Date:    now.Add(-7 * time.Hour),
		Text:    "Claim your reward today by confirming your details.",
	})

	add(Message{
		Account: "Work", Folder: Spam,
		From:    Address{Name: "Fast Leads", Email: "sales@example.org"},
		To:      []Address{work},
		Subject: "10,000 verified contacts for your business",
		Date:    now.Add(-2 * day),
		Text:    "Grow your pipeline with our verified contact lists.",
	})

	add(Message{
		Account: "Club", Folder: Trash,
		From:    Address{Name: "Hall Bookings", Email: "bookings@example.net"},
		To:      []Address{club},
		Subject: "Old booking request",
		Date:    now.Add(-20 * day),
		Text:    "This booking request has expired.",
	})

	return box
}
