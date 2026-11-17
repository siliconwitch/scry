package imapsync

import "testing"

func TestPlainText(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "indented signature table",
			html: `<table>
    <tr>
        <td><img src="data:image/png;base64,ZmFrZQ==" alt="Hollow Lane Club"></td>
        <td>
            <span>Kit Hale</span><br />
            Hollow Lane Club<br />
            <a href="tel:+10000000000">+1 000 000 0000</a>
            &middot;
            <a href="https://example.com">example.com</a>
        </td>
    </tr>
</table>`,
			want: "Kit Hale\nHollow Lane Club\n+1 000 000 0000 · example.com",
		},
		{
			name: "every form of line break",
			html: "one<br>two<br/>three<br />four",
			want: "one\ntwo\nthree\nfour",
		},
		{
			name: "spaces around inline tags",
			html: "<p>Hello <b>Alex</b>, see\n    you  soon</p>",
			want: "Hello Alex, see you soon",
		},
		{
			name: "preformatted text keeps its layout",
			html: "<p>Run:</p><pre>  make\n    test</pre>",
			want: "Run:\n  make\n    test",
		},
		{
			name: "non-breaking spaces stay",
			html: "a&nbsp;&nbsp;b",
			want: "a  b",
		},
		{
			name: "head, styles and scripts are left out",
			html: "<html><head><title>Hidden</title></head><body><style>p{}</style><script>x()</script><p>Hi</p></body></html>",
			want: "Hi",
		},
		{
			name: "unclosed tags",
			html: "<p>unclosed <b>bold",
			want: "unclosed bold",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := PlainText(test.html)

			if got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}
