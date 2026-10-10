package streamer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Event
	}{
		{
			name: "version header",
			in:   "##V=9.0.0",
			want: Event{Type: EventVersion, Version: "9.0.0"},
		},
		{
			name: "version with trailing whitespace",
			in:   "##V=9.0.0 ",
			want: Event{Type: EventVersion, Version: "9.0.0"},
		},
		{
			name: "offset origin",
			in:   "##O=web|2049-1234|418",
			want: Event{Type: EventOffset, FileID: "web", FileIdentity: "2049-1234", Offset: 418},
		},
		{
			name: "simple file line",
			in:   "##F=web|2049-1234|438|GET /api/method/foo",
			want: Event{Type: EventLine, FileID: "web", FileIdentity: "2049-1234", Offset: 438, Content: "GET /api/method/foo"},
		},
		{
			name: "content containing the protocol pipe",
			in:   "##F=web|2049-1234|444|line with a | pipe in it",
			want: Event{Type: EventLine, FileID: "web", FileIdentity: "2049-1234", Offset: 444, Content: "line with a | pipe in it"},
		},
		{
			name: "empty content",
			in:   "##F=web|2049-1234|445|",
			want: Event{Type: EventLine, FileID: "web", FileIdentity: "2049-1234", Offset: 445, Content: ""},
		},
		{
			name: "file error MISSING",
			in:   "##F=slowq ##E=MISSING",
			want: Event{Type: EventFileError, FileID: "slowq", ErrorToken: "MISSING"},
		},
		{
			name: "file error PERMISSION",
			in:   "##F=err ##E=PERMISSION",
			want: Event{Type: EventFileError, FileID: "err", ErrorToken: "PERMISSION"},
		},
		{
			name: "file error TAIL_EXITED",
			in:   "##F=web ##E=TAIL_EXITED",
			want: Event{Type: EventFileError, FileID: "web", ErrorToken: "TAIL_EXITED"},
		},
		{
			name: "empty line is unknown",
			in:   "",
			want: Event{Type: EventUnknown},
		},
		{
			name: "future-prefix is unknown (forward compat)",
			in:   "##X=newthing",
			want: Event{Type: EventUnknown},
		},
		{
			name: "legacy line without position is unknown",
			in:   "##F=web|old content",
			want: Event{Type: EventUnknown},
		},
		{
			name: "negative offset is unknown",
			in:   "##O=web|2049-1234|-1",
			want: Event{Type: EventUnknown},
		},
		{
			name: "free-form text is unknown",
			in:   "just some text from somewhere",
			want: Event{Type: EventUnknown},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseLine(c.in)
			require.Equal(t, c.want, got)
		})
	}
}
