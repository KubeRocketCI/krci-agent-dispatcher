package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

// maxLine is the longest stream line read; longer lines are skipped.
const maxLine = 16 << 20

// stream is what a run's stdout told. saved is the error of onInit.
type stream struct {
	text    string
	result  bool
	started bool
	saved   error
}

type event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
}

// readStream reads JSON lines from r until EOF or a read error. The init event counts only for conversation; onInit,
// when not nil, runs at the first one, before the next line is read.
func readStream(r io.Reader, conversation string, onInit func() error) stream {
	var (
		s    stream
		line []byte
		long bool
	)

	br := bufio.NewReader(r)

	for {
		chunk, err := br.ReadSlice('\n')
		if !long && len(line)+len(chunk) > maxLine+1 {
			long, line = true, line[:0]
		}

		if !long {
			line = append(line, chunk...)
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if !long {
			s.add(line, conversation, onInit)
		}

		line, long = line[:0], false

		if err != nil {
			return s
		}
	}
}

func (s *stream) add(line []byte, conversation string, onInit func() error) {
	var e event
	if json.Unmarshal(line, &e) != nil {
		return
	}

	switch {
	case e.Type == "result":
		s.text, s.result = e.Result, true
	case e.Type == "system" && e.Subtype == "init" && e.SessionID == conversation:
		if !s.started && onInit != nil {
			s.saved = onInit()
		}

		s.started = true
	}
}
