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
			in:   "##V=8.0.0",
			want: Event{Type: EventVersion, Version: "8.0.0"},
		},
		{
			name: "version with trailing whitespace",
			in:   "##V=8.0.0 ",
			want: Event{Type: EventVersion, Version: "8.0.0"},
		},
		{
			name: "simple file line",
			in:   "##F=web|GET /api/method/foo",
			want: Event{Type: EventLine, FileID: "web", Content: "GET /api/method/foo"},
		},
		{
			name: "content containing the protocol pipe",
			in:   "##F=web|line with a | pipe in it",
			want: Event{Type: EventLine, FileID: "web", Content: "line with a | pipe in it"},
		},
		{
			name: "empty content",
			in:   "##F=web|",
			want: Event{Type: EventLine, FileID: "web", Content: ""},
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
			name: "##F= with no separator is unknown",
			in:   "##F=web",
			want: Event{Type: EventUnknown},
		},
		{
			// Regression: a content line that literally contains " ##E="
			// must parse as a LINE (the pipe comes first), not be
			// misclassified as a file-error sentinel and dropped.
			name: "content containing ' ##E=' is a line, not a file error",
			in:   "##F=web|GET /x ##E=trace",
			want: Event{Type: EventLine, FileID: "web", Content: "GET /x ##E=trace"},
		},
		{
			name: "genuine file-error sentinel (no pipe) still parses",
			in:   "##F=web ##E=MISSING",
			want: Event{Type: EventFileError, FileID: "web", ErrorToken: "MISSING"},
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

func TestEvent_SourceByteLen(t *testing.T) {
	require.Equal(t, 4, Event{Type: EventLine, Content: "abc"}.SourceByteLen(),
		"3 bytes content + 1 byte LF")
	require.Equal(t, 1, Event{Type: EventLine, Content: ""}.SourceByteLen(),
		"empty content still represents one LF byte in the source")
	require.Equal(t, 0, Event{Type: EventVersion}.SourceByteLen(),
		"version events don't consume source bytes")
	require.Equal(t, 0, Event{Type: EventFileError, FileID: "web"}.SourceByteLen(),
		"file errors don't consume source bytes")
}
